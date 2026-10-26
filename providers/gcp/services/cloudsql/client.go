// Package cloudsql provides GCP Cloud SQL commitments client
package cloudsql

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/recommender/apiv1/recommenderpb"
	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/option"
	"google.golang.org/api/sqladmin/v1"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/internal/billingcurrency"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/internal/skulist"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/internal/skumatch"
)

// SQLAdminService interface for SQL admin operations (enables mocking).
type SQLAdminService interface {
	ListInstances(projectID string) (*sqladmin.InstancesListResponse, error)
	InsertInstance(projectID string, instance *sqladmin.DatabaseInstance) (*sqladmin.Operation, error)
	ListTiers(projectID string) (*sqladmin.TiersListResponse, error)
}

// BillingService interface for billing operations (enables mocking).
type BillingService interface {
	ListSKUs(serviceID string) (*cloudbilling.ListSkusResponse, error)
}

// RecommenderIterator preserves the legacy iteration API for source compatibility.
//
// Deprecated: Cloud SQL has no project-scoped commitment recommender (issue #294).
type RecommenderIterator interface {
	Next() (*recommenderpb.Recommendation, error)
}

// RecommenderClient preserves the legacy injection API for source compatibility.
//
// Deprecated: Cloud SQL has no project-scoped commitment recommender (issue #294).
type RecommenderClient interface {
	ListRecommendations(ctx context.Context, req *recommenderpb.ListRecommendationsRequest) RecommenderIterator
	Close() error
}

// Client handles GCP Cloud SQL commitments.
type Client struct {
	skuCache        *cloudbilling.ListSkusResponse
	ctx             context.Context
	projectID       string
	region          string
	sqlAdminService SQLAdminService
	billingService  BillingService
	clientOpts      []option.ClientOption
	// skuMu guards skuCache. It sits last because it holds no pointers
	// (govet fieldalignment).
	skuMu sync.Mutex
}

// NewClient creates a new GCP Cloud SQL client.
func NewClient(ctx context.Context, projectID, region string, opts ...option.ClientOption) (*Client, error) {
	return &Client{
		ctx:        ctx,
		projectID:  projectID,
		region:     region,
		clientOpts: opts,
	}, nil
}

// SetSQLAdminService sets the SQL admin service (for testing).
func (c *Client) SetSQLAdminService(svc SQLAdminService) {
	c.sqlAdminService = svc
}

// SetBillingService sets the billing service (for testing).
func (c *Client) SetBillingService(svc BillingService) {
	c.billingService = svc
}

// SetRecommenderClient ignores the supplied client and does not close it.
//
// Deprecated: Cloud SQL has no project-scoped commitment recommender (issue #294).
func (c *Client) SetRecommenderClient(_ RecommenderClient) {}

// realSQLAdminService wraps the real sqladmin.Service.
type realSQLAdminService struct {
	service *sqladmin.Service
}

func (r *realSQLAdminService) ListInstances(projectID string) (*sqladmin.InstancesListResponse, error) {
	return r.service.Instances.List(projectID).Do()
}

func (r *realSQLAdminService) InsertInstance(projectID string, instance *sqladmin.DatabaseInstance) (*sqladmin.Operation, error) {
	return r.service.Instances.Insert(projectID, instance).Do()
}

func (r *realSQLAdminService) ListTiers(projectID string) (*sqladmin.TiersListResponse, error) {
	return r.service.Tiers.List(projectID).Do()
}

// realBillingService wraps the real cloudbilling.APIService.
type realBillingService struct {
	service *cloudbilling.APIService
}

func (r *realBillingService) ListSKUs(serviceID string) (*cloudbilling.ListSkusResponse, error) {
	skus, err := skulist.All(context.Background(), r.service.Services.Skus.List(serviceID), skulist.DefaultMaxPages)
	if err != nil {
		return nil, fmt.Errorf("%s: list SKUs for %s: %w", "cloudsql", serviceID, err)
	}
	return &cloudbilling.ListSkusResponse{Skus: skus}, nil
}

// GetServiceType returns the service type.
func (c *Client) GetServiceType() common.ServiceType {
	return common.ServiceRelationalDB
}

// GetRegion returns the region.
func (c *Client) GetRegion() string {
	return c.region
}

// GetRecommendations always returns an error (issue #294). Cloud SQL CUDs are
// spend-based, and Google recommends them only at Cloud Billing account scope
// (google.cloudbilling.commitment.SpendBasedCommitmentRecommender); the
// project-scoped google.cloudsql.instance.PerformanceRecommender
// gives per-instance tuning advice, not commitments, so its output must not be
// relabeled as a CUD. The billing-scoped path is designed in
// docs/design/gcp-spend-commitments.md. The error is explicit so callers do
// not read an empty result as "nothing to buy".
func (c *Client) GetRecommendations(_ context.Context, _ *common.RecommendationParams) ([]common.Recommendation, error) {
	return nil, fmt.Errorf("%w: Cloud SQL spend-based CUD recommendations are only available at billing-account scope", common.ErrCommitmentPurchaseNotSupported)
}

// GetExistingCommitments returns an empty slice for Cloud SQL. Cloud SQL
// spend-based CUDs are purchased via the Cloud Billing console / Cloud Commerce
// Consumer Procurement API, not exposed through the sqladmin API. The legacy
// PricingPlan "PACKAGE" is a per-instance billing mode (non-commitment,
// deprecated) -- treating it as a commitment caused double-counting against
// real spend-based CUDs (10-L3). Return empty until a proper commitment-
// detection path is available.
func (c *Client) GetExistingCommitments(_ context.Context) ([]common.Commitment, error) {
	return nil, nil
}

// PurchaseCommitment is intentionally a no-op for Cloud SQL: GCP exposes no
// programmatic API to buy a Cloud SQL committed-use discount. CUD purchases are
// spend-based and bought via the Cloud Billing console / Cloud Commerce
// Consumer Procurement API, not via sqladmin.Instances.Insert. The previous
// implementation created a brand-new billable SQL instance (the legacy
// PricingPlan "PACKAGE" is a per-instance billing mode, not a commitment), so a
// "purchase" silently spun up a new database that kept billing. Cloud SQL
// recommendations are therefore advisory only; this returns a clear
// not-supported error and never calls any resource-creation API (issue #640).
func (c *Client) PurchaseCommitment(ctx context.Context, rec common.Recommendation, opts common.PurchaseOptions) (common.PurchaseResult, error) {
	return common.PurchaseResult{
		Recommendation: rec,
		DryRun:         false,
		Success:        false,
		Timestamp:      time.Now(),
		Error: fmt.Errorf("%w: GCP Cloud SQL committed-use discounts are spend-based and "+
			"must be purchased via the Cloud Billing console or Cloud Commerce Consumer "+
			"Procurement API; this recommendation is advisory only", common.ErrCommitmentPurchaseNotSupported),
	}, fmt.Errorf("%w: Cloud SQL", common.ErrCommitmentPurchaseNotSupported)
}

// ValidateOffering validates that a Cloud SQL tier exists.
func (c *Client) ValidateOffering(ctx context.Context, rec common.Recommendation) error {
	validTiers, err := c.GetValidResourceTypes(ctx)
	if err != nil {
		return fmt.Errorf("failed to get valid tiers: %w", err)
	}

	for _, tier := range validTiers {
		if tier == rec.ResourceType {
			return nil
		}
	}

	return fmt.Errorf("invalid Cloud SQL tier: %s", rec.ResourceType)
}

// GetOfferingDetails retrieves Cloud SQL offering details from GCP Billing API.
func (c *Client) GetOfferingDetails(ctx context.Context, rec common.Recommendation) (*common.OfferingDetails, error) {
	termYears, err := skumatch.TermYears(rec.Term)
	if err != nil {
		return nil, err
	}

	pricing, err := c.getSQLPricing(ctx, rec.ResourceType, c.region, termYears)
	if err != nil {
		return nil, fmt.Errorf("failed to get pricing: %w", err)
	}

	var upfrontCost, recurringCost float64
	totalCost := pricing.CommitmentPrice

	switch rec.PaymentOption {
	case "all-upfront", "upfront":
		upfrontCost = totalCost
		recurringCost = 0
	case "monthly", "no-upfront":
		upfrontCost = 0
		recurringCost = totalCost / (float64(termYears) * 12)
	default:
		upfrontCost = totalCost
	}

	return &common.OfferingDetails{
		OfferingID:          fmt.Sprintf("gcp-cloudsql-%s-%s-%s", rec.ResourceType, c.region, rec.Term),
		ResourceType:        rec.ResourceType,
		Term:                rec.Term,
		PaymentOption:       rec.PaymentOption,
		UpfrontCost:         upfrontCost,
		RecurringCost:       recurringCost,
		TotalCost:           totalCost,
		EffectiveHourlyRate: pricing.HourlyRate,
		Currency:            pricing.Currency,
	}, nil
}

// GetValidResourceTypes returns valid Cloud SQL tiers.
func (c *Client) GetValidResourceTypes(ctx context.Context) ([]string, error) {
	// Use injected service if available (for testing)
	var svc SQLAdminService
	if c.sqlAdminService != nil {
		svc = c.sqlAdminService
	} else {
		service, err := sqladmin.NewService(ctx, c.clientOpts...)
		if err != nil {
			return nil, fmt.Errorf("failed to create SQL admin service: %w", err)
		}
		svc = &realSQLAdminService{service: service}
	}

	// List available tiers for the region
	tiers, err := svc.ListTiers(c.projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list SQL tiers: %w", err)
	}

	validTiers := make([]string, 0)
	for _, tier := range tiers.Items {
		// Filter for tiers available in the region
		if len(tier.Region) == 0 || contains(tier.Region, c.region) {
			validTiers = append(validTiers, tier.Tier)
		}
	}

	if len(validTiers) == 0 {
		return nil, fmt.Errorf("no Cloud SQL tiers found for region %s", c.region)
	}

	return validTiers, nil
}

// SQLPricing contains pricing information for Cloud SQL.
type SQLPricing struct {
	Currency          string
	HourlyRate        float64
	CommitmentPrice   float64
	OnDemandPrice     float64
	SavingsPercentage float64
}

// getSQLPricing gets pricing from GCP Cloud Billing Catalog API.
// It returns an error when commitment pricing is absent from the catalog rather
// than fabricating a price from a hardcoded discount factor (issue #1020).
func (c *Client) getSQLPricing(ctx context.Context, tier, region string, termYears int) (*SQLPricing, error) {
	svc, err := c.getOrCreateBillingService(ctx)
	if err != nil {
		return nil, err
	}

	skus, err := c.listSKUs(svc)
	if err != nil {
		return nil, fmt.Errorf("failed to list SKUs: %w", err)
	}

	onDemandPrice, commitmentPrice, currency, err := extractSQLPricingFromSKUs(skus.Skus, tier, region, termYears)
	if err != nil {
		return nil, err
	}
	if onDemandPrice == 0 {
		return nil, fmt.Errorf("no pricing found for Cloud SQL tier %s", tier)
	}
	if commitmentPrice == 0 {
		return nil, fmt.Errorf("no commitment pricing found for Cloud SQL tier %s in region %s: catalog has no CUD SKU; cannot compute savings percentage", tier, region)
	}

	hoursInTerm := common.HoursPerYear * float64(termYears)
	// Scale the per-hour commitment price to a term total so it is on the
	// same basis as onDemandPrice * hoursInTerm. Without this, the savings
	// percentage would be nearly 100% (per-hour price vs term total).
	commitmentPriceTerm := commitmentPrice * hoursInTerm
	savingsPercentage := calculateSQLSavingsPercentage(onDemandPrice, hoursInTerm, commitmentPriceTerm)

	return &SQLPricing{
		HourlyRate:        commitmentPrice,
		CommitmentPrice:   commitmentPriceTerm,
		OnDemandPrice:     onDemandPrice * hoursInTerm,
		Currency:          currency,
		SavingsPercentage: savingsPercentage,
	}, nil
}

// getOrCreateBillingService returns the billing service, creating it if needed.
func (c *Client) getOrCreateBillingService(ctx context.Context) (BillingService, error) {
	if c.billingService != nil {
		return c.billingService, nil
	}

	service, err := cloudbilling.NewService(ctx, c.clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create billing service: %w", err)
	}

	return &realBillingService{service: service}, nil
}

// extractSQLPricingFromSKUs extracts on-demand and commitment pricing from the SKU list.
// Cloud SQL committed-use discounts are SKUs whose Category.UsageType is Commit1Yr or Commit3Yr.
func extractSQLPricingFromSKUs(skus []*cloudbilling.Sku, tier, region string, termYears int) (onDemand, commitment float64, currency string, err error) {
	var prices skumatch.SlotPrices
	for _, sku := range skus {
		if !skuMatchesTier(sku, tier, region) {
			continue
		}

		price, curr := extractSQLPriceFromSKU(sku)
		if price == 0 {
			continue
		}

		currency, err = billingcurrency.Unify(currency, curr, sku.SkuId)
		if err != nil {
			return 0, 0, "", err
		}

		slot, slotErr := skumatch.Slot(sku)
		if slotErr != nil {
			return 0, 0, "", slotErr
		}
		if err := prices.Add(slot, sku, price); err != nil {
			return 0, 0, "", err
		}
	}

	commitmentPrice, termErr := prices.Commitment(termYears)
	if termErr != nil {
		return 0, 0, "", termErr
	}
	return prices.OnDemand, commitmentPrice, currency, nil
}

// extractSQLPriceFromSKU extracts the unit price from a SKU.
func extractSQLPriceFromSKU(sku *cloudbilling.Sku) (price float64, currency string) {
	if len(sku.PricingInfo) == 0 {
		return 0, ""
	}

	pricingInfo := sku.PricingInfo[0]
	rate := skumatch.PaidRate(pricingInfo.PricingExpression)
	if rate == nil {
		return 0, ""
	}

	price = float64(rate.UnitPrice.Units) + float64(rate.UnitPrice.Nanos)/1e9
	return price, rate.UnitPrice.CurrencyCode
}

// calculateSQLSavingsPercentage calculates the savings percentage.
func calculateSQLSavingsPercentage(onDemandPrice, hoursInTerm, commitmentPrice float64) float64 {
	onDemandTotal := onDemandPrice * hoursInTerm
	return ((onDemandTotal - commitmentPrice) / onDemandTotal) * 100
}

// skuMatchesTier checks if a SKU matches the tier and region.
func skuMatchesTier(sku *cloudbilling.Sku, tier, region string) bool {
	return skumatch.HasToken(sku.Description, tier) && skumatch.InRegion(sku, region)
}

// contains checks if a slice contains a string.
func contains(slice []string, str string) bool {
	for _, s := range slice {
		if strings.EqualFold(s, str) {
			return true
		}
	}
	return false
}

// listSKUs returns the service's SKU catalog, listing it once per client so
// that pricing N recommendations costs one catalog read instead of N.
func (c *Client) listSKUs(svc BillingService) (*cloudbilling.ListSkusResponse, error) {
	c.skuMu.Lock()
	defer c.skuMu.Unlock()
	if c.skuCache != nil {
		return c.skuCache, nil
	}
	skus, err := svc.ListSKUs(cloudSQLServiceID)
	if err != nil {
		return nil, err
	}
	c.skuCache = skus
	return skus, nil
}

// cloudSQLServiceID is the Cloud Billing service for Cloud SQL.
const cloudSQLServiceID = "services/9662-B51E-5089"
