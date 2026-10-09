// Package compute provides Azure VM Reserved Instances client
package compute

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/httpclient"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/pricing"
	azrecs "github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/recommendations"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/internal/reservations"
)

// RecommendationsPager defines the interface for paging through recommendations.
type RecommendationsPager interface {
	More() bool
	NextPage(ctx context.Context) (armconsumption.ReservationRecommendationsClientListResponse, error)
}

// InventoryFactories exposes the reservation inventory factory callbacks.
type InventoryFactories = reservations.InventoryFactories

// AppliedReservationsLister is the applied-order client used by InventoryFactories.
type AppliedReservationsLister = reservations.AppliedReservationsLister

// OrderReservationsPager is the child-reservation pager used by InventoryFactories.
type OrderReservationsPager = reservations.OrderReservationsPager

// ResourceSKUsPager defines the interface for paging through resource SKUs.
type ResourceSKUsPager interface {
	More() bool
	NextPage(ctx context.Context) (armcompute.ResourceSKUsClientListResponse, error)
}

// HTTPClient defines the interface for making HTTP requests.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// vmSKUEntry holds the SKU-catalog-derived fields the converter
// wants for each VM SKU. Sourced from
// armcompute.ResourceSKU.Capabilities (a name/value-pair list):
//   - vCPUs: Capabilities[Name=="vCPUs"].Value, parsed as int.
//   - memoryGB: Capabilities[Name=="MemoryGB"].Value, parsed as float64.
//
// Either field stays at the zero value when the capability is missing
// or unparseable; common.ComputeDetails treats 0 as "unknown" (the
// JSON tags on VCPU/MemoryGB are omitempty).

// maxRecsPages caps Consumption API recommendation pagination to avoid
// burning a Lambda deadline on a stalled or unexpectedly deep result set.
const maxRecsPages = 10

// maxReservationsPages caps reservation-detail pagination.
// Large orgs may have hundreds of reservations spread over many pages.
const maxReservationsPages = 50

// maxSKUPages caps Azure ResourceSKUs pagination.
// The SKU catalog for a subscription can run to many pages.
const maxSKUPages = 20

type vmSKUEntry struct {
	vCPUs    int
	memoryGB float64
}

// Client handles Azure VM Reserved Instances.
type Client struct {
	cred           azcore.TokenCredential
	subscriptionID string
	region         string
	httpClient     HTTPClient

	// For testing - these can be set to mock implementations
	recommendationsPager RecommendationsPager
	resourceSKUsPager    ResourceSKUsPager

	// Optional injected factories for the reservation-inventory path used by
	// GetExistingCommitments. When nil (the production default) the method
	// builds real armreservations SDK clients via
	// reservations.DefaultInventoryFactories. Tests inject stubs via
	// SetInventoryFactories to run hermetically without Azure credentials.
	inventoryFactories *InventoryFactories

	// Microsoft.Capacity provider registration check (cached per client lifetime)
	capacityProviderOnce sync.Once
	capacityProviderErr  error

	// Lazy SKU catalog cache. armcompute.ResourceSKUsClient.NewListPager
	// returns every SKU available to the subscription with its
	// Capabilities (vCPUs, MemoryGB). Fetched ONCE per client lifetime;
	// subsequent converter calls in the same GetRecommendations run hit
	// the in-memory map. A failed fetch leaves skuCacheMap nil and
	// converters fall back to VCPU=0/MemoryGB=0 with a WARN log — the
	// conversion itself does NOT fail (graceful-degradation contract,
	// matches cache/cosmosdb/database from PR #81).
	skuCacheOnce sync.Once
	skuCacheMap  map[string]vmSKUEntry

	// Optional injected pager for ListExchangeableReservations. When nil
	// (the production default) the method creates a real
	// armreservations.ReservationClient. Tests inject a stub to run
	// hermetically without Azure credentials.
	exchangeablePager ExchangeableReservationPager

	// Optional injected LRO callers for CalculateExchange and
	// ExecuteExchange. When nil (the production default) the methods
	// construct real armreservations SDK clients. Tests inject stubs via
	// SetCalculateExchangeCaller and SetDoExchangeCaller to run
	// hermetically and make the LRO synchronous.
	calculateExchangeCaller CalculateExchangeCallerFunc
	doExchangeCaller        DoExchangeCallerFunc
}

// NewClient creates a new Azure Compute client.
func NewClient(cred azcore.TokenCredential, subscriptionID, region string) *Client {
	return &Client{
		cred:           cred,
		subscriptionID: subscriptionID,
		region:         region,
		httpClient:     httpclient.New(),
	}
}

// NewClientWithHTTP creates a new Azure Compute client with a custom HTTP client (for testing).
func NewClientWithHTTP(cred azcore.TokenCredential, subscriptionID, region string, httpClient HTTPClient) *Client {
	return &Client{
		cred:           cred,
		subscriptionID: subscriptionID,
		region:         region,
		httpClient:     httpClient,
	}
}

// SetRecommendationsPager sets a mock pager for recommendations (for testing).
func (c *Client) SetRecommendationsPager(pager RecommendationsPager) {
	c.recommendationsPager = pager
}

// SetInventoryFactories injects reservation-inventory factories (for testing).
func (c *Client) SetInventoryFactories(f *InventoryFactories) {
	c.inventoryFactories = f
}

// SetResourceSKUsPager sets a mock pager for resource SKUs (for testing).
func (c *Client) SetResourceSKUsPager(pager ResourceSKUsPager) {
	c.resourceSKUsPager = pager
}

// GetServiceType returns the service type.
func (c *Client) GetServiceType() common.ServiceType {
	return common.ServiceCompute
}

// GetRegion returns the region.
func (c *Client) GetRegion() string {
	return c.region
}

// AzureRetailPriceItem is a type alias for pricing.RetailPriceItem kept for
// backward compatibility within this package. New code should use
// pricing.RetailPriceItem directly.
type AzureRetailPriceItem = pricing.RetailPriceItem

// AzureRetailPrice is the response envelope for the Azure Retail Prices API.
// It wraps pricing.Page[pricing.RetailPriceItem] under a package-local name
// so existing call sites do not need to be updated.
type AzureRetailPrice = pricing.Page[pricing.RetailPriceItem]

// GetRecommendations gets VM RI recommendations from Azure Consumption API.
func (c *Client) GetRecommendations(ctx context.Context, _ *common.RecommendationParams) ([]common.Recommendation, error) {
	recommendations := make([]common.Recommendation, 0)
	pricer := azrecs.NewPricer("compute", func(ctx context.Context, sku, region string, termYears int) (azrecs.ReservationPrice, error) {
		p, err := c.getVMPricing(ctx, sku, region, termYears)
		if err != nil {
			return azrecs.ReservationPrice{}, err
		}
		return azrecs.ReservationPrice{Total: p.ReservationPrice, Currency: p.Currency}, nil
	})

	// Use injected pager if available (for testing)
	var pager RecommendationsPager
	if c.recommendationsPager != nil {
		pager = c.recommendationsPager
	} else {
		client, err := armconsumption.NewReservationRecommendationsClient(c.cred, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create consumption client: %w", err)
		}

		// NewListPager's first argument is the billing scope (the subscription
		// path), NOT the filter. Passing the filter here produced a malformed
		// URL where the ODATA filter got spliced into the URL path between
		// management.azure.com and providers/Microsoft.Consumption/... and
		// every request returned an error. The filter belongs in the
		// ClientListOptions.Filter field.
		scope := fmt.Sprintf("/subscriptions/%s", c.subscriptionID)
		filter := azrecs.ConsumptionFilter("VirtualMachines")
		pager = client.NewListPager(scope, &armconsumption.ReservationRecommendationsClientListOptions{Filter: &filter})
	}

	for pageIdx := 0; pager.More(); pageIdx++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("context canceled during pagination: %w", err)
		}
		if pageIdx >= maxRecsPages {
			return nil, fmt.Errorf("compute: GetRecommendations pagination cap (%d pages) reached", maxRecsPages)
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get VM recommendations: %w", err)
		}

		recommendations, err = c.appendPage(ctx, pricer, recommendations, page.Value)
		if err != nil {
			return recommendations, err
		}
	}

	return recommendations, nil
}

// appendPage converts one page of recommendations and appends their priced
// variants. It is split out of GetRecommendations to keep that function under
// the cyclomatic limit.
func (c *Client) appendPage(ctx context.Context, pricer *azrecs.Pricer, recs []common.Recommendation, page []armconsumption.ReservationRecommendationClassification) ([]common.Recommendation, error) {
	for _, rec := range page {
		converted := c.convertAzureVMRecommendation(ctx, rec)
		if converted == nil {
			continue
		}
		var err error
		recs, err = azrecs.AppendConsumptionVariants(ctx, "compute", recs, *converted, pricer)
		if err != nil {
			return recs, err
		}
	}
	return recs, nil
}

// GetExistingCommitments retrieves existing VM Reserved Instances from the
// armreservations SDK: the applied-reservation list yields the reservation
// orders whose benefits apply to this subscription, and each order's child
// reservations carry the authoritative ID, region, SKU, state, quantity and
// purchase/expiry dates (issue #190).
//
// Any failure is returned as an error rather than as an empty inventory: an
// empty list is indistinguishable from "no commitments" and would be unsafe
// for the duplicate-purchase guard (matches the error-returning behavior of
// the cache, cosmosdb, database and search clients).
func (c *Client) GetExistingCommitments(ctx context.Context) ([]common.Commitment, error) {
	factories := reservations.DefaultInventoryFactories(c.cred)
	if c.inventoryFactories != nil {
		factories = *c.inventoryFactories
	}

	responses, err := reservations.ListAppliedReservations(ctx, c.subscriptionID, factories, maxReservationsPages)
	if err != nil {
		return nil, fmt.Errorf("compute: list applied reservations: %w", err)
	}

	now := time.Now()
	commitments := make([]common.Commitment, 0, len(responses))
	for _, r := range responses {
		commitment, err := c.commitmentFromVMReservation(r, now)
		if err != nil {
			return nil, err
		}
		if commitment != nil {
			commitments = append(commitments, *commitment)
		}
	}
	return commitments, nil
}

// commitmentFromVMReservation validates and converts one reservation. It
// returns a nil commitment for non-VM types.
func (c *Client) commitmentFromVMReservation(r *armreservations.ReservationResponse, now time.Time) (*common.Commitment, error) {
	// filterAppliedReservations passes nil and property-less rows through;
	// dropping them would hide a possibly just-bought reservation.
	if r == nil || r.Properties == nil || r.Properties.ReservedResourceType == nil {
		return nil, fmt.Errorf("compute: cannot classify reservation %s in subscription %q: missing properties or reservedResourceType",
			reservationLabel(r), c.subscriptionID)
	}
	commitment := reservations.CommitmentFromReservation(r, c.subscriptionID, common.ServiceCompute, armreservations.ReservedResourceTypeVirtualMachines, now)
	if commitment == nil {
		return nil, nil
	}
	if err := validateVMReservationInventory(r.Properties, commitment.CommitmentID, c.subscriptionID, now); err != nil {
		return nil, err
	}
	if !isTerminalCommitmentState(commitment.State) {
		if err := validateLiveVMReservationFields(r, commitment.CommitmentID, c.subscriptionID); err != nil {
			return nil, err
		}
	}
	return commitment, nil
}

func reservationLabel(r *armreservations.ReservationResponse) string {
	if r == nil || r.ID == nil {
		return "(no id)"
	}
	return fmt.Sprintf("%q", *r.ID)
}

// isTerminalCommitmentState reports lifecycle states that no longer count as
// coverage. It works on the converted state so a Succeeded reservation with a
// past expiry is terminal too.
func isTerminalCommitmentState(s common.CommitmentState) bool {
	switch s {
	case common.CommitmentStateCanceled, common.CommitmentStateExpired,
		common.CommitmentStateFailed, common.CommitmentStateRetired:
		return true
	}
	return false
}

// validateLiveVMReservationFields rejects a live VM reservation missing a field
// the duplicate guard keys or sizes on: a blank SKU or location never matches a
// recommendation, a missing quantity counts as zero capacity, and a missing
// purchase date makes a fresh purchase look old. Erroring (not skipping) is
// deliberate: the row may be the reservation bought an hour ago.
func validateLiveVMReservationFields(r *armreservations.ReservationResponse, reservationID, subscriptionID string) error {
	var missing []string
	if r.SKU == nil || r.SKU.Name == nil || strings.TrimSpace(*r.SKU.Name) == "" {
		missing = append(missing, "sku.name")
	}
	if r.Location == nil || strings.TrimSpace(*r.Location) == "" {
		missing = append(missing, "location")
	}
	if r.Properties.Quantity == nil || *r.Properties.Quantity <= 0 {
		missing = append(missing, "quantity")
	}
	if r.Properties.PurchaseDate == nil {
		missing = append(missing, "purchaseDate")
	}
	if len(missing) > 0 {
		return fmt.Errorf("compute: incomplete live VM reservation %q in subscription %q: missing or invalid %s",
			reservationID, subscriptionID, strings.Join(missing, ", "))
	}
	return nil
}

func validateVMReservationInventory(props *armreservations.Properties, reservationID, subscriptionID string, now time.Time) error {
	if props.AppliedScopeType == nil {
		return fmt.Errorf("compute: cannot attribute VM reservation %q to subscription %q: missing applied scope type", reservationID, subscriptionID)
	}
	if *props.AppliedScopeType != armreservations.AppliedScopeTypeSingle {
		return fmt.Errorf("compute: cannot attribute VM reservation %q to subscription %q: applied scope type %q is not Single", reservationID, subscriptionID, *props.AppliedScopeType)
	}
	if props.ProvisioningState != nil && *props.ProvisioningState == armreservations.ProvisioningStateSucceeded &&
		props.ExpiryDate != nil && props.ExpiryDate.UTC().Format(time.DateOnly) == now.UTC().Format(time.DateOnly) {
		return fmt.Errorf("compute: cannot determine lifecycle for VM reservation %q in subscription %q: reservations API date-only expiryDate %s falls on the current UTC date",
			reservationID, subscriptionID, props.ExpiryDate.UTC().Format(time.DateOnly))
	}
	return nil
}

// providerRegistrationState is the JSON shape returned by the ARM providers API.
type providerRegistrationState struct {
	RegistrationState string `json:"registrationState"`
}

// ensureCapacityProviderRegistered checks that the Microsoft.Capacity resource provider
// is registered in the subscription. The check is performed once per client lifetime.
// An error is logged but does not block the purchase attempt.
func (c *Client) ensureCapacityProviderRegistered(ctx context.Context) {
	c.capacityProviderOnce.Do(func() {
		c.capacityProviderErr = c.checkAndRegisterCapacityProvider(ctx)
		if c.capacityProviderErr != nil {
			log.Printf("WARNING: Microsoft.Capacity provider registration check failed: %v", c.capacityProviderErr)
		}
	})
}

// checkAndRegisterCapacityProvider performs the actual provider registration check.
func (c *Client) checkAndRegisterCapacityProvider(ctx context.Context) error {
	if c.cred == nil {
		return nil // skip in test environments without credentials
	}

	token, err := c.cred.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{"https://management.azure.com/.default"},
	})
	if err != nil {
		return fmt.Errorf("get token: %w", err)
	}

	const apiVersion = "2021-04-01"
	state, err := c.fetchCapacityProviderState(ctx, token.Token, apiVersion)
	if err != nil {
		return err
	}
	if state.RegistrationState == "Registered" {
		return nil
	}
	return c.triggerCapacityProviderRegistration(ctx, token.Token, apiVersion, state.RegistrationState)
}

// fetchCapacityProviderState queries the ARM providers API and returns the
// current registration state of Microsoft.Capacity.
func (c *Client) fetchCapacityProviderState(ctx context.Context, bearerToken, apiVersion string) (providerRegistrationState, error) {
	checkURL := fmt.Sprintf(
		"https://management.azure.com/subscriptions/%s/providers/Microsoft.Capacity?api-version=%s",
		c.subscriptionID, apiVersion,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, http.NoBody)
	if err != nil {
		return providerRegistrationState{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearerToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return providerRegistrationState{}, fmt.Errorf("check provider: %w", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close() // #nosec G104 -- body fully drained by io.ReadAll before Close; transport close error does not affect correctness
	if err != nil {
		return providerRegistrationState{}, fmt.Errorf("read provider check response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Non-2xx from the provider check (e.g. 403 permissions, 429 throttle).
		// Log and return so the purchase attempt can still proceed; a failed
		// registration check is non-fatal.
		return providerRegistrationState{}, fmt.Errorf("check Microsoft.Capacity provider status returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var state providerRegistrationState
	if err := json.Unmarshal(body, &state); err != nil {
		return providerRegistrationState{}, fmt.Errorf("decode provider state: %w", err)
	}
	return state, nil
}

// triggerCapacityProviderRegistration POSTs to the ARM register endpoint to
// initiate Microsoft.Capacity provider registration.
func (c *Client) triggerCapacityProviderRegistration(ctx context.Context, bearerToken, apiVersion, currentState string) error {
	registerURL := fmt.Sprintf(
		"https://management.azure.com/subscriptions/%s/providers/Microsoft.Capacity/register?api-version=%s",
		c.subscriptionID, apiVersion,
	)
	regReq, err := http.NewRequestWithContext(ctx, http.MethodPost, registerURL, http.NoBody)
	if err != nil {
		return fmt.Errorf("build register request: %w", err)
	}
	regReq.Header.Set("Authorization", "Bearer "+bearerToken)

	regResp, err := c.httpClient.Do(regReq)
	if err != nil {
		return fmt.Errorf("register provider: %w", err)
	}
	regBody, err := io.ReadAll(regResp.Body)
	regResp.Body.Close() // #nosec G104 -- body fully drained by io.ReadAll before Close; transport close error does not affect correctness
	if err != nil {
		return fmt.Errorf("read register-provider response: %w", err)
	}
	if regResp.StatusCode < 200 || regResp.StatusCode >= 300 {
		return fmt.Errorf("register Microsoft.Capacity provider returned HTTP %d: %s", regResp.StatusCode, string(regBody))
	}

	log.Printf("Triggered Microsoft.Capacity provider registration (was: %q)", currentState)
	return nil
}

// buildReservationBody builds the JSON body for a reservation purchase request.
// The same body is sent to both calculatePrice and purchase endpoints (issue #677).
// billingPlan is resolved by the caller (PurchaseCommitment) via
// reservations.BillingPlanForPaymentOption before any side-effecting call is
// made, so it is threaded in here rather than re-derived from rec.PaymentOption.
// The purchase-automation and cudly-idempotency-token tags are attached via
// reservations.ApplyPurchaseTags so the resulting reservation is identifiable
// in the portal AND a re-driven purchase can find it via tag lookup before
// buying a duplicate (issue #721).
func (c *Client) buildReservationBody(rec common.Recommendation, billingPlan armreservations.ReservationBillingPlan, source, idempotencyToken string) ([]byte, error) {
	termYears, err := reservations.ParseTermYears(rec.Term)
	if err != nil {
		return nil, err
	}
	requestBody := map[string]interface{}{
		"sku":      map[string]string{"name": rec.ResourceType},
		"location": c.region,
		"properties": map[string]interface{}{
			"reservedResourceType": string(armreservations.ReservedResourceTypeVirtualMachines),
			"billingScopeId":       fmt.Sprintf("/subscriptions/%s", c.subscriptionID),
			"billingPlan":          string(billingPlan),
			"term":                 fmt.Sprintf("P%dY", termYears),
			"quantity":             rec.Count,
			"displayName": reservations.BuildDisplayName(reservations.DisplayNameFields{
				Service:      "vm",
				Region:       c.region,
				ResourceType: rec.ResourceType,
				Count:        rec.Count,
				Term:         rec.Term,
				Payment:      rec.PaymentOption,
				Now:          time.Now(),
			}),
			"appliedScopeType": "Shared",
			"renew":            false,
		},
	}
	reservations.ApplyPurchaseTags(requestBody, source, idempotencyToken)
	return json.Marshal(requestBody)
}

// PurchaseCommitment purchases a VM Reserved Instance using the two-step
// calculatePrice->purchase flow required by Azure's Reservations API (issue #677).
//
// Azure shifted newer SKU families (Burstable v2 and others) to require a
// calculatePrice call before purchase. The previous direct-PUT pattern returns
// 400 "Session timed out" for these families. The two-step flow:
//  1. POST calculatePrice -- Azure mints a session-bound reservationOrderId.
//  2. POST reservationOrders/{id}/purchase -- commits the order.
//
// Idempotency: Azure mints the order ID in step 1, so client-supplied IDs are
// no longer used. Re-drives are idempotent via tag-based deduplication
// performed inside reservations.DoIdempotentPurchaseTwoStep: every purchase
// body carries the cudly-idempotency-token tag derived from opts.IdempotencyToken,
// and a re-drive lists existing reservation orders and short-circuits when an
// order already carries the same tag (issue #721).
func (c *Client) PurchaseCommitment(ctx context.Context, rec common.Recommendation, opts common.PurchaseOptions) (common.PurchaseResult, error) {
	result := common.PurchaseResult{
		Recommendation: rec,
		DryRun:         false,
		Success:        false,
		Timestamp:      time.Now(),
	}

	// Source is required so the resulting reservation is attributable to CUDly
	// in the portal via the purchase-automation tag. The dedupe key for
	// idempotent re-drives is now opts.IdempotencyToken (issue #721, applied
	// in reservations.DoIdempotentPurchaseTwoStep); source remains mandatory
	// for attribution.
	if opts.Source == "" {
		result.Error = fmt.Errorf("purchase source is required for Azure reservation purchases")
		return result, result.Error
	}
	if rec.Count <= 0 {
		result.Error = fmt.Errorf("quantity must be greater than zero, got %d", rec.Count)
		return result, result.Error
	}

	// Validate the payment option and build the request body BEFORE any
	// side-effecting call. Microsoft.Capacity provider registration below is
	// a real ARM operation (a GET, and a POST to register when unregistered);
	// every fallible local parse -- payment option AND term (buildReservationBody
	// calls reservations.ParseTermYears) -- must be rejected here first so a
	// doomed purchase never triggers it.
	billingPlan, err := reservations.BillingPlanForPaymentOption(rec.PaymentOption)
	if err != nil {
		result.Error = err
		return result, result.Error
	}

	bodyBytes, err := c.buildReservationBody(rec, billingPlan, opts.Source, opts.IdempotencyToken)
	if err != nil {
		result.Error = fmt.Errorf("failed to marshal request: %w", err)
		return result, result.Error
	}

	// Ensure Microsoft.Capacity provider is registered (cached after first call).
	c.ensureCapacityProviderRegistered(ctx)

	token, err := c.cred.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{"https://management.azure.com/.default"},
	})
	if err != nil {
		result.Error = fmt.Errorf("failed to get access token: %w", err)
		return result, result.Error
	}

	reservationOrderID, existing, err := reservations.DoIdempotentPurchaseTwoStep(ctx, c.httpClient, reservations.CalculatePriceURL(), bodyBytes, token.Token, opts.IdempotencyToken)
	if err != nil {
		result.Error = err
		return result, result.Error
	}

	result.Success = true
	result.ExistingCommitment = existing
	result.CommitmentID = reservationOrderID
	result.Cost = reservations.UpfrontCostForBillingPlan(billingPlan)
	return result, nil
}

// ValidateOffering validates that a VM SKU exists.
func (c *Client) ValidateOffering(ctx context.Context, rec common.Recommendation) error {
	validSKUs, err := c.GetValidResourceTypes(ctx)
	if err != nil {
		return fmt.Errorf("failed to get valid SKUs: %w", err)
	}

	resourceType := strings.TrimSpace(rec.ResourceType)
	for _, sku := range validSKUs {
		if strings.EqualFold(sku, resourceType) {
			return nil
		}
	}

	return fmt.Errorf("invalid Azure VM SKU: %s", rec.ResourceType)
}

// GetOfferingDetails retrieves VM RI offering details from Azure Retail Prices API.
func (c *Client) GetOfferingDetails(ctx context.Context, rec common.Recommendation) (*common.OfferingDetails, error) {
	termYears, err := reservations.ParseTermYears(rec.Term)
	if err != nil {
		return nil, fmt.Errorf("invalid term: %w", err)
	}

	vmPricing, err := c.getVMPricing(ctx, rec.ResourceType, c.region, termYears)
	if err != nil {
		return nil, fmt.Errorf("failed to get pricing: %w", err)
	}

	var upfrontCost, recurringCost float64
	totalCost := vmPricing.ReservationPrice

	switch rec.PaymentOption {
	case "all-upfront", "upfront":
		upfrontCost = totalCost
		recurringCost = 0
	case "monthly", "no-upfront":
		upfrontCost = 0
		recurringCost = totalCost / (float64(termYears) * 12)
	default:
		// Fail loud on an unrecognized payment option rather than silently
		// billing it as all-upfront (owner policy: no silent fallbacks on
		// money-affecting fields).
		return nil, fmt.Errorf("unsupported payment option for Azure VM offering details: %q", rec.PaymentOption)
	}

	return &common.OfferingDetails{
		OfferingID:          fmt.Sprintf("azure-vm-%s-%s-%s", rec.ResourceType, c.region, rec.Term),
		ResourceType:        rec.ResourceType,
		Term:                rec.Term,
		PaymentOption:       rec.PaymentOption,
		UpfrontCost:         upfrontCost,
		RecurringCost:       recurringCost,
		TotalCost:           totalCost,
		EffectiveHourlyRate: vmPricing.HourlyRate,
		Currency:            vmPricing.Currency,
	}, nil
}

// GetValidResourceTypes returns valid VM sizes from Azure Compute API.
func (c *Client) GetValidResourceTypes(ctx context.Context) ([]string, error) {
	pager, err := c.createResourceSKUsPager()
	if err != nil {
		return nil, err
	}

	vmSizes, err := c.collectVMSizesFromSKUs(ctx, pager)
	if err != nil {
		return nil, err
	}

	if len(vmSizes) == 0 {
		return nil, fmt.Errorf("no VM sizes found for region %s", c.region)
	}

	return vmSizes, nil
}

// createResourceSKUsPager creates a pager for listing resource SKUs.
func (c *Client) createResourceSKUsPager() (ResourceSKUsPager, error) {
	// Use injected pager if available (for testing)
	if c.resourceSKUsPager != nil {
		return c.resourceSKUsPager, nil
	}

	client, err := armcompute.NewResourceSKUsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource SKUs client: %w", err)
	}

	return client.NewListPager(&armcompute.ResourceSKUsClientListOptions{Filter: nil}), nil
}

// collectVMSizesFromSKUs collects VM sizes from the resource SKUs pager.
func (c *Client) collectVMSizesFromSKUs(ctx context.Context, pager ResourceSKUsPager) ([]string, error) {
	vmSizes := make([]string, 0)

	for pageIdx := 0; pager.More(); pageIdx++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("context canceled during pagination: %w", err)
		}
		if pageIdx >= maxSKUPages {
			return nil, fmt.Errorf("compute: GetValidResourceTypes pagination cap (%d pages) reached", maxSKUPages)
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list VM sizes: %w", err)
		}

		for _, sku := range page.Value {
			if vmSize := c.extractVMSizeIfValid(sku); vmSize != "" {
				vmSizes = append(vmSizes, vmSize)
			}
		}
	}

	return vmSizes, nil
}

// extractVMSizeIfValid extracts the VM size name if it's a valid VM in the region.
func (c *Client) extractVMSizeIfValid(sku *armcompute.ResourceSKU) string {
	if sku.Name == nil || sku.ResourceType == nil || *sku.ResourceType != "virtualMachines" {
		return ""
	}

	if !c.isAvailableInRegion(sku, c.region) {
		return ""
	}

	return *sku.Name
}

// isAvailableInRegion checks if a SKU is available in the specified region.
func (c *Client) isAvailableInRegion(sku *armcompute.ResourceSKU, region string) bool {
	if sku.Locations == nil {
		return false
	}

	for _, location := range sku.Locations {
		if location != nil && strings.EqualFold(*location, region) {
			return true
		}
	}

	return false
}

// VMPricing contains VM pricing information.
type VMPricing struct {
	HourlyRate        float64
	ReservationPrice  float64
	OnDemandPrice     float64
	Currency          string
	SavingsPercentage float64
}

// getVMPricing gets real VM pricing from Azure Retail Prices API.
func (c *Client) getVMPricing(ctx context.Context, vmSize, region string, termYears int) (*VMPricing, error) {
	filter := fmt.Sprintf("serviceName eq 'Virtual Machines' and armRegionName eq '%s' and armSkuName eq '%s' and priceType eq 'Reservation'",
		strings.ReplaceAll(region, "'", "''"), strings.ReplaceAll(vmSize, "'", "''"))

	priceData, err := c.fetchAzurePricing(ctx, filter)
	if err != nil {
		return nil, err
	}

	if len(priceData.Items) == 0 {
		return nil, fmt.Errorf("no pricing data found for VM size %s in region %s", vmSize, region)
	}

	item, err := pricing.SelectReservation(priceData.Items, termYears, func(item pricing.RetailPriceItem) bool {
		return item.ServiceName == "Virtual Machines" && item.ArmRegionName == region && item.ArmSKUName == vmSize &&
			matchesVMReservationProduct(item)
	})
	if err != nil {
		return nil, fmt.Errorf("VM size %s in region %s: %w", vmSize, region, err)
	}

	return &VMPricing{
		HourlyRate:       item.RetailPrice / (8760.0 * float64(termYears)),
		ReservationPrice: item.RetailPrice,
		Currency:         item.CurrencyCode,
	}, nil
}

func matchesVMReservationProduct(item pricing.RetailPriceItem) bool {
	return item.ProductName != "" && item.MeterName != "" && item.SKUName != "" &&
		!strings.Contains(item.ProductName, "Windows") &&
		!strings.Contains(item.MeterName, "Spot") && !strings.Contains(item.SKUName, "Spot") &&
		!strings.Contains(item.MeterName, "Low Priority") && !strings.Contains(item.SKUName, "Low Priority")
}

// fetchAzurePricing fetches pricing data from Azure Retail Prices API,
// following NextPageLink until exhausted (hitting the shared page cap
// is an error). Delegates the pagination walk to pricing.FetchAll so every
// service client shares the same per-page timeout, seen-URL guard, and
// max-pages cap — see providers/azure/internal/pricing for those
// invariants.
//
// The earlier version issued a single GET and decoded just the first
// page, so any SKU/term/region combination that landed on page 2+
// produced a "no on-demand pricing found" error or a wrong price
// estimate.
func (c *Client) fetchAzurePricing(ctx context.Context, filter string) (*AzureRetailPrice, error) {
	params := url.Values{}
	params.Add("$filter", filter)
	params.Add("api-version", "2023-01-01-preview")

	initialURL := "https://prices.azure.com/api/retail/prices?" + params.Encode()
	items, err := pricing.FetchAll[AzureRetailPriceItem](ctx, c.httpClient, initialURL, pricing.DefaultPageTimeout, pricing.DefaultMaxPages)
	if err != nil {
		return nil, err
	}
	return &AzureRetailPrice{Items: items}, nil
}

// convertAzureVMRecommendation converts Azure VM reservation recommendation to common format.
//
// Returns nil when the SDK payload is unusable (nil, wrong concrete type,
// or missing Properties) so the caller can filter it out rather than
// append an empty recommendation. The field extraction lives in
// providers/azure/internal/recommendations so the four Azure service
// converters share the same type-assertion + nil-guard ladder.
//
// Details.VCPU and Details.MemoryGB are enriched from a lazily-cached
// armcompute.ResourceSKUsClient catalog (cachedSKULookup). The
// catalog is fetched ONCE per client lifetime; converter calls in
// the same GetRecommendations run share the in-memory map (the N+1
// invariant pinned by TestComputeClient_CachedSKULookup_FetchedOnce).
// On catalog-fetch failure or cache miss, both fields stay at 0
// (the omitempty JSON tags hide them from API payloads) and the
// conversion still succeeds — matches the graceful-degradation
// contract from cache/cosmosdb/database in PR #81.
//
// Platform / Tenancy / Scope still require additional Azure data
// sources (consumption usage records, dedicated-host inventory) and
// remain unpopulated — out of scope for this issue.
func (c *Client) convertAzureVMRecommendation(ctx context.Context, azureRec armconsumption.ReservationRecommendationClassification) *common.Recommendation {
	f := azrecs.ExtractConsumptionOrSkip("compute", azureRec)
	if f == nil {
		return nil
	}
	details := common.ComputeDetails{
		InstanceType: f.ResourceType,
	}
	if entry, ok := c.cachedSKULookup(ctx, f.ResourceType); ok {
		if entry.vCPUs > 0 {
			details.VCPU = entry.vCPUs
		}
		if entry.memoryGB > 0 {
			details.MemoryGB = entry.memoryGB
		}
	}
	return &common.Recommendation{
		Provider:             common.ProviderAzure,
		Service:              common.ServiceCompute,
		Account:              c.subscriptionID,
		Region:               f.Region,
		ResourceType:         f.ResourceType,
		Count:                f.Count,
		OnDemandCost:         f.OnDemandCost,
		CommitmentCost:       f.CommitmentCost,
		EstimatedSavings:     f.EstimatedSavings,
		RecurringMonthlyCost: f.RecurringMonthlyCost,
		CommitmentType:       common.CommitmentReservedInstance,
		Term:                 f.Term,
		PaymentOption:        "upfront",
		Timestamp:            time.Now(),
		Details:              details,
	}
}

// cachedSKULookup returns the SKU catalog entry for skuName, fetching
// the catalog lazily on first call. The catalog is fetched ONCE per
// client lifetime via armcompute.ResourceSKUsClient.NewListPager;
// subsequent calls are O(1) map lookups. ok=false on cache miss OR
// catalog-fetch failure — the caller falls back to VCPU=0 / MemoryGB=0
// rather than failing the whole conversion.
func (c *Client) cachedSKULookup(ctx context.Context, skuName string) (vmSKUEntry, bool) {
	c.skuCacheOnce.Do(func() {
		c.skuCacheMap = c.fetchSKUCatalogue(ctx)
	})
	if c.skuCacheMap == nil {
		return vmSKUEntry{}, false
	}
	entry, ok := c.skuCacheMap[skuName]
	return entry, ok
}

// fetchSKUCatalogue performs the single ResourceSKUsClient.NewListPager
// walk and reduces the response into a name->vmSKUEntry map keyed by
// SKU.Name (matches the recommendation engine's ResourceType output).
//
// Filters out non-VM resource types and SKUs not available in c.region
// (mirrors the existing extractVMSizeIfValid filter used by
// GetValidResourceTypes — a SKU listed for a different region is not
// safe to attribute to a recommendation in this client's region).
//
// Returns nil on pager-create, page-fetch error, or context cancellation
// so the sync.Once-gated cache field stays nil and cachedSKULookup falls
// back to the empty-fields path. Errors and cancellation are logged WARN
// once; context.Canceled/DeadlineExceeded are treated as terminal
// (feedback_ctx_cancel_terminal.md).
func (c *Client) fetchSKUCatalogue(ctx context.Context) map[string]vmSKUEntry {
	pager, err := c.createResourceSKUsPager()
	if err != nil {
		logging.Warnf("azure compute: SKU catalog pager create failed for region %s: %v — Details.VCPU/MemoryGB left at 0", c.region, err)
		return nil
	}
	out := make(map[string]vmSKUEntry)
	for pageIdx := 0; pager.More(); pageIdx++ {
		if err := ctx.Err(); err != nil {
			logging.Warnf("azure compute: SKU catalog fetch canceled for region %s after %d pages: %v; partial cache discarded", c.region, pageIdx, err)
			return nil
		}
		if pageIdx >= maxSKUPages {
			logging.Warnf("azure compute: SKU catalog pagination cap (%d pages) reached for region %s; partial cache (%d entries) used", maxSKUPages, c.region, len(out))
			break
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			logging.Warnf("azure compute: SKU catalog page fetch failed for region %s: %v; partial cache (%d entries) discarded, Details.VCPU/MemoryGB left at 0", c.region, err, len(out))
			return nil
		}
		c.populateVMSKUMapFromPage(out, page.Value)
	}
	return out
}

// populateVMSKUMapFromPage writes one vmSKUEntry per qualifying VM SKU
// found in the page into out. Filters non-VM resource types and SKUs
// not available in c.region. First-write-wins on duplicate SKU names —
// ResourceSKUs returns each SKU once per subscription, but defending
// against duplicates keeps the cache deterministic regardless of pager
// order. Extracted out of fetchSKUCatalogue to keep that function
// under the cyclomatic-complexity threshold enforced by the
// pre-commit hook (matches the cache/database extraction pattern from
// PR #81).
func (c *Client) populateVMSKUMapFromPage(out map[string]vmSKUEntry, skus []*armcompute.ResourceSKU) {
	for _, sku := range skus {
		if sku == nil || sku.Name == nil {
			continue
		}
		if sku.ResourceType == nil || *sku.ResourceType != "virtualMachines" {
			continue
		}
		if !c.isAvailableInRegion(sku, c.region) {
			continue
		}
		if _, exists := out[*sku.Name]; exists {
			continue
		}
		vCPUs, memoryGB := extractVMSKUCapabilities(sku)
		out[*sku.Name] = vmSKUEntry{vCPUs: vCPUs, memoryGB: memoryGB}
	}
}

// extractVMSKUCapabilities pulls the vCPUs and MemoryGB capabilities
// out of an armcompute.ResourceSKU's Capabilities name/value list.
// Returns (0, 0) when the capability is missing or unparseable —
// callers treat the zero value as "unknown" (omitempty JSON tag on
// common.ComputeDetails).
//
// Extracted out of fetchSKUCatalogue to keep that function under the
// cyclomatic-complexity threshold enforced by the pre-commit hook
// (matches the cache/database extraction pattern from PR #81).
func extractVMSKUCapabilities(sku *armcompute.ResourceSKU) (vCPUs int, memoryGB float64) {
	for _, cb := range sku.Capabilities {
		if cb == nil || cb.Name == nil || cb.Value == nil {
			continue
		}
		switch *cb.Name {
		case "vCPUs":
			if v, err := strconv.Atoi(*cb.Value); err == nil {
				vCPUs = v
			}
		case "MemoryGB":
			if v, err := strconv.ParseFloat(*cb.Value, 64); err == nil {
				memoryGB = v
			}
		}
	}
	return vCPUs, memoryGB
}
