// Package synapse provides Azure Synapse Analytics Reserved Capacity client.
// Azure Synapse Analytics (formerly SQL Data Warehouse) supports reservation-based
// commitments for Dedicated SQL Pool DWUs and Spark Compute Units (SCUs).
// Reservations are issued via the Azure Capacity / Consumption APIs — the same
// pattern used by cosmosdb and cache in this provider.
package synapse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/httpclient"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/pricing"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/recommendations"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/internal/reservations"
)

// reservationResourceTypeSynapse is the canonical resourceType value for Azure
// Synapse Analytics (Dedicated SQL Pool) in the Consumption API $filter.
// Source: Azure REST API spec for Microsoft.Consumption/reservationRecommendations
// (2021-10-01 stable). The previous hand-written value "SQLDatabaseDTU" is not
// a valid enum member and caused the API to return no recommendations.
const reservationResourceTypeSynapse = "SqlDataWarehouse"

// maxRecsPages caps Consumption API recommendation pagination.
const maxRecsPages = 10

// maxReservationsPages caps reservation-detail pagination.
const maxReservationsPages = 50

// HTTPClient interface for HTTP operations (enables mocking).
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// RecommendationsPager interface for recommendations pager (enables mocking).
type RecommendationsPager interface {
	More() bool
	NextPage(ctx context.Context) (armconsumption.ReservationRecommendationsClientListResponse, error)
}

// ReservationsDetailsPager interface for reservations details pager (enables mocking).
type ReservationsDetailsPager interface {
	More() bool
	NextPage(ctx context.Context) (armconsumption.ReservationsDetailsClientListResponse, error)
}

// Client handles Azure Synapse Analytics Reserved Capacity.
type Client struct {
	cred                 azcore.TokenCredential
	subscriptionID       string
	region               string
	httpClient           HTTPClient
	recommendationsPager RecommendationsPager
	reservationsPager    ReservationsDetailsPager
}

// NewClient creates a new Azure Synapse Analytics client.
func NewClient(cred azcore.TokenCredential, subscriptionID, region string) *Client {
	return &Client{
		cred:           cred,
		subscriptionID: subscriptionID,
		region:         region,
		httpClient:     httpclient.New(),
	}
}

// NewClientWithHTTP creates a new Azure Synapse client with a custom HTTP client (for testing).
// When httpClient is nil, the SSRF-hardened httpclient.New() is used so the nil
// fallback also blocks IMDS connections.
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

// SetRecommendationsPager sets the recommendations pager (for testing).
func (c *Client) SetRecommendationsPager(pager RecommendationsPager) {
	c.recommendationsPager = pager
}

// SetReservationsPager sets the reservations pager (for testing).
func (c *Client) SetReservationsPager(pager ReservationsDetailsPager) {
	c.reservationsPager = pager
}

// GetServiceType returns the service type.
func (c *Client) GetServiceType() common.ServiceType {
	return common.ServiceDataWarehouse
}

// GetRegion returns the region.
func (c *Client) GetRegion() string {
	return c.region
}

// RetailPriceItem is the exported Azure Retail Prices API item shape for
// Synapse Analytics. Private pricing selection uses the shared item shape.
type RetailPriceItem struct {
	CurrencyCode    string  `json:"currencyCode"`
	RetailPrice     float64 `json:"retailPrice"`
	UnitPrice       float64 `json:"unitPrice"`
	ArmRegionName   string  `json:"armRegionName"`
	ProductName     string  `json:"productName"`
	ServiceName     string  `json:"serviceName"`
	ArmSKUName      string  `json:"armSkuName"`
	MeterName       string  `json:"meterName"`
	SKUName         string  `json:"skuName"`
	ReservationTerm string  `json:"reservationTerm"`
	Type            string  `json:"type"`
}

// GetRecommendations retrieves Synapse reservation recommendations from the
// Azure Consumption API.
func (c *Client) GetRecommendations(ctx context.Context, _ *common.RecommendationParams) ([]common.Recommendation, error) {
	recs := make([]common.Recommendation, 0)

	var pager RecommendationsPager
	if c.recommendationsPager != nil {
		pager = c.recommendationsPager
	} else {
		client, err := armconsumption.NewReservationRecommendationsClient(c.cred, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create consumption client: %w", err)
		}
		scope := fmt.Sprintf("/subscriptions/%s", c.subscriptionID)
		filter := "properties/scope eq 'Shared' and properties/resourceType eq '" + reservationResourceTypeSynapse + "'"
		pager = client.NewListPager(scope, &armconsumption.ReservationRecommendationsClientListOptions{Filter: &filter})
	}

	for pageIdx := 0; pager.More(); pageIdx++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("context canceled during pagination: %w", err)
		}
		if pageIdx >= maxRecsPages {
			return nil, fmt.Errorf("synapse: GetRecommendations pagination cap (%d pages) reached", maxRecsPages)
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get Synapse recommendations: %w", err)
		}

		recs = c.appendRegionRecommendations(recs, page.Value)
	}

	return recs, nil
}

// appendRegionRecommendations converts the page and appends the entries that
// belong to the client's region (all of them when no region is set).
func (c *Client) appendRegionRecommendations(recs []common.Recommendation, page []armconsumption.ReservationRecommendationClassification) []common.Recommendation {
	for _, rec := range page {
		converted := c.convertSynapseRecommendation(rec)
		if converted == nil {
			continue
		}
		if c.region != "" && !strings.EqualFold(converted.Region, c.region) {
			continue
		}
		recs = append(recs, *converted)
	}
	return recs
}

// GetExistingCommitments retrieves existing Synapse reserved capacity
// commitments from the Azure Consumption API.
func (c *Client) GetExistingCommitments(ctx context.Context) ([]common.Commitment, error) {
	pager, err := c.createReservationsPager()
	if err != nil {
		return nil, fmt.Errorf("synapse: create reservations pager: %w", err)
	}

	return c.collectSynapseReservations(ctx, pager)
}

func (c *Client) createReservationsPager() (ReservationsDetailsPager, error) {
	if c.reservationsPager != nil {
		return c.reservationsPager, nil
	}
	client, err := armconsumption.NewReservationsDetailsClient(c.cred, nil)
	if err != nil {
		return nil, err
	}
	scope := fmt.Sprintf("subscriptions/%s", c.subscriptionID)
	return client.NewListPager(scope, &armconsumption.ReservationsDetailsClientListOptions{}), nil
}

func (c *Client) collectSynapseReservations(ctx context.Context, pager ReservationsDetailsPager) ([]common.Commitment, error) {
	commitments := make([]common.Commitment, 0)

	for pageIdx := 0; pager.More(); pageIdx++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("context canceled during pagination: %w", err)
		}
		if pageIdx >= maxReservationsPages {
			return nil, fmt.Errorf("synapse: GetExistingCommitments pagination cap (%d pages) reached", maxReservationsPages)
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("synapse: list reservations: %w", err)
		}
		for _, detail := range page.Value {
			if commitment := c.convertSynapseReservation(detail); commitment != nil {
				commitments = append(commitments, *commitment)
			}
		}
	}

	// ReservationsDetails is a daily usage API: one row per reservation per
	// usage day. Without this, a single reservation held for N days would
	// surface as N commitments sharing one CommitmentID (issue #73).
	return reservations.DedupeCommitmentsByID(commitments), nil
}

// convertSynapseReservation converts a reservation detail to a Commitment if
// it is a Synapse SQL Pool or Spark reservation. Identification relies on the
// SKU name containing a Synapse-specific prefix ("DW" for Dedicated SQL Pools
// or "SCU" for Spark Compute Units).
func (c *Client) convertSynapseReservation(detail *armconsumption.ReservationDetail) *common.Commitment {
	if detail == nil || detail.Properties == nil {
		return nil
	}
	props := detail.Properties
	if props.SKUName == nil {
		return nil
	}
	skuLower := strings.ToLower(*props.SKUName)
	if !strings.HasPrefix(skuLower, "dw") &&
		!strings.HasPrefix(skuLower, "scu") &&
		!strings.Contains(skuLower, "synapse") {
		return nil
	}

	commitment := &common.Commitment{
		Provider:       common.ProviderAzure,
		Account:        c.subscriptionID,
		CommitmentType: common.CommitmentReservedInstance,
		Service:        common.ServiceDataWarehouse,
		Region:         c.region,
		State:          common.CommitmentStateActive,
	}
	if props.ReservationID != nil {
		commitment.CommitmentID = *props.ReservationID
	}
	commitment.ResourceType = *props.SKUName
	return commitment
}

// PurchaseCommitment purchases Synapse reserved capacity via the Azure
// Reservations API two-step flow (calculatePrice -> purchase). The reserved
// resource type is "SqlDW" which covers Dedicated SQL Pool DWU reservations.
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

	if strings.TrimSpace(rec.ResourceType) == "" {
		result.Error = fmt.Errorf("resource type is required")
		return result, result.Error
	}
	if rec.Count <= 0 {
		result.Error = fmt.Errorf("quantity must be greater than zero")
		return result, result.Error
	}

	termYears, err := reservations.ParseTermYears(rec.Term)
	if err != nil {
		result.Error = err
		return result, result.Error
	}
	billingPlan, err := reservations.BillingPlanForPaymentOption(rec.PaymentOption)
	if err != nil {
		result.Error = err
		return result, result.Error
	}

	requestBody := map[string]interface{}{
		"sku": map[string]string{
			"name": rec.ResourceType,
		},
		"location": c.region,
		"properties": map[string]interface{}{
			"reservedResourceType": string(armreservations.ReservedResourceTypeSQLDataWarehouse),
			"billingScopeId":       fmt.Sprintf("/subscriptions/%s", c.subscriptionID),
			"billingPlan":          string(billingPlan),
			"term":                 fmt.Sprintf("P%dY", termYears),
			"quantity":             rec.Count,
			"displayName":          fmt.Sprintf("Synapse SQL Pool Reservation - %s", rec.ResourceType),
			"appliedScopeType":     "Shared",
			"renew":                false,
		},
	}
	reservations.ApplyPurchaseTags(requestBody, opts.Source, opts.IdempotencyToken)

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		result.Error = fmt.Errorf("failed to marshal request: %w", err)
		return result, result.Error
	}

	token, err := c.cred.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{"https://management.azure.com/.default"},
	})
	if err != nil {
		result.Error = fmt.Errorf("failed to get access token: %w", err)
		return result, result.Error
	}

	reservationOrderID, err := reservations.DoIdempotentPurchaseTwoStep(ctx, c.httpClient, reservations.CalculatePriceURL(), bodyBytes, token.Token, opts.IdempotencyToken)
	if err != nil {
		result.Error = err
		return result, result.Error
	}

	result.Success = true
	result.CommitmentID = reservationOrderID
	result.Cost = reservations.UpfrontCostForBillingPlan(billingPlan)
	return result, nil
}

// ValidateOffering validates that a Synapse SKU is in the known set.
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
	return fmt.Errorf("invalid Azure Synapse SKU: %s", rec.ResourceType)
}

// GetOfferingDetails retrieves Synapse reservation offering details from the
// Azure Retail Prices API.
func (c *Client) GetOfferingDetails(ctx context.Context, rec common.Recommendation) (*common.OfferingDetails, error) {
	termYears, err := reservations.ParseTermYears(rec.Term)
	if err != nil {
		return nil, err
	}

	p, err := c.getSynapsePricing(ctx, rec.ResourceType, c.region, termYears)
	if err != nil {
		return nil, fmt.Errorf("failed to get pricing: %w", err)
	}

	var upfrontCost, recurringCost float64
	totalCost := p.ReservationPrice

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
		return nil, fmt.Errorf("unsupported payment option for Azure Synapse offering details: %q", rec.PaymentOption)
	}

	return &common.OfferingDetails{
		OfferingID:          fmt.Sprintf("azure-synapse-%s-%s-%s", rec.ResourceType, c.region, rec.Term),
		ResourceType:        rec.ResourceType,
		Term:                rec.Term,
		PaymentOption:       rec.PaymentOption,
		UpfrontCost:         upfrontCost,
		RecurringCost:       recurringCost,
		TotalCost:           totalCost,
		EffectiveHourlyRate: p.HourlyRate,
		Currency:            p.Currency,
	}, nil
}

// GetValidResourceTypes returns the known Synapse Dedicated SQL Pool DWU SKUs
// that support reservations. Azure Synapse reservations are available for
// DW100c through DW30000c performance levels.
func (c *Client) GetValidResourceTypes(_ context.Context) ([]string, error) {
	return []string{
		// Dedicated SQL Pool DWU levels (cDWU generation)
		"DW100c",
		"DW200c",
		"DW300c",
		"DW400c",
		"DW500c",
		"DW1000c",
		"DW1500c",
		"DW2000c",
		"DW2500c",
		"DW3000c",
		"DW5000c",
		"DW6000c",
		"DW7500c",
		"DW10000c",
		"DW15000c",
		"DW30000c",
	}, nil
}

// Pricing holds pricing information for Synapse Analytics.
type Pricing struct {
	HourlyRate        float64
	ReservationPrice  float64
	OnDemandPrice     float64
	Currency          string
	SavingsPercentage float64
}

// getSynapsePricing fetches pricing from the Azure Retail Prices API.
func (c *Client) getSynapsePricing(ctx context.Context, sku, region string, termYears int) (*Pricing, error) {
	filter := fmt.Sprintf("serviceName eq 'Azure Synapse Analytics' and productName eq 'Azure Synapse Analytics Dedicated SQL Pool' and armRegionName eq '%s' and skuName eq '%s' and priceType eq 'Reservation'",
		strings.ReplaceAll(region, "'", "''"), strings.ReplaceAll(sku, "'", "''"))

	params := url.Values{}
	params.Add("$filter", filter)
	params.Add("api-version", "2023-01-01-preview")

	initialURL := "https://prices.azure.com/api/retail/prices?" + params.Encode()
	items, err := pricing.FetchAll[pricing.RetailPriceItem](ctx, c.httpClient, initialURL, pricing.DefaultPageTimeout, pricing.DefaultMaxPages)
	if err != nil {
		return nil, err
	}

	selected, err := pricing.SelectReservation(items, termYears, func(item pricing.RetailPriceItem) bool {
		return item.ServiceName == "Azure Synapse Analytics" &&
			item.ProductName == "Azure Synapse Analytics Dedicated SQL Pool" &&
			item.ArmRegionName == region && item.SKUName == sku
	})
	if err != nil {
		return nil, fmt.Errorf("synapse reservation pricing for SKU %s in region %s: %w", sku, region, err)
	}

	return &Pricing{
		HourlyRate:       selected.RetailPrice / (8760.0 * float64(termYears)),
		ReservationPrice: selected.RetailPrice,
		Currency:         selected.CurrencyCode,
	}, nil
}

// convertSynapseRecommendation converts an Azure reservation recommendation
// to the common Recommendation format.
func (c *Client) convertSynapseRecommendation(azureRec armconsumption.ReservationRecommendationClassification) *common.Recommendation {
	f := recommendations.Extract(azureRec)
	if f == nil {
		return nil
	}
	details := common.DataWarehouseDetails{
		NodeType:    f.ResourceType,
		ClusterType: "dedicated-sql-pool",
	}
	return &common.Recommendation{
		Provider:             common.ProviderAzure,
		Service:              common.ServiceDataWarehouse,
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
