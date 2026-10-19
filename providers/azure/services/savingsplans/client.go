// Package savingsplans provides an Azure Savings Plans service client.
package savingsplans

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/billingbenefits/armbillingbenefits"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/httpclient"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/pricing"
)

// SavingsPlanOrderAliasAPI defines operations on SavingsPlanOrderAlias (enables mocking).
type SavingsPlanOrderAliasAPI interface {
	BeginCreate(
		ctx context.Context,
		savingsPlanOrderAliasName string,
		body armbillingbenefits.SavingsPlanOrderAliasModel,
		options *armbillingbenefits.SavingsPlanOrderAliasClientBeginCreateOptions,
	) (SavingsPlanOrderAliasPoller, error)
}

// SavingsPlanOrderAliasPoller abstracts the LRO poller returned by BeginCreate (enables mocking).
type SavingsPlanOrderAliasPoller interface {
	PollUntilDone(ctx context.Context, options *PollOptions) (armbillingbenefits.SavingsPlanOrderAliasClientCreateResponse, error)
}

// PollOptions mirrors arm/runtime.PollUntilDoneOptions to avoid a direct dependency.
type PollOptions struct {
	Frequency time.Duration
}

// SavingsPlanListAllPager abstracts the list-all pager (enables mocking).
type SavingsPlanListAllPager interface {
	More() bool
	NextPage(ctx context.Context) (armbillingbenefits.SavingsPlanClientListAllResponse, error)
}

// RPValidateAPI defines the ValidatePurchase operation (enables mocking).
type RPValidateAPI interface {
	ValidatePurchase(
		ctx context.Context,
		body armbillingbenefits.SavingsPlanPurchaseValidateRequest,
		options *armbillingbenefits.RPClientValidatePurchaseOptions,
	) (armbillingbenefits.RPClientValidatePurchaseResponse, error)
}

// HTTPClient defines the interface for HTTP calls (enables mocking).
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client handles Azure Savings Plans.
type Client struct {
	cred           azcore.TokenCredential
	subscriptionID string
	region         string
	httpClient     HTTPClient

	// Injected in tests to avoid real Azure API calls.
	orderAliasClient SavingsPlanOrderAliasAPI
	listAllPager     SavingsPlanListAllPager
	rpValidateClient RPValidateAPI
}

// NewClient creates a new Azure Savings Plans client.
func NewClient(cred azcore.TokenCredential, subscriptionID, region string) *Client {
	return &Client{
		cred:           cred,
		subscriptionID: subscriptionID,
		region:         region,
		httpClient:     httpclient.New(),
	}
}

// NewClientWithHTTP creates a new Azure Savings Plans client with a custom HTTP client (for testing).
// When httpClient is nil, the SSRF-hardened httpclient.New() is used so the nil
// fallback also blocks IMDS connections. A non-nil httpClient is used as-is and
// bypasses that guard: metadata, NAT64/6to4 and unspecified addresses are
// dialable unless the caller's client blocks them itself.
func NewClientWithHTTP(cred azcore.TokenCredential, subscriptionID, region string, httpClient HTTPClient) *Client {
	if httpClient == nil {
		httpClient = httpclient.New()
	}
	return &Client{
		cred:           cred,
		subscriptionID: subscriptionID,
		region:         region,
		httpClient:     httpClient,
	}
}

// SetOrderAliasClient injects a mock SavingsPlanOrderAliasAPI (for testing).
func (c *Client) SetOrderAliasClient(api SavingsPlanOrderAliasAPI) {
	c.orderAliasClient = api
}

// SetListAllPager injects a mock list-all pager (for testing).
func (c *Client) SetListAllPager(pager SavingsPlanListAllPager) {
	c.listAllPager = pager
}

// SetRPValidateClient injects a mock RPValidateAPI (for testing).
func (c *Client) SetRPValidateClient(api RPValidateAPI) {
	c.rpValidateClient = api
}

// GetServiceType returns the service type for this client.
func (c *Client) GetServiceType() common.ServiceType {
	return common.ServiceSavingsPlansAll
}

// GetRegion returns the region for this client.
func (c *Client) GetRegion() string {
	return c.region
}

// GetRecommendations returns Shared-scope Compute Savings Plan purchase
// recommendations from the Cost Management Benefit Recommendations API
// (api-version 2024-08-01) at this client's subscription scope. See
// benefit_recommendations.go. Term and LookbackPeriod from params are honored;
// an unsupported value is an error.
func (c *Client) GetRecommendations(ctx context.Context, params *common.RecommendationParams) ([]common.Recommendation, error) {
	return c.getBenefitRecommendations(ctx, params)
}

// maxSavingsPlanPages caps the savings-plan inventory walk so a pager that
// never terminates fails loud instead of looping forever.
const maxSavingsPlanPages = 100

// GetExistingCommitments retrieves the Azure Savings Plans billed to this
// client's subscription, plus Single-scope plans billed to a billing account
// that name this subscription (see planOwnedBy). Shared and management-group
// plans billed to a billing account fail the call closed.
func (c *Client) GetExistingCommitments(ctx context.Context) ([]common.Commitment, error) {
	var pager SavingsPlanListAllPager

	if c.listAllPager != nil {
		pager = c.listAllPager
	} else {
		spClient, err := armbillingbenefits.NewSavingsPlanClient(nil, c.cred, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create savings plan client: %w", err)
		}
		pager = spClient.NewListAllPager(nil)
	}

	commitments := make([]common.Commitment, 0)
	skippedSingle := 0
	for pageIndex := 1; pager.More(); pageIndex++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("failed to list savings plans: context canceled during pagination: %w", err)
		}
		if pageIndex > maxSavingsPlanPages {
			return nil, fmt.Errorf("failed to list savings plans: pagination cap (%d pages) reached", maxSavingsPlanPages)
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list savings plans: %w", err)
		}
		var skipped int
		commitments, skipped, err = c.appendOwnedPlans(commitments, page.Value)
		skippedSingle += skipped
		if err != nil {
			return nil, fmt.Errorf("incomplete savings plan inventory for subscription %s, page %d: %w", c.subscriptionID, pageIndex, err)
		}
	}
	if skippedSingle > 0 {
		log.Printf("WARNING: skipped %d single-scope savings plans applied to other subscriptions", skippedSingle)
	}
	return commitments, nil
}

const subscriptionScopePrefix = "/subscriptions/"

func (c *Client) appendOwnedPlans(dst []common.Commitment, plans []*armbillingbenefits.SavingsPlanModel) ([]common.Commitment, int, error) {
	skipped := 0
	for i, sp := range plans {
		disposition, err := planOwnedBy(sp, c.subscriptionID)
		if err != nil {
			return nil, 0, fmt.Errorf("row %d: %w", i+1, err)
		}
		switch disposition {
		case planOwned:
			commitment, err := convertSavingsPlan(sp, c.subscriptionID)
			if err != nil {
				return nil, 0, fmt.Errorf("row %d: %w", i+1, err)
			}
			dst = append(dst, *commitment)
		case planSkippedSingle:
			skipped++
		}
	}
	return dst, skipped, nil
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// billingScopeID is the scope this client purchases under and owns plans by.
func (c *Client) billingScopeID() string {
	return subscriptionScopePrefix + c.subscriptionID
}

// azureSavingsPlanState maps a savings plan provisioning state to the common
// state. A state with no mapping passes through verbatim so duplicate detection
// counts it and logs it.
func azureSavingsPlanState(ps armbillingbenefits.ProvisioningState) common.CommitmentState {
	switch ps {
	case armbillingbenefits.ProvisioningStateSucceeded:
		return common.CommitmentStateActive
	case armbillingbenefits.ProvisioningStateCreating, armbillingbenefits.ProvisioningStateCreated,
		armbillingbenefits.ProvisioningStatePendingBilling, armbillingbenefits.ProvisioningStateConfirmedBilling:
		return common.CommitmentStatePaymentPending
	case armbillingbenefits.ProvisioningStateFailed:
		return common.CommitmentStateFailed
	case armbillingbenefits.ProvisioningStateCancelled:
		return common.CommitmentStateCanceled
	case armbillingbenefits.ProvisioningStateExpired:
		return common.CommitmentStateExpired
	default:
		return common.CommitmentState(ps)
	}
}

// computeResourceType is the ResourceType of a Compute savings plan on both the
// recommendation and the inventory side; recfilter.DuplicateChecker matches on it.
const computeResourceType = "Compute"

// savingsPlanResourceType maps the plan SKU (armSkuComputeSavingsPlan, matched
// case-insensitively) to the shared resource type. A missing or unknown SKU is
// an error so the duplicate guard fails closed.
func savingsPlanResourceType(sp *armbillingbenefits.SavingsPlanModel) (string, error) {
	if sp.SKU == nil || sp.SKU.Name == nil || *sp.SKU.Name == "" {
		return "", fmt.Errorf("owned savings plan %s has an unrecognized SKU (none reported); extend the SKU mapping in convertSavingsPlan", *sp.ID)
	}
	if !strings.EqualFold(*sp.SKU.Name, armSkuComputeSavingsPlan) {
		return "", fmt.Errorf("owned savings plan %s has an unrecognized SKU %q; extend the SKU mapping in convertSavingsPlan", *sp.ID, *sp.SKU.Name)
	}
	return computeResourceType, nil
}

// convertSavingsPlan maps an armbillingbenefits.SavingsPlanModel to a common.Commitment.
func convertSavingsPlan(sp *armbillingbenefits.SavingsPlanModel, subscriptionID string) (*common.Commitment, error) {
	if sp == nil || sp.ID == nil {
		return nil, fmt.Errorf("savings plan has no ID")
	}
	resourceType, err := savingsPlanResourceType(sp)
	if err != nil {
		return nil, err
	}

	commitment := common.Commitment{
		Provider:       common.ProviderAzure,
		Account:        subscriptionID,
		CommitmentID:   *sp.ID,
		CommitmentType: common.CommitmentSavingsPlan,
		Service:        common.ServiceSavingsPlansAll,
		ResourceType:   resourceType,
		Count:          1,
		State:          common.CommitmentStateActive,
	}

	if sp.Properties != nil {
		props := sp.Properties

		if props.ProvisioningState != nil {
			commitment.State = azureSavingsPlanState(*props.ProvisioningState)
		}

		if props.EffectiveDateTime != nil {
			commitment.StartDate = *props.EffectiveDateTime
		}

		if props.ExpiryDateTime != nil {
			commitment.EndDate = *props.ExpiryDateTime
		}

		if props.Commitment != nil && props.Commitment.Amount != nil {
			commitment.Cost = *props.Commitment.Amount
		}
	}

	return requireStartDate(&commitment)
}

// requireStartDate returns c, or fails closed on a non-terminal plan with no start date.
func requireStartDate(c *common.Commitment) (*common.Commitment, error) {
	if c.StartDate.IsZero() && !isTerminalSavingsPlanState(c.State) {
		return nil, fmt.Errorf("owned savings plan %s is in state %q but reports no effectiveDateTime; the duplicate guard cannot date it", c.CommitmentID, c.State)
	}
	return c, nil
}

// isTerminalSavingsPlanState reports the states recfilter rejects before it
// reads StartDate, so a zero start is harmless. Anything else, including an
// unknown or defaulted state, is treated as live.
func isTerminalSavingsPlanState(s common.CommitmentState) bool {
	switch s {
	case common.CommitmentStateFailed, common.CommitmentStateCanceled, common.CommitmentStateExpired:
		return true
	}
	return false
}

var currencyCodePattern = regexp.MustCompile(`^[A-Za-z]{3}$`)

// commitmentCurrency returns the upper-cased 3-letter currency code of the
// commitment amount. It fails closed on empty or malformed input (no trimming,
// no USD default). The check is format-only, not ISO 4217 membership; the
// pattern runs before upper-casing so Unicode case folds such as "ınr" or "ſek"
// are rejected. Azure needs the code to match the billing account's currency.
func commitmentCurrency(d *common.SavingsPlanDetails) (string, error) {
	if !currencyCodePattern.MatchString(d.Currency) {
		return "", fmt.Errorf("azure savings plan commitment currency %q is not a 3-letter currency code", d.Currency)
	}
	return strings.ToUpper(d.Currency), nil
}

// savingsPlanSKUName maps a CUDly PlanType to the ARM SKU name that the
// SavingsPlanOrderAlias and validatePurchase bodies carry in sku.name. Azure
// names the compute savings plan SKU "Compute_Savings_Plan" (the armSkuName the
// Benefit Recommendations API returns), while CUDly's PlanType is "Compute"
// (issue #387). The ARM name itself is accepted as-is. Anything else fails
// closed before any Azure call rather than sending a sku.name Azure rejects.
func savingsPlanSKUName(planType string) (string, error) {
	switch planType {
	case "Compute", armSkuComputeSavingsPlan:
		return armSkuComputeSavingsPlan, nil
	default:
		return "", fmt.Errorf("azure savings plan type %q has no known Azure savings plan SKU", planType)
	}
}

// PurchaseCommitment creates an Azure Savings Plan via the OrderAlias API.
//
// Idempotency: the SavingsPlanOrderAlias is created with an HTTP PUT on the
// alias name (Microsoft.BillingBenefits/savingsPlanOrderAliases/{name}), so a
// PUT with a previously-used name returns the existing order alias rather than
// creating a second savings plan. When opts.IdempotencyToken is set (the
// purchase-execution path), the alias name is derived deterministically from it
// via common.OrderIDFromToken so a re-drive of a stranded execution (issue
// #636) re-PUTs the same alias instead of double-buying. When the token is empty
// (the CLI path, which has no owning execution), the prior timestamp-based name
// is retained. A non-empty malformed token returns an error wrapping
// common.ErrInvalidIdempotencyToken before any PUT.
func (c *Client) PurchaseCommitment(ctx context.Context, rec common.Recommendation, opts common.PurchaseOptions) (common.PurchaseResult, error) {
	result := common.PurchaseResult{
		Recommendation: rec,
		DryRun:         false,
		Success:        false,
		Timestamp:      time.Now(),
	}

	spDetails, ok := rec.Details.(*common.SavingsPlanDetails)
	if !ok {
		result.Error = fmt.Errorf("invalid service details for Azure Savings Plans")
		return result, result.Error
	}

	body, err := c.purchaseOrderBody(rec, spDetails)
	if err != nil {
		result.Error = err
		return result, result.Error
	}
	// Derive a deterministic alias name from the idempotency token when present
	// so a re-drive PUTs the same alias (idempotent), falling back to the prior
	// timestamp-based name on the CLI path (empty token). A malformed non-empty
	// token fails here, before the body or alias client exists, so it can never
	// downgrade to a non-idempotent purchase.
	aliasName, err := common.OrderIDFromToken(opts.IdempotencyToken, fmt.Sprintf("cudly-%d", time.Now().UnixNano()))
	if err != nil {
		result.Error = err
		return result, result.Error
	}

	aliasClient, err := c.orderAlias()
	if err != nil {
		result.Error = err
		return result, result.Error
	}

	poller, err := aliasClient.BeginCreate(ctx, aliasName, body, nil)
	if err != nil {
		result.Error = fmt.Errorf("failed to begin savings plan purchase: %w", err)
		return result, result.Error
	}

	resp, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		result.Error = fmt.Errorf("savings plan purchase failed: %w", err)
		return result, result.Error
	}

	if resp.ID != nil {
		result.CommitmentID = *resp.ID
		result.Success = true
	} else {
		result.Error = fmt.Errorf("purchase response missing ID")
		return result, result.Error
	}

	return result, nil
}

// purchaseOrderBody builds the order-alias request for a savings plan
// purchase. The term, currency and SKU name are validated here, so an invalid
// recommendation fails before any Azure client exists.
func (c *Client) purchaseOrderBody(rec common.Recommendation, spDetails *common.SavingsPlanDetails) (armbillingbenefits.SavingsPlanOrderAliasModel, error) {
	term, err := toAzureTerm(rec.Term)
	if err != nil {
		return armbillingbenefits.SavingsPlanOrderAliasModel{}, err
	}
	currency, err := commitmentCurrency(spDetails)
	if err != nil {
		return armbillingbenefits.SavingsPlanOrderAliasModel{}, err
	}
	skuName, err := savingsPlanSKUName(spDetails.PlanType)
	if err != nil {
		return armbillingbenefits.SavingsPlanOrderAliasModel{}, err
	}
	grain := armbillingbenefits.CommitmentGrainHourly
	billingScopeID := c.billingScopeID()
	appliedScope := armbillingbenefits.AppliedScopeTypeShared
	hourlyAmount := spDetails.HourlyCommitment
	displayName := fmt.Sprintf("cudly-%s-%s", spDetails.PlanType, rec.Term)
	return armbillingbenefits.SavingsPlanOrderAliasModel{
		SKU: &armbillingbenefits.SKU{Name: toPtr(skuName)},
		Properties: &armbillingbenefits.SavingsPlanOrderAliasProperties{
			DisplayName:      &displayName,
			BillingScopeID:   &billingScopeID,
			Term:             &term,
			AppliedScopeType: &appliedScope,
			Commitment: &armbillingbenefits.Commitment{
				Amount:       &hourlyAmount,
				CurrencyCode: &currency,
				Grain:        &grain,
			},
		},
	}, nil
}

// orderAlias returns the injected order-alias client, or a real one built
// from the client's credential.
func (c *Client) orderAlias() (SavingsPlanOrderAliasAPI, error) {
	if c.orderAliasClient != nil {
		return c.orderAliasClient, nil
	}
	client, err := armbillingbenefits.NewSavingsPlanOrderAliasClient(c.cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create order alias client: %w", err)
	}
	return &realOrderAliasClient{client: client}, nil
}

// ValidateOffering checks whether the Savings Plan configuration is valid.
func (c *Client) ValidateOffering(ctx context.Context, rec common.Recommendation) error {
	validateBody, err := c.buildValidateBody(rec)
	if err != nil {
		return err
	}

	rpClient, err := c.getRPValidateClient()
	if err != nil {
		return err
	}

	resp, err := rpClient.ValidatePurchase(ctx, validateBody, nil)
	if err != nil {
		return fmt.Errorf("failed to validate savings plan: %w", err)
	}

	return checkValidateResponse(resp)
}

// buildValidateBody constructs the SavingsPlanPurchaseValidateRequest from a recommendation.
func (c *Client) buildValidateBody(rec common.Recommendation) (armbillingbenefits.SavingsPlanPurchaseValidateRequest, error) {
	spDetails, ok := rec.Details.(*common.SavingsPlanDetails)
	if !ok {
		return armbillingbenefits.SavingsPlanPurchaseValidateRequest{}, fmt.Errorf("invalid service details for Azure Savings Plans")
	}

	term, err := toAzureTerm(rec.Term)
	if err != nil {
		return armbillingbenefits.SavingsPlanPurchaseValidateRequest{}, err
	}

	grain := armbillingbenefits.CommitmentGrainHourly
	billingScopeID := c.billingScopeID()
	appliedScope := armbillingbenefits.AppliedScopeTypeShared
	hourlyAmount := spDetails.HourlyCommitment
	currency, err := commitmentCurrency(spDetails)
	if err != nil {
		return armbillingbenefits.SavingsPlanPurchaseValidateRequest{}, err
	}
	skuName, err := savingsPlanSKUName(spDetails.PlanType)
	if err != nil {
		return armbillingbenefits.SavingsPlanPurchaseValidateRequest{}, err
	}
	displayName := "cudly-validate"

	return armbillingbenefits.SavingsPlanPurchaseValidateRequest{
		Benefits: []*armbillingbenefits.SavingsPlanOrderAliasModel{
			{
				SKU: &armbillingbenefits.SKU{Name: toPtr(skuName)},
				Properties: &armbillingbenefits.SavingsPlanOrderAliasProperties{
					DisplayName:      &displayName,
					BillingScopeID:   &billingScopeID,
					Term:             &term,
					AppliedScopeType: &appliedScope,
					Commitment: &armbillingbenefits.Commitment{
						Amount:       &hourlyAmount,
						CurrencyCode: &currency,
						Grain:        &grain,
					},
				},
			},
		},
	}, nil
}

// getRPValidateClient returns the injected mock or creates a real RPClient.
func (c *Client) getRPValidateClient() (RPValidateAPI, error) {
	if c.rpValidateClient != nil {
		return c.rpValidateClient, nil
	}
	client, err := armbillingbenefits.NewRPClient(c.cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create RP client: %w", err)
	}
	return client, nil
}

// checkValidateResponse returns an error if any benefit in the response is invalid.
func checkValidateResponse(resp armbillingbenefits.RPClientValidatePurchaseResponse) error {
	for _, b := range resp.Benefits {
		if b == nil || b.Valid == nil || *b.Valid {
			continue
		}
		reason := ""
		if b.Reason != nil {
			reason = *b.Reason
		}
		return fmt.Errorf("savings plan offering invalid: %s", reason)
	}
	return nil
}

// AzureRetailPrice is the response envelope for the Azure Retail Prices API.
type AzureRetailPrice = pricing.Page[pricing.RetailPriceItem]

// GetOfferingDetails retrieves pricing details for an Azure Savings Plan offering.
func (c *Client) GetOfferingDetails(ctx context.Context, rec common.Recommendation) (*common.OfferingDetails, error) {
	spDetails, ok := rec.Details.(*common.SavingsPlanDetails)
	if !ok {
		return nil, fmt.Errorf("invalid service details for Azure Savings Plans")
	}

	currency, err := commitmentCurrency(spDetails)
	if err != nil {
		return nil, err
	}

	// Calculate term hours.
	azTerm, err := toAzureTerm(rec.Term)
	if err != nil {
		return nil, err
	}

	var hoursInTerm float64
	var termStr string
	switch azTerm {
	case armbillingbenefits.TermP1Y:
		hoursInTerm, termStr = 8760.0, "1yr"
	case armbillingbenefits.TermP3Y:
		hoursInTerm, termStr = 26280.0, "3yr"
	case armbillingbenefits.TermP5Y:
		hoursInTerm, termStr = 43800.0, "5yr"
	default:
		return nil, fmt.Errorf("unsupported savings plan term: %s", rec.Term)
	}

	totalCost := spDetails.HourlyCommitment * hoursInTerm

	var upfrontCost, recurringCost float64
	switch rec.PaymentOption {
	case "All Upfront", "all-upfront":
		upfrontCost = totalCost
		recurringCost = 0
	case "Partial Upfront", "partial-upfront":
		upfrontCost = totalCost * 0.5
		recurringCost = (totalCost * 0.5) / hoursInTerm
	case "No Upfront", "no-upfront":
		upfrontCost = 0
		recurringCost = totalCost / hoursInTerm
	default:
		return nil, fmt.Errorf("unsupported payment option: %s", rec.PaymentOption)
	}

	return &common.OfferingDetails{
		ResourceType:        spDetails.PlanType,
		Term:                termStr,
		PaymentOption:       rec.PaymentOption,
		UpfrontCost:         upfrontCost,
		RecurringCost:       recurringCost,
		TotalCost:           totalCost,
		EffectiveHourlyRate: spDetails.HourlyCommitment,
		Currency:            currency,
	}, nil
}

// fetchOnDemandRate queries the Azure Retail Prices API for the on-demand hourly
// rate for the given plan type. The $filter is built with url.Values so that
// spaces and quotes are properly percent-encoded (issue #1021 H3). Pagination is
// handled by pricing.FetchAll (seen-URL guard, max-pages cap, per-page timeout).
// Returns an error when no price record is found rather than (0, nil), which would
// conflate "zero rate" with "not found" (feedback_nullable_not_zero).
func (c *Client) fetchOnDemandRate(ctx context.Context, planType string) (float64, error) {
	qPlan, err := pricing.ODataString(planType)
	if err != nil {
		return 0, err
	}
	filter := fmt.Sprintf(
		"serviceFamily eq 'Compute' and priceType eq 'Consumption' and armSkuName eq %s",
		qPlan,
	)
	params := url.Values{}
	params.Add("$filter", filter)
	params.Add("api-version", "2023-01-01-preview")
	initialURL := "https://prices.azure.com/api/retail/prices?" + params.Encode()

	items, err := pricing.FetchAll[pricing.RetailPriceItem](ctx, c.httpClient, initialURL, pricing.DefaultPageTimeout, pricing.DefaultMaxPages)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch on-demand rate for plan type %s: %w", planType, err)
	}

	for i := range items {
		if items[i].RetailPrice > 0 {
			return items[i].RetailPrice, nil
		}
	}

	return 0, fmt.Errorf("no on-demand pricing found for savings plan type %s", planType)
}

// GetValidResourceTypes returns the Azure Savings Plan types.
func (c *Client) GetValidResourceTypes(_ context.Context) ([]string, error) {
	return []string{
		"Compute",
		"MachineLearning",
	}, nil
}

// toAzureTerm converts a CUDly term string to an armbillingbenefits.Term.
func toAzureTerm(term string) (armbillingbenefits.Term, error) {
	switch term {
	case "1yr", "1", "P1Y", "":
		return armbillingbenefits.TermP1Y, nil
	case "3yr", "3", "P3Y":
		return armbillingbenefits.TermP3Y, nil
	case "5yr", "5", "P5Y":
		return armbillingbenefits.TermP5Y, nil
	default:
		return "", fmt.Errorf("unsupported savings plan term: %s", term)
	}
}

// toPtr returns a pointer to v.
func toPtr[T any](v T) *T {
	return &v
}

// realOrderAliasClient wraps armbillingbenefits.SavingsPlanOrderAliasClient to
// satisfy SavingsPlanOrderAliasAPI.
type realOrderAliasClient struct {
	client *armbillingbenefits.SavingsPlanOrderAliasClient
}

func (r *realOrderAliasClient) BeginCreate(
	ctx context.Context,
	savingsPlanOrderAliasName string,
	body armbillingbenefits.SavingsPlanOrderAliasModel,
	options *armbillingbenefits.SavingsPlanOrderAliasClientBeginCreateOptions,
) (SavingsPlanOrderAliasPoller, error) {
	poller, err := r.client.BeginCreate(ctx, savingsPlanOrderAliasName, body, options)
	if err != nil {
		return nil, err
	}
	return &realPoller{poller: poller}, nil
}

// realPoller wraps the SDK LRO poller to satisfy SavingsPlanOrderAliasPoller.
type realPoller struct {
	poller *runtime.Poller[armbillingbenefits.SavingsPlanOrderAliasClientCreateResponse]
}

func (r *realPoller) PollUntilDone(ctx context.Context, _ *PollOptions) (armbillingbenefits.SavingsPlanOrderAliasClientCreateResponse, error) {
	return r.poller.PollUntilDone(ctx, nil)
}

// SavingsPlanOrderAliasProperties mirrors armbillingbenefits for use in BeginCreate body.
// Re-exporting via an alias for clarity.
type SavingsPlanOrderAliasProperties = armbillingbenefits.SavingsPlanOrderAliasProperties
