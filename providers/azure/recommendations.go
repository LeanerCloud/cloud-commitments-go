// Package azure provides Azure recommendations client
package azure

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/advisor/armadvisor"
	"golang.org/x/sync/errgroup"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/concurrency"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	azrecs "github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/recommendations"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/cache"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/compute"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/cosmosdb"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/database"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/savingsplans"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/synapse"
)

// serviceRecsGetter is the narrow interface satisfied by each per-service
// client (compute, database, cache, cosmosdb). The interface exists solely to
// allow tests to substitute fake implementations; production code uses the
// concrete types via the newXxxClientFn variables below.
type serviceRecsGetter interface {
	GetRecommendations(ctx context.Context, params *common.RecommendationParams) ([]common.Recommendation, error)
}

// newComputeClientFn, newDatabaseClientFn, newCacheClientFn,
// newCosmosDBClientFn, and newSavingsPlansClientFn default to the real
// constructors and are overridden in tests to inject fakes. The variables are
// package-level (not fields on the adapter) so that the constructor signature
// stays unchanged and the injection is limited to the test package that owns
// the test binary's address space.
//
// getAdvisorRecsFn wraps r.getAdvisorRecommendations; it is a field rather
// than a package-level var so that injection is scoped to the adapter
// instance and avoids shared-state issues when tests run the advisor path
// concurrently. Tests that do not need real ARM calls set it to noopAdvisorFn.
var (
	newComputeClientFn func(azcore.TokenCredential, string, string) serviceRecsGetter = func(cred azcore.TokenCredential, sub, region string) serviceRecsGetter {
		return compute.NewClient(cred, sub, region)
	}
	newDatabaseClientFn func(azcore.TokenCredential, string, string) serviceRecsGetter = func(cred azcore.TokenCredential, sub, region string) serviceRecsGetter {
		return database.NewClient(cred, sub, region)
	}
	newCacheClientFn func(azcore.TokenCredential, string, string) serviceRecsGetter = func(cred azcore.TokenCredential, sub, region string) serviceRecsGetter {
		return cache.NewClient(cred, sub, region)
	}
	newCosmosDBClientFn func(azcore.TokenCredential, string, string) serviceRecsGetter = func(cred azcore.TokenCredential, sub, region string) serviceRecsGetter {
		return cosmosdb.NewClient(cred, sub, region)
	}
	newSavingsPlansClientFn func(azcore.TokenCredential, string, string) serviceRecsGetter = func(cred azcore.TokenCredential, sub, region string) serviceRecsGetter {
		return savingsplans.NewClient(cred, sub, region)
	}
	newSynapseClientFn func(azcore.TokenCredential, string, string) serviceRecsGetter = func(cred azcore.TokenCredential, sub, region string) serviceRecsGetter {
		return synapse.NewClient(cred, sub, region)
	}
)

// RecommendationsClientAdapter aggregates Azure reservation recommendations across all services.
//
// Invariant: subscriptionID must be non-empty. Downstream converters use it as
// the Recommendation.Account field; an empty subscriptionID would silently
// produce Account="" recommendations that downstream consumers (account-scoped
// caches, UI filters, billing reports) can't route. The canonical construction
// path is NewRecommendationsClientAdapter; direct struct literals bypass the
// invariant check and should be confined to tests that deliberately exercise
// the unvalidated shape.
//
// getAdvisorRecsFn defaults to r.getAdvisorRecommendations and may be
// overridden per-instance in tests to avoid real ARM network calls.
type RecommendationsClientAdapter struct {
	cred             azcore.TokenCredential
	subscriptionID   string
	getAdvisorRecsFn func(ctx context.Context, params common.RecommendationParams) ([]common.Recommendation, error)
}

// NewRecommendationsClientAdapter builds a RecommendationsClientAdapter with
// the subscriptionID-non-empty invariant enforced. Returns an error when
// subscriptionID is the empty string so the caller sees the mis-wiring at
// construction time rather than via confusing Account="" rows later.
func NewRecommendationsClientAdapter(cred azcore.TokenCredential, subscriptionID string) (*RecommendationsClientAdapter, error) {
	if subscriptionID == "" {
		return nil, fmt.Errorf("azure recommendations: subscriptionID is required")
	}
	r := &RecommendationsClientAdapter{
		cred:           cred,
		subscriptionID: subscriptionID,
	}
	r.getAdvisorRecsFn = r.getAdvisorRecommendations
	return r, nil
}

// GetRecommendations retrieves all Azure reservation recommendations across services.
//
// The Azure Consumption Reservation Recommendations API is subscription-scoped:
// the response covers every region in one call. Iterating regions and calling each
// service per region (the previous behavior) produced ~60× duplicate results,
// hammered the rate limit, and meant downstream consumers had to deduplicate.
// We now call each service client exactly once. Region is intentionally left
// blank on the client — converters must populate Region from the response data
// (see known_issues/10_azure_provider.md CRITICAL "Recommendation converters
// ignore the API response entirely" for the matching converter work).
//
// All seven service calls run concurrently under errgroup. Each goroutine captures
// its own error and returns nil to the group so that a single service failure
// does not cancel sibling calls. Results are appended in a deterministic order
// (compute → database → cache → cosmosdb → synapse → savingsplans → advisor) after all goroutines finish.
func (r *RecommendationsClientAdapter) GetRecommendations(ctx context.Context, params *common.RecommendationParams) ([]common.Recommendation, error) {
	if params == nil {
		return nil, fmt.Errorf("params cannot be nil")
	}
	var (
		computeRecs, dbRecs, cacheRecs, cosmosRecs, advisorRecs, spRecs, synapseRecs []common.Recommendation
		computeErr, dbErr, cacheErr, cosmosErr, advisorErr, spErr, synapseErr        error
	)

	g, gctx := errgroup.WithContext(ctx)
	gctx, skips := azrecs.WithSkipLog(gctx)

	// Each per-service goroutine is a leaf — it issues the actual ARM /
	// pricing-API call. The shared semaphore on gctx (set by the scheduler)
	// bounds aggregate concurrent IO across the whole recommendations-
	// collection fan-out tree at CUDLY_MAX_PARALLELISM (default 20). If no
	// semaphore is attached (CLI tools, unit tests), Acquire/Release are
	// no-ops. See pkg/concurrency.
	//
	// goService extracts the Acquire/Release boilerplate so each per-
	// service block stays a single g.Go call — keeps GetRecommendations
	// under the project's gocyclo gate (the per-service `if Acquire {…}`
	// branches counted toward this function's cyclomatic complexity
	// before extraction).
	goService := func(errOut *error, fn func()) {
		g.Go(func() error {
			if err := concurrency.Acquire(gctx); err != nil {
				*errOut = err
				return nil
			}
			defer concurrency.Release(gctx)
			fn()
			return nil // error isolation: never propagate to errgroup
		})
	}

	// Record which services the params filter lets through so the merge can
	// distinguish "skipped by filter" from "attempted and succeeded with zero
	// recommendations" when applying the all-attempted-failed guard.
	includeCompute := shouldIncludeService(*params, common.ServiceCompute)
	includeDB := shouldIncludeService(*params, common.ServiceRelationalDB)
	includeCache := shouldIncludeService(*params, common.ServiceCache)
	includeCosmos := shouldIncludeService(*params, common.ServiceNoSQL)
	includeSP := shouldIncludeService(*params, common.ServiceSavingsPlansAll)
	includeSynapse := shouldIncludeService(*params, common.ServiceDataWarehouse)

	// Compute (VM) recommendations — subscription-wide.
	if includeCompute {
		goService(&computeErr, func() {
			computeClient := newComputeClientFn(r.cred, r.subscriptionID, "")
			computeRecs, computeErr = computeClient.GetRecommendations(gctx, params)
		})
	}

	// Database (SQL) recommendations — subscription-wide.
	if includeDB {
		goService(&dbErr, func() {
			dbClient := newDatabaseClientFn(r.cred, r.subscriptionID, "")
			dbRecs, dbErr = dbClient.GetRecommendations(gctx, params)
		})
	}

	// Cache (Redis) recommendations — subscription-wide.
	if includeCache {
		goService(&cacheErr, func() {
			cacheClient := newCacheClientFn(r.cred, r.subscriptionID, "")
			cacheRecs, cacheErr = cacheClient.GetRecommendations(gctx, params)
		})
	}

	// CosmosDB (NoSQL) recommendations — subscription-wide.
	if includeCosmos {
		goService(&cosmosErr, func() {
			cosmosClient := newCosmosDBClientFn(r.cred, r.subscriptionID, "")
			cosmosRecs, cosmosErr = cosmosClient.GetRecommendations(gctx, params)
		})
	}

	// Synapse (SqlDataWarehouse) recommendations — subscription-wide. Search and
	// Managed Redis are deliberately not collected here: see #47.
	if includeSynapse {
		goService(&synapseErr, func() {
			synapseRecs, synapseErr = newSynapseClientFn(r.cred, r.subscriptionID, "").GetRecommendations(gctx, params)
		})
	}

	// Savings Plans — Azure has no stable public API for SP purchase
	// recommendations (Benefits Recommendations API is still in preview).
	// The call returns an empty slice so the service appears in the fan-out
	// and will start returning data once the API stabilizes without requiring
	// a scheduler change.
	if includeSP {
		goService(&spErr, func() {
			spClient := newSavingsPlansClientFn(r.cred, r.subscriptionID, "")
			spRecs, spErr = spClient.GetRecommendations(gctx, params)
		})
	}

	// Azure Advisor adds cross-cutting cost recommendations independent of the
	// per-service Reservation API. Failures here are non-fatal — the per-service
	// results above are still useful on their own.
	//
	// getAdvisorRecsFn defaults to r.getAdvisorRecommendations and may be
	// replaced per-adapter in tests to avoid real ARM network calls.
	advisorFn := r.advisorFn()
	goService(&advisorErr, func() {
		advisorRecs, advisorErr = collectAdvisorOrSkip(gctx, advisorFn, *params)
	})

	// Wait for all goroutines. g.Wait() always returns nil because every
	// goroutine returns nil — errors are captured in per-service variables
	// above. After Wait, propagate ctx cancellation so callers can distinguish
	// "all five services completed (with possibly per-service errors)" from
	// "the parent ctx was canceled mid-fan-out". Without this check the
	// CHECK could swallow a deadline exceeded that the caller expected to
	// see.
	_ = g.Wait() //nolint:errcheck // always nil by construction, see comment above
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	merged, err := mergeServiceResults(serviceResult{"compute", computeRecs, computeErr, includeCompute},
		serviceResult{"database", dbRecs, dbErr, includeDB},
		serviceResult{"cache", cacheRecs, cacheErr, includeCache},
		serviceResult{"cosmosdb", cosmosRecs, cosmosErr, includeCosmos},
		serviceResult{"synapse", synapseRecs, synapseErr, includeSynapse},
		// The savingsplans client is a stub that unconditionally returns
		// ([], nil) until the Benefits Recommendations API stabilizes (see
		// services/savingsplans Client.GetRecommendations). Counting its
		// built-in success as an attempted service would keep the
		// all-attempted-failed guard from ever firing on a total provider
		// failure (expired credential, subscription-wide throttle), so it is
		// excluded from the guard until it makes real API calls.
		serviceResult{"savingsplans", spRecs, spErr, false},
		// The Advisor client is excluded from the all-attempted-failed guard
		// for the same reason: getAdvisorRecommendations swallows pagination
		// errors (the auth failure from an expired credential surfaces as a
		// 401 during pager.NextPage, not during client construction) and
		// always returns (recs, nil). Counting its unconditional success as
		// an attempted service would keep the guard from firing on a total
		// credential failure -- the same hazard the savingsplans exclusion
		// prevents. When getAdvisorRecommendations is changed to propagate
		// hard errors, flip this flag back to true.
		serviceResult{"advisor", dropCoveredAdvisorSavingsPlans(advisorRecs, spRecs), advisorErr, false})
	return withSkips(merged, err, skips)
}

// withSkips attaches the collection's skipped recommendations (non-USD
// currency, unpriceable SKU) to a merge result as a *azrecs.PartialResultError
// returned alongside the recommendations that did succeed, so an EUR/GBP
// account does not look like "no savings".
func withSkips(recs []common.Recommendation, err error, skips *azrecs.SkipLog) ([]common.Recommendation, error) {
	if err != nil {
		return nil, err
	}
	if skipped := skips.Entries(); len(skipped) > 0 {
		return recs, &azrecs.PartialResultError{Skipped: skipped}
	}
	return recs, nil
}

// dropCoveredAdvisorSavingsPlans removes Advisor Savings Plan rows that the
// savings-plan client (Benefit Recommendations API) already covers, matched on
// (account, term). Advisor's row carries only a saving, no commitment amount,
// so the Benefit Recommendations row is the better record of the same advice.
// Matching on a key, not dropping all Advisor SP rows, keeps Advisor's advice
// when that client returns nothing for an account and term.
func dropCoveredAdvisorSavingsPlans(advisor, sp []common.Recommendation) []common.Recommendation {
	type key struct{ account, term string }
	covered := make(map[key]bool)
	for i := range sp {
		if sp[i].CommitmentType == common.CommitmentSavingsPlan {
			covered[key{sp[i].Account, sp[i].Term}] = true
		}
	}
	if len(covered) == 0 {
		return advisor
	}
	out := make([]common.Recommendation, 0, len(advisor))
	for i := range advisor {
		if advisor[i].CommitmentType == common.CommitmentSavingsPlan && covered[key{advisor[i].Account, advisor[i].Term}] {
			continue
		}
		out = append(out, advisor[i])
	}
	return out
}

// advisorFn returns the injected Advisor collector, or the real one.
func (r *RecommendationsClientAdapter) advisorFn() func(context.Context, common.RecommendationParams) ([]common.Recommendation, error) {
	if r.getAdvisorRecsFn != nil {
		return r.getAdvisorRecsFn
	}
	return r.getAdvisorRecommendations
}

// serviceResult bundles a per-service collection outcome for the deterministic
// merge in mergeServiceResults. Extracted into a helper so GetRecommendations
// stays under the cyclomatic-complexity gate after the post-Wait ctx.Err()
// propagation was added.
type serviceResult struct {
	name string
	recs []common.Recommendation
	err  error
	// attempted records whether the service call was actually launched
	// (i.e. not skipped by the params service filter). Skipped services
	// carry nil recs and nil err, which is indistinguishable from a
	// successful zero-recommendation call, so the all-attempted-failed
	// guard below needs this flag to avoid counting skips as successes.
	attempted bool
}

// mergeServiceResults logs per-service errors (matches the previous sequential
// behavior where each error was logged inline via logging.Warnf) and appends
// successful results in the order the slice is passed — callers must preserve
// the canonical compute → database → cache → cosmosdb → synapse → savingsplans → advisor
// order so that order-sensitive consumers remain stable. The advisor entry's
// error is logged via logging.Errorf to match the pre-parallelisation severity.
//
// Partial failure is tolerated: as long as at least one attempted service
// succeeded, the successful services' recommendations are returned with a nil
// error. But when EVERY attempted service errored (e.g. an expired federated
// credential or a subscription-wide throttle), the merge returns a wrapped
// error instead of an empty-but-nil-error result, porting the AWS 08-H4 guard
// from providers/aws/recommendations/client.go. Returning (recs, nil) on a
// total failure makes a broken run indistinguishable from "no savings
// available": the scheduler would count the account as succeeded, evict its
// previously collected rows, and clear last_collection_error (COR-03).
func mergeServiceResults(results ...serviceResult) ([]common.Recommendation, error) {
	total := 0
	attempted := 0
	failures := 0
	var lastErr error
	for _, r := range results {
		total += len(r.recs)
		if !r.attempted {
			continue
		}
		attempted++
		if r.err != nil {
			failures++
			lastErr = r.err
		}
	}
	out := make([]common.Recommendation, 0, total)
	for _, r := range results {
		if r.err != nil {
			if r.name == "advisor" {
				logging.Errorf("Failed to get Azure Advisor recommendations: %v", r.err)
			} else {
				logging.Warnf("Azure %s recommendations: %v", r.name, r.err)
			}
			continue
		}
		out = append(out, r.recs...)
	}
	if failures > 0 && failures == attempted {
		return nil, fmt.Errorf("all %d Azure recommendation services failed: %w", failures, lastErr)
	}
	return out, nil
}

// GetRecommendationsForService retrieves Azure reservation recommendations for a specific service.
func (r *RecommendationsClientAdapter) GetRecommendationsForService(ctx context.Context, service common.ServiceType) ([]common.Recommendation, error) {
	params := common.RecommendationParams{
		Service: service,
	}
	return r.GetRecommendations(ctx, &params)
}

// GetAllRecommendations retrieves all Azure reservation recommendations across all services.
func (r *RecommendationsClientAdapter) GetAllRecommendations(ctx context.Context) ([]common.Recommendation, error) {
	params := common.RecommendationParams{}
	return r.GetRecommendations(ctx, &params)
}

// getAdvisorRecommendations retrieves cost optimization recommendations from Azure Advisor.
func (r *RecommendationsClientAdapter) getAdvisorRecommendations(ctx context.Context, params common.RecommendationParams) ([]common.Recommendation, error) {
	client, err := armadvisor.NewRecommendationsClient(r.subscriptionID, r.cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create advisor client: %w", err)
	}

	// Filter for cost recommendations
	filter := "Category eq 'Cost'"
	pager := client.NewListPager(&armadvisor.RecommendationsClientListOptions{
		Filter: &filter,
	})
	return r.collectAdvisorRecommendations(ctx, params, pager)
}

// maxAdvisorPages caps the Advisor cost-recommendation walk; above the
// 10-page cap on the Consumption recommendation walks because Advisor lists
// every resource-level cost recommendation in the subscription.
const maxAdvisorPages = 50

// AdvisorPager is the page iterator collectAdvisorRecommendations walks
// (enables mocking).
type AdvisorPager interface {
	More() bool
	NextPage(ctx context.Context) (armadvisor.RecommendationsClientListResponse, error)
}

// errAdvisorTruncated marks an Advisor walk that stopped before the last
// page (page cap or page error); the recommendations gathered so far are
// returned with it.
var errAdvisorTruncated = errors.New("azure advisor: recommendation list is incomplete")

// collectAdvisorOrSkip runs the Advisor collection and reports a truncated
// walk through the context's skip log, keeping the recommendations gathered so
// far, so GetRecommendations surfaces it as a *azrecs.PartialResultError.
// Any other error is returned unchanged.
func collectAdvisorOrSkip(ctx context.Context, fn func(context.Context, common.RecommendationParams) ([]common.Recommendation, error), params common.RecommendationParams) ([]common.Recommendation, error) {
	recs, err := fn(ctx, params)
	if errors.Is(err, errAdvisorTruncated) {
		azrecs.RecordSkip(ctx, "advisor", err)
		return recs, nil
	}
	return recs, err
}

// collectAdvisorRecommendations walks the Advisor pager. A canceled context
// is an error. Hitting the page cap or a page error returns the
// recommendations collected so far together with errAdvisorTruncated, never a
// bare partial result.
func (r *RecommendationsClientAdapter) collectAdvisorRecommendations(ctx context.Context, params common.RecommendationParams, pager AdvisorPager) ([]common.Recommendation, error) {
	recommendations := make([]common.Recommendation, 0)
	for pageIdx := 0; pager.More(); pageIdx++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("azure advisor: context canceled during pagination: %w", err)
		}
		if pageIdx >= maxAdvisorPages {
			return recommendations, fmt.Errorf("%w: pagination cap (%d pages) reached after %d recommendations", errAdvisorTruncated, maxAdvisorPages, len(recommendations))
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			return recommendations, fmt.Errorf("%w: page error after %d recommendations: %w", errAdvisorTruncated, len(recommendations), err)
		}
		recommendations = r.appendAdvisorPageRecs(ctx, params, page, recommendations)
	}

	return recommendations, nil
}

// appendAdvisorPageRecs converts one Advisor pager page into common recommendations
// and appends them to the provided slice. Pulled out of getAdvisorRecommendations
// to keep that function under the cyclomatic limit.
func (r *RecommendationsClientAdapter) appendAdvisorPageRecs(
	ctx context.Context,
	params common.RecommendationParams,
	page armadvisor.RecommendationsClientListResponse,
	recommendations []common.Recommendation,
) []common.Recommendation {
	for _, advisorRec := range page.Value {
		if advisorRec.Properties == nil {
			continue
		}
		rec := r.convertAdvisorRecommendation(ctx, advisorRec)
		if rec != nil && shouldIncludeService(params, rec.Service) {
			variants := azrecs.ExpandPaymentVariants(*rec)
			recommendations = append(recommendations, variants...)
		}
	}
	return recommendations
}

// resolveAdvisorRegion picks the region for an Advisor recommendation.
// ExtendedProperties["region"|"location"] is authoritative when present;
// the resource-ID parser is a fallback for the rare case where the ID
// embeds /locations/{region}/. Pulled out of convertAdvisorRecommendation
// to keep that function under the cyclomatic limit.
func resolveAdvisorRegion(advisorRec *armadvisor.ResourceRecommendationBase) string {
	if ext := advisorRec.Properties.ExtendedProperties; ext != nil {
		for _, key := range []string{"region", "location"} {
			if v, ok := ext[key]; ok && v != nil && *v != "" {
				return *v
			}
		}
	}
	if advisorRec.ID != nil {
		return extractRegionFromResourceID(*advisorRec.ID)
	}
	return ""
}

// Advisor ExtendedProperties keys. The SDK types ExtendedProperties as a bare
// map[string]*string, so the keys are not in the SDK. annualSavingsAmount,
// savingsCurrency, term and lookbackPeriod are the names Microsoft's Advisor
// Resource Graph samples read from properties.extendedProperties
// (learn.microsoft.com/azure/advisor/advisor-azure-resource-graph);
// reservedResourceType, recommendationSubCategory, sku and qty are the keys
// this collector has always relied on.
const (
	advisorKeySavings  = "annualSavingsAmount"
	advisorKeyCurrency = "savingsCurrency"
	advisorKeyTerm     = "term"
	advisorKeySKU      = "sku"
	advisorKeyQty      = "qty"
	advisorKeyRRT      = "reservedResourceType"
)

// advisorPurchaseKind classifies an Advisor recommendation. Only
// subscription-scoped reservation (reservedResourceType) and Savings Plan
// recommendations are commitment purchases; resource-level recommendations
// such as resize or shutdown are not, and are ignored (ok=false, no error).
// An unmapped reservedResourceType is ignored (debug log), not a skip.
func advisorPurchaseKind(ext map[string]*string) (common.CommitmentType, string, bool) {
	service := serviceFromExtendedProperties(ext)
	if service == "" {
		if v := ext[advisorKeyRRT]; v != nil && *v != "" {
			// Advisor routinely returns reservations for services this
			// collector does not price; they are not ours to convert.
			logging.Debugf("azure advisor: ignoring %s %q", advisorKeyRRT, *v)
		}
		return "", "", false
	}
	if v := ext["recommendationSubCategory"]; v != nil && strings.EqualFold(*v, "SavingsPlan") {
		return common.CommitmentSavingsPlan, service, true
	}
	return common.CommitmentReservedInstance, service, true
}

// convertAdvisorRecommendation converts an Azure Advisor commitment
// recommendation to common format. Payload problems are recorded in the
// context's skip log and yield nil.
//
// Payment option is left empty: the Advisor payload carries no payment-plan
// or price field, and Azure charges the same total for upfront and monthly
// billing, so ExpandPaymentVariants fans it out to both. CommitmentCost stays 0
// and RecurringMonthlyCost nil (unknown) because the payload has no price.
func (r *RecommendationsClientAdapter) convertAdvisorRecommendation(ctx context.Context, advisorRec *armadvisor.ResourceRecommendationBase) *common.Recommendation {
	if advisorRec.Properties == nil {
		return nil
	}
	ext := advisorRec.Properties.ExtendedProperties
	commitmentType, service, ok := advisorPurchaseKind(ext)
	if !ok {
		return nil
	}

	rec := &common.Recommendation{
		Provider:       common.ProviderAzure,
		Service:        common.ServiceType(service),
		Account:        r.subscriptionID,
		CommitmentType: commitmentType,
	}
	rec.Region = resolveAdvisorRegion(advisorRec)

	if err := populateFromExtendedProperties(rec, ext); err != nil {
		azrecs.RecordSkip(ctx, "advisor", fmt.Errorf("%s recommendation in %q: %w", service, rec.Region, err))
		return nil
	}
	return rec
}

// populateFromExtendedProperties fills savings, SKU, term and count from the
// Advisor ExtendedProperties map and errors on anything absent or unknown:
// savings, a USD currency label and a known term are always required; a
// reservation also needs its SKU and a positive quantity.
func populateFromExtendedProperties(rec *common.Recommendation, ext map[string]*string) error {
	annualSavings, err := extFloat(ext, advisorKeySavings)
	if err != nil {
		return err
	}
	if cur := extString(ext, advisorKeyCurrency, ""); !strings.EqualFold(cur, "USD") {
		return fmt.Errorf("%s %q is not USD", advisorKeyCurrency, cur)
	}
	rec.EstimatedSavings = annualSavings / 12

	if rec.Term, err = advisorTerm(ext); err != nil {
		return err
	}
	if rec.CommitmentType == common.CommitmentSavingsPlan {
		return nil
	}
	if rec.ResourceType = extString(ext, advisorKeySKU, ""); rec.ResourceType == "" {
		return fmt.Errorf("%s is absent", advisorKeySKU)
	}
	qty, err := extInt(ext, advisorKeyQty)
	if err != nil {
		return err
	}
	rec.Count = qty
	return nil
}

// advisorTerm maps the Advisor term ("P1Y"/"P3Y", or the already-normalised
// "1yr"/"3yr") to the common form and errors on a missing or unknown value.
func advisorTerm(ext map[string]*string) (string, error) {
	switch t := extString(ext, advisorKeyTerm, ""); t {
	case "P1Y", "1yr":
		return "1yr", nil
	case "P3Y", "3yr":
		return "3yr", nil
	case "":
		return "", fmt.Errorf("%s is absent", advisorKeyTerm)
	default:
		return "", fmt.Errorf("unknown %s %q", advisorKeyTerm, t)
	}
}

func extString(m map[string]*string, key, fallback string) string {
	if v, ok := m[key]; ok && v != nil && *v != "" {
		return *v
	}
	return fallback
}

func extFloat(m map[string]*string, key string) (float64, error) {
	v, ok := m[key]
	if !ok || v == nil {
		return 0, fmt.Errorf("%s is absent", key)
	}
	f, err := strconv.ParseFloat(*v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not a number: %w", key, *v, err)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%s %q is not finite", key, *v)
	}
	if f < 0 {
		return 0, fmt.Errorf("%s %q is negative", key, *v)
	}
	return f, nil
}

func extInt(m map[string]*string, key string) (int, error) {
	v, ok := m[key]
	if !ok || v == nil {
		return 0, fmt.Errorf("%s is absent", key)
	}
	n, err := strconv.Atoi(*v)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not an integer: %w", key, *v, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%s %q is not positive", key, *v)
	}
	return n, nil
}

// serviceFromExtendedProperties resolves service type for subscription-scoped
// recommendations where ImpactedField is "Microsoft.Subscriptions/subscriptions".
func serviceFromExtendedProperties(ext map[string]*string) string {
	if ext == nil {
		return ""
	}
	if rrt, ok := ext["reservedResourceType"]; ok && rrt != nil {
		switch strings.ToLower(*rrt) {
		case "virtualmachines":
			return string(common.ServiceCompute)
		case "sqldatabases":
			return string(common.ServiceRelationalDB)
		case "rediscache":
			return string(common.ServiceCache)
		}
	}
	if subcat, ok := ext["recommendationSubCategory"]; ok && subcat != nil {
		if strings.EqualFold(*subcat, "SavingsPlan") {
			return string(common.ServiceSavingsPlansAll)
		}
	}
	return ""
}

// extractRegionFromResourceID extracts the region from an Azure resource ID.
//
// Standard ARM resource IDs follow the shape
//
//	/subscriptions/{sub}/resourceGroups/{rg}/providers/{ns}/{type}/{name}
//
// and do NOT embed the region — so this helper is a best-effort fallback
// only. Callers that have a better source (Advisor recommendation's
// Properties.ExtendedProperties["region"] / "location", or a sibling
// Location field) must use that first. This helper exists for the rare
// Advisor recommendation whose ID happens to carry a /locations/{region}/
// segment (some reservation-scope resource IDs do).
//
// Returns "" when the ID has no recognizable region segment.
func extractRegionFromResourceID(resourceID string) string {
	// Case-insensitive scan for /locations/{region}/ — Azure is inconsistent
	// between `locations`, `Locations`, `location`.
	lower := strings.ToLower(resourceID)
	for _, marker := range []string{"/locations/", "/location/"} {
		idx := strings.Index(lower, marker)
		if idx < 0 {
			continue
		}
		rest := resourceID[idx+len(marker):]
		// The next / ends the region segment.
		if end := strings.IndexByte(rest, '/'); end >= 0 {
			return rest[:end]
		}
		// No trailing / — the region is the last segment.
		return rest
	}
	return ""
}

// shouldIncludeService checks if a service should be included based on params.
func shouldIncludeService(params common.RecommendationParams, service common.ServiceType) bool {
	// If no service specified in params, include all
	if params.Service == "" {
		return true
	}

	// Check if this is the requested service
	return params.Service == service
}
