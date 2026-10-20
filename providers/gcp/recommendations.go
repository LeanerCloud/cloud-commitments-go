// Package gcp provides GCP recommendations client
package gcp

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"

	"golang.org/x/sync/errgroup"
	"google.golang.org/api/option"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/concurrency"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/services/cloudsql"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/services/cloudstorage"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/services/computeengine"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/services/memorystore"
)

// defaultGCPRegionConcurrency caps the parallel per-region goroutines inside
// a single GetRecommendations call. The GCP Recommender API is project-scoped
// and per-region calls share the project's quota, so the cap is intentionally
// modest. Override at runtime via CUDLY_GCP_REGION_PARALLELISM.
const defaultGCPRegionConcurrency = 10

// gcpRegionConcurrency reads the CUDLY_GCP_REGION_PARALLELISM env var and
// returns its positive-integer value, falling back to
// defaultGCPRegionConcurrency on unset / invalid / non-positive values. The
// helper is local to the gcp package because the providers/gcp module is a
// separate Go module from internal/execution and cannot import its
// ConcurrencyFromEnv counterpart directly.
func gcpRegionConcurrency() int {
	if v := os.Getenv("CUDLY_GCP_REGION_PARALLELISM"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultGCPRegionConcurrency
}

// regionResult bundles the recommendations returned for a single GCP region.
// The merge in GetRecommendations walks regions in sorted order so output is
// deterministic independent of goroutine completion order.
//
// Only the Compute Engine commitment recommender is part of the region
// fan-out. Cloud Storage has no commitment product (issue #78), and Cloud SQL
// and Memorystore CUDs are spend-based and recommended only at Cloud Billing
// account scope, not by their project PerformanceRecommenders (issue #294), so
// only an explicit request reaches those clients, which return
// common.ErrCommitmentPurchaseNotSupported.
type regionResult struct {
	lastErr error
	compute []common.Recommendation
	// attempted counts the service calls launched for this region (after the
	// params service filter), failed counts how many of those errored, and
	// lastErr keeps one representative error. mergeRegionResults aggregates
	// these across regions for the all-attempted-failed guard (COR-03).
	attempted int
	failed    int
}

// RecommendationsClientAdapter aggregates GCP CUD and commitment recommendations across all services.
type RecommendationsClientAdapter struct {
	// regionsFn replaces the live region lookup; tests only.
	regionsFn  func(context.Context) ([]string, error)
	projectID  string
	clientOpts []option.ClientOption
}

// GetRecommendations retrieves all GCP commitment recommendations across all
// services and regions.
//
// Two-level concurrent fan-out:
//   - Outer: errgroup over regions, capped at gcpRegionConcurrency()
//     (CUDLY_GCP_REGION_PARALLELISM, default 10) to stay polite to the
//     project-scoped Recommender API quota.
//   - Inner: each region's goroutine calls the Compute Engine commitment
//     recommender, the only GCP service with a project-scoped commitment
//     recommender (issue #294).
//
// Behavior change vs the previous nested for-loops: per-(region, service)
// errors that were previously silently swallowed (`if err == nil { ... }`
// shape) are now logged at WARN with region+service identifiers so
// misconfigured projects are diagnosable. Errors do NOT cancel siblings —
// each goroutine returns nil to its errgroup, matching the previous
// silent-skip-on-err semantics.
//
// After the outer Wait(), ctx.Err() is checked and propagated so a canceled
// parent ctx surfaces as context.Canceled / context.DeadlineExceeded rather
// than being swallowed by the per-region error-isolation goroutines.
//
// Mirrors the Azure parallelisation in
// providers/azure/recommendations.go (closes #258, commit b10326c5) and the
// AWS service-loop parallelisation (closes #266).
func (r *RecommendationsClientAdapter) GetRecommendations(ctx context.Context, p *common.RecommendationParams) ([]common.Recommendation, error) {
	if p == nil {
		return nil, fmt.Errorf("params cannot be nil")
	}
	// Context cancellation is terminal: bail out before any API fan-out.
	// Newer cloud.google.com/go/compute REST clients can complete a regions
	// List call (and return a real 403) even when ctx is already canceled,
	// which would otherwise be swallowed by the permission branch below.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	params := *p
	if isUnsupportedRecommendationService(params.Service) {
		return r.collectUnsupportedRecs(ctx, params)
	}

	// Get list of regions to check
	regions, err := r.getRegions(ctx)
	if err != nil {
		// Permission errors (403 / missing compute.regions.list) mean the
		// service account lacks Compute Viewer on this project. Log at Warn
		// so it doesn't spam as ERROR in Lambda — the application-layer auth
		// still works; only GCP recommendations for this account are skipped.
		// See issue #247. A canceled ctx never reaches this branch (guarded
		// above), so a genuine 403 is the only thing swallowed here.
		if IsPermissionError(err) {
			logging.Warnf("GCP account %s: skipping recommendations — insufficient Compute permission to list regions (grant roles/compute.viewer): %v", r.projectID, err)
			return []common.Recommendation{}, nil
		}
		return nil, fmt.Errorf("failed to get regions: %w", err)
	}

	// Collect per-region results into a map keyed by region name. The merge
	// step walks regions in sorted order so the output is deterministic
	// independent of goroutine completion order — keeps snapshot tests
	// stable.
	var (
		mu      sync.Mutex
		results = make(map[string]regionResult, len(regions))
	)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(gcpRegionConcurrency())

	for _, region := range regions {
		region := region // capture per-iteration
		g.Go(func() error {
			res := r.collectRegion(gctx, params, region)
			mu.Lock()
			results[region] = res
			mu.Unlock()
			return nil // error isolation: per-region failures don't cancel siblings
		})
	}

	// Wait for all region goroutines. g.Wait() always returns nil because
	// every goroutine returns nil — errors are logged inside collectRegion.
	// After Wait, propagate ctx cancellation so callers can distinguish
	// "all regions completed (with possibly per-region errors)" from "the
	// parent ctx was canceled mid-fan-out".
	_ = g.Wait() //nolint:errcheck // always nil: every g.Go closure above returns nil unconditionally, isolating per-region errors into results[region] instead of propagating them through errgroup
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Deterministic merge: walk regions in sorted order. Output is stable
	// regardless of GCP API region-list ordering or goroutine completion order.
	sortedRegions := make([]string, 0, len(results))
	for region := range results {
		sortedRegions = append(sortedRegions, region)
	}
	sort.Strings(sortedRegions)

	return mergeRegionResults(sortedRegions, results)
}

// mergeRegionResults appends each region's recommendations in the
// (sorted) order given so output is deterministic, and ports the AWS 08-H4
// all-failed guard from providers/aws/recommendations/client.go: when every
// attempted (region, service) call errored (e.g. an expired credential, a
// project-wide throttle, or an RBAC gap), it returns a wrapped error instead
// of an empty-but-nil-error result. Returning (recs, nil) on a total failure
// makes a broken run indistinguishable from "no savings available": the
// scheduler would count the account as succeeded, evict its previously
// collected rows, and clear last_collection_error (COR-03). Partial failure
// is tolerated: if at least one call succeeded, the successful results are
// returned with a nil error (failures were already logged at WARN in
// collectRegion).
func mergeRegionResults(sortedRegions []string, results map[string]regionResult) ([]common.Recommendation, error) {
	attempted := 0
	failed := 0
	var lastErr error
	total := 0
	for _, region := range sortedRegions {
		res := results[region]
		total += len(res.compute)
	}
	merged := make([]common.Recommendation, 0, total)
	for _, region := range sortedRegions {
		res := results[region]
		attempted += res.attempted
		failed += res.failed
		if res.lastErr != nil {
			lastErr = res.lastErr
		}
		merged = append(merged, res.compute...)
	}
	if failed > 0 && failed == attempted {
		return nil, fmt.Errorf("all %d GCP recommendation service calls failed across %d regions: %w", failed, len(sortedRegions), lastErr)
	}
	return merged, nil
}

// collectComputeRecs fetches Compute Engine CUD recommendations for one region.
// Handles semaphore acquire/release and client construction so these branches
// are not counted toward collectRegion's cyclomatic complexity.
func (r *RecommendationsClientAdapter) collectComputeRecs(ctx context.Context, params common.RecommendationParams, region string) ([]common.Recommendation, error) {
	if err := concurrency.Acquire(ctx); err != nil {
		return nil, err
	}
	defer concurrency.Release(ctx)
	client, err := computeengine.NewClient(r.projectID, region, r.clientOpts...)
	if err != nil {
		return nil, err
	}
	return client.GetRecommendations(ctx, &params)
}

// isUnsupportedRecommendationService reports whether service has no
// project-scoped commitment recommender: Cloud Storage has no commitment
// product (issue #78), and Cloud SQL and Memorystore CUDs are spend-based and
// recommended only at Cloud Billing account scope (issue #294).
func isUnsupportedRecommendationService(service common.ServiceType) bool {
	switch service {
	case common.ServiceStorage, common.ServiceRelationalDB, common.ServiceCache:
		return true
	default:
		return false
	}
}

// collectUnsupportedRecs returns the service client's not-supported error for
// an explicit Cloud Storage, Cloud SQL or Memorystore request, once and before
// any region lookup or Recommender call.
func (r *RecommendationsClientAdapter) collectUnsupportedRecs(ctx context.Context, params common.RecommendationParams) ([]common.Recommendation, error) {
	var (
		client provider.ServiceClient
		err    error
	)
	switch params.Service {
	case common.ServiceRelationalDB:
		client, err = cloudsql.NewClient(ctx, r.projectID, "", r.clientOpts...)
	case common.ServiceCache:
		client, err = memorystore.NewClient(ctx, r.projectID, "", r.clientOpts...)
	default:
		client, err = cloudstorage.NewClient(ctx, r.projectID, "", r.clientOpts...)
	}
	if err != nil {
		return nil, err
	}
	return client.GetRecommendations(ctx, &params)
}

// collectRegion fetches Compute Engine CUD recommendations for a single
// region. An error is logged at WARN with the region tag and does not fail the
// region on its own; the returned regionResult carries the attempted/failed
// call counts and the error so mergeRegionResults can fail loud when EVERY
// attempted call across all regions errored (COR-03).
func (r *RecommendationsClientAdapter) collectRegion(ctx context.Context, params common.RecommendationParams, region string) regionResult {
	if !shouldIncludeService(params, common.ServiceCompute) {
		return regionResult{}
	}
	recs, err := r.collectComputeRecs(ctx, params, region)
	if err != nil {
		logging.Warnf("GCP %s compute recommendations: %v", region, err)
		return regionResult{attempted: 1, failed: 1, lastErr: err}
	}
	return regionResult{compute: recs, attempted: 1}
}

// GetRecommendationsForService retrieves GCP commitment recommendations for a specific service.
func (r *RecommendationsClientAdapter) GetRecommendationsForService(ctx context.Context, service common.ServiceType) ([]common.Recommendation, error) {
	params := common.RecommendationParams{
		Service: service,
	}
	return r.GetRecommendations(ctx, &params)
}

// GetAllRecommendations retrieves all GCP commitment recommendations across all services.
func (r *RecommendationsClientAdapter) GetAllRecommendations(ctx context.Context) ([]common.Recommendation, error) {
	params := common.RecommendationParams{}
	return r.GetRecommendations(ctx, &params)
}

// getRegions retrieves available GCP regions for the project.
func (r *RecommendationsClientAdapter) getRegions(ctx context.Context) ([]string, error) {
	if r.regionsFn != nil {
		return r.regionsFn(ctx)
	}
	// Create a temporary provider to get regions. The local variable is named
	// p (not provider) to avoid shadowing the imported provider package (10-N3).
	p := NewProviderWithProject(ctx, r.projectID, r.clientOpts...)

	regions, err := p.GetRegions(ctx)
	if err != nil {
		return nil, err
	}

	regionNames := make([]string, 0, len(regions))
	for _, region := range regions {
		regionNames = append(regionNames, region.ID)
	}

	return regionNames, nil
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
