// Package memorystore provides GCP Memorystore (Redis) commitments client
package memorystore

import (
	"context"
	"fmt"
	"sync"
	"time"

	"cloud.google.com/go/recommender/apiv1/recommenderpb"
	"cloud.google.com/go/redis/apiv1/redispb"
	gax "github.com/googleapis/gax-go/v2"
	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/option"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/internal/billingcurrency"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/internal/skulist"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/internal/skumatch"
)

// RedisService interface for Redis operations.
type RedisService interface {
	ListInstances(ctx context.Context, req *redispb.ListInstancesRequest) RedisIterator
	CreateInstance(ctx context.Context, req *redispb.CreateInstanceRequest) (CreateInstanceOperation, error)
	Close() error
}

// RedisIterator interface for iterating Redis instances.
type RedisIterator interface {
	Next() (*redispb.Instance, error)
}

// CreateInstanceOperation interface for create instance operation.
type CreateInstanceOperation interface {
	Wait(ctx context.Context, opts ...gax.CallOption) (*redispb.Instance, error)
}

// BillingService interface for Cloud Billing operations.
type BillingService interface {
	ListSKUs(serviceID string) (*cloudbilling.ListSkusResponse, error)
}

// RecommenderIterator interface for iterating recommendations.
type RecommenderIterator interface {
	Next() (*recommenderpb.Recommendation, error)
}

// RecommenderClient interface for recommender operations.
type RecommenderClient interface {
	ListRecommendations(ctx context.Context, req *recommenderpb.ListRecommendationsRequest) RecommenderIterator
	Close() error
}

// Client handles GCP Memorystore (Redis) commitments.
type Client struct {
	skuCache       *cloudbilling.ListSkusResponse
	ctx            context.Context
	projectID      string
	region         string
	redisService   RedisService
	billingService BillingService
	clientOpts     []option.ClientOption
	// skuMu guards skuCache. It sits last because it holds no pointers
	// (govet fieldalignment).
	skuMu sync.Mutex
}

// NewClient creates a new GCP Memorystore client.
func NewClient(ctx context.Context, projectID, region string, opts ...option.ClientOption) (*Client, error) {
	return &Client{
		ctx:        ctx,
		projectID:  projectID,
		region:     region,
		clientOpts: opts,
	}, nil
}

// SetRedisService sets the Redis service (for testing).
func (c *Client) SetRedisService(svc RedisService) {
	c.redisService = svc
}

// SetBillingService sets the billing service (for testing).
func (c *Client) SetBillingService(svc BillingService) {
	c.billingService = svc
}

// SetRecommenderClient ignores the supplied client and does not close it.
//
// Deprecated: Memorystore has no project-scoped commitment recommender (issue #294).
func (c *Client) SetRecommenderClient(_ RecommenderClient) {}

// realBillingService wraps the actual Cloud Billing service.
type realBillingService struct {
	service *cloudbilling.APIService
}

func (r *realBillingService) ListSKUs(serviceID string) (*cloudbilling.ListSkusResponse, error) {
	skus, err := skulist.All(context.Background(), r.service.Services.Skus.List(serviceID), skulist.DefaultMaxPages)
	if err != nil {
		return nil, fmt.Errorf("%s: list SKUs for %s: %w", "memorystore", serviceID, err)
	}
	return &cloudbilling.ListSkusResponse{Skus: skus}, nil
}

// GetServiceType returns the service type.
func (c *Client) GetServiceType() common.ServiceType {
	return common.ServiceCache
}

// GetRegion returns the region.
func (c *Client) GetRegion() string {
	return c.region
}

// GetRecommendations always returns an error (issue #294). Memorystore CUDs are
// spend-based, and Google recommends them only at Cloud Billing account scope
// (google.cloudbilling.commitment.SpendBasedCommitmentRecommender); the
// project-scoped google.memorystore.redis.PerformanceRecommender
// gives per-instance tuning advice, not commitments, so its output must not be
// relabeled as a CUD. The billing-scoped path is designed in
// docs/design/gcp-spend-commitments.md. The error is explicit so callers do
// not read an empty result as "nothing to buy".
func (c *Client) GetRecommendations(_ context.Context, _ *common.RecommendationParams) ([]common.Recommendation, error) {
	return nil, fmt.Errorf("%w: Memorystore spend-based CUD recommendations are only available at billing-account scope", common.ErrCommitmentPurchaseNotSupported)
}

// GetExistingCommitments retrieves existing Memorystore Redis commitments.
func (c *Client) GetExistingCommitments(ctx context.Context) ([]common.Commitment, error) {
	// GCP Memorystore Redis does not expose commitment status via the Redis API.
	// ReservedIpRange (previously used here) is the VPC peering CIDR, not a
	// commitment indicator. Return empty until a proper detection method is available.
	return nil, nil
}

// PurchaseCommitment is intentionally a no-op for Memorystore: GCP exposes no
// standalone committed-use discount purchase API for Memorystore. Memorystore is
// covered by spend-based CUDs bought via the Cloud Billing console / Cloud
// Commerce Consumer Procurement API, not by creating an instance. The previous
// implementation created a brand-new billable Redis instance (a reserved IP range
// is networking config, not a commitment), so a "purchase" silently spun up a new
// Redis instance that kept billing. Memorystore recommendations are therefore
// advisory only; this returns a clear not-supported error and never calls any
// resource-creation API (issue #640).
func (c *Client) PurchaseCommitment(ctx context.Context, rec common.Recommendation, opts common.PurchaseOptions) (common.PurchaseResult, error) {
	return common.PurchaseResult{
		Recommendation: rec,
		DryRun:         false,
		Success:        false,
		Timestamp:      time.Now(),
		Error: fmt.Errorf("%w: GCP Memorystore has no standalone committed-use discount "+
			"purchase API (spend-based CUDs are bought via the Cloud Billing console or "+
			"Cloud Commerce Consumer Procurement API); this recommendation is advisory only",
			common.ErrCommitmentPurchaseNotSupported),
	}, fmt.Errorf("%w: Memorystore", common.ErrCommitmentPurchaseNotSupported)
}

// ValidateOffering validates that a Redis tier exists.
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

	return fmt.Errorf("invalid Memorystore tier: %s", rec.ResourceType)
}

// GetOfferingDetails retrieves Memorystore offering details from GCP Billing API.
func (c *Client) GetOfferingDetails(ctx context.Context, rec common.Recommendation) (*common.OfferingDetails, error) {
	termYears := 1
	if rec.Term == "3yr" || rec.Term == "3" {
		termYears = 3
	}

	pricing, err := c.getRedisPricing(ctx, rec.ResourceType, c.region, termYears)
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
		OfferingID:          fmt.Sprintf("gcp-memorystore-%s-%s-%s", rec.ResourceType, c.region, rec.Term),
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

// GetValidResourceTypes returns valid Memorystore tiers.
func (c *Client) GetValidResourceTypes(ctx context.Context) ([]string, error) {
	// Memorystore Redis has predefined tiers
	validTiers := []string{
		"BASIC",
		"STANDARD_HA",
	}

	return validTiers, nil
}

// RedisPricing contains pricing information for Memorystore Redis.
type RedisPricing struct {
	Currency          string
	HourlyRate        float64
	CommitmentPrice   float64
	OnDemandPrice     float64
	SavingsPercentage float64
}

// getRedisPricing gets pricing from GCP Cloud Billing Catalog API.
// It returns an error when commitment pricing is absent from the catalog rather
// than fabricating a price from a hardcoded discount factor (issue #1020).
func (c *Client) getRedisPricing(ctx context.Context, tier, region string, termYears int) (*RedisPricing, error) {
	billingSvc, err := c.getOrCreateBillingService(ctx)
	if err != nil {
		return nil, err
	}

	skus, err := c.listSKUs(billingSvc)
	if err != nil {
		return nil, fmt.Errorf("failed to list SKUs: %w", err)
	}

	onDemandPrice, commitmentPrice, currency, err := extractPricingFromSKUs(skus.Skus, tier, region)
	if err != nil {
		return nil, err
	}
	if onDemandPrice == 0 {
		return nil, fmt.Errorf("no pricing found for Memorystore tier %s", tier)
	}
	if commitmentPrice == 0 {
		return nil, fmt.Errorf("no commitment pricing found for Memorystore tier %s in region %s: catalog has no CUD SKU; cannot compute savings percentage", tier, region)
	}

	hoursInTerm := 8760.0 * float64(termYears)
	// Scale the per-hour commitment price to a term total so it is on the
	// same basis as onDemandPrice * hoursInTerm. Without this, the savings
	// percentage would be nearly 100% (per-hour price vs term total).
	commitmentPriceTerm := commitmentPrice * hoursInTerm
	savingsPercentage := calculateSavingsPercentage(onDemandPrice, hoursInTerm, commitmentPriceTerm)

	return &RedisPricing{
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

// extractPricingFromSKUs extracts on-demand and commitment pricing from SKU list.
func extractPricingFromSKUs(skus []*cloudbilling.Sku, tier, region string) (onDemand, commitment float64, currency string, err error) {
	var prices skumatch.SlotPrices
	for _, sku := range skus {
		if !skuMatchesTier(sku, tier, region) {
			continue
		}

		price, curr := extractPriceFromSKU(sku)
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

	return prices.OnDemand, prices.Commitment, currency, nil
}

// extractPriceFromSKU extracts the unit price from a SKU.
func extractPriceFromSKU(sku *cloudbilling.Sku) (price float64, currency string) {
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

// calculateSavingsPercentage calculates the savings percentage.
func calculateSavingsPercentage(onDemandPrice, hoursInTerm, commitmentPrice float64) float64 {
	onDemandTotal := onDemandPrice * hoursInTerm
	return ((onDemandTotal - commitmentPrice) / onDemandTotal) * 100
}

// skuMatchesTier checks if a SKU matches the tier and region.
func skuMatchesTier(sku *cloudbilling.Sku, tier, region string) bool {
	return skumatch.HasToken(sku.Description, tier) && skumatch.InRegion(sku, region)
}

// memorystoreRedisServiceID is the Cloud Billing service for Cloud Memorystore for Redis.
const memorystoreRedisServiceID = "services/5AF5-2C11-D467"

// listSKUs returns the service's SKU catalog, listing it once per client so
// that pricing N recommendations costs one catalog read instead of N.
func (c *Client) listSKUs(svc BillingService) (*cloudbilling.ListSkusResponse, error) {
	c.skuMu.Lock()
	defer c.skuMu.Unlock()
	if c.skuCache != nil {
		return c.skuCache, nil
	}
	skus, err := svc.ListSKUs(memorystoreRedisServiceID)
	if err != nil {
		return nil, err
	}
	c.skuCache = skus
	return skus, nil
}
