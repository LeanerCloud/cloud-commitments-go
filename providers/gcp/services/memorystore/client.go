// Package memorystore provides GCP Memorystore (Redis) commitments client
package memorystore

import (
	"context"
	"fmt"
	"slices"
	"time"

	"cloud.google.com/go/recommender/apiv1/recommenderpb"
	"cloud.google.com/go/redis/apiv1/redispb"
	gax "github.com/googleapis/gax-go/v2"
	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/option"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/internal/skulist"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/internal/skumatch"
)

// BillingService lists SKUs of one Cloud Billing service (enables mocking).
type BillingService = skulist.BillingService

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
	skus           skulist.Cache
	ctx            context.Context
	projectID      string
	region         string
	redisService   RedisService
	billingService BillingService
	clientOpts     []option.ClientOption
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

	if slices.Contains(validTiers, rec.ResourceType) {
		return nil
	}

	return fmt.Errorf("invalid Memorystore tier: %s", rec.ResourceType)
}

// GetOfferingDetails retrieves Memorystore offering details from GCP Billing API.
func (c *Client) GetOfferingDetails(ctx context.Context, rec common.Recommendation) (*common.OfferingDetails, error) {
	termYears, err := skumatch.TermYears(rec.Term)
	if err != nil {
		return nil, err
	}

	pricing, err := c.getRedisPricing(ctx, rec.ResourceType, c.region, termYears)
	if err != nil {
		return nil, fmt.Errorf("failed to get pricing: %w", err)
	}

	offeringID := fmt.Sprintf("gcp-memorystore-%s-%s-%s", rec.ResourceType, c.region, rec.Term)
	return skumatch.OfferingDetails(rec, offeringID, termYears, pricing.CommitmentPrice, pricing.HourlyRate, pricing.Currency), nil
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
	billingSvc, err := skulist.Resolve(ctx, c.billingService, "memorystore", c.clientOpts...)
	if err != nil {
		return nil, err
	}

	skus, err := c.listSKUs(billingSvc)
	if err != nil {
		return nil, fmt.Errorf("failed to list SKUs: %w", err)
	}

	onDemandPrice, commitmentPrice, currency, err := extractPricingFromSKUs(skus.Skus, tier, region, termYears)
	if err != nil {
		return nil, err
	}
	prices, err := skumatch.ScaleToTerm("Memorystore tier "+tier, region, onDemandPrice, commitmentPrice, termYears)
	if err != nil {
		return nil, err
	}

	return &RedisPricing{
		HourlyRate:        prices.HourlyRate,
		CommitmentPrice:   prices.CommitmentPrice,
		OnDemandPrice:     prices.OnDemandPrice,
		Currency:          currency,
		SavingsPercentage: prices.SavingsPercentage,
	}, nil
}

// extractPricingFromSKUs extracts on-demand and commitment pricing from SKU list.
func extractPricingFromSKUs(skus []*cloudbilling.Sku, tier, region string, termYears int) (onDemand, commitment float64, currency string, err error) {
	return skumatch.ExtractPricing(skus, func(sku *cloudbilling.Sku) bool {
		return skumatch.MatchesTier(sku, tier, region)
	}, skumatch.HourlyUnitPrice, termYears)
}

// memorystoreRedisServiceID is the Cloud Billing service for Cloud Memorystore for Redis.
const memorystoreRedisServiceID = "services/5AF5-2C11-D467"

// listSKUs returns the service's SKU catalog, listing it once per client so
// that pricing N recommendations costs one catalog read instead of N.
func (c *Client) listSKUs(svc BillingService) (*cloudbilling.ListSkusResponse, error) {
	return c.skus.Get(svc, memorystoreRedisServiceID)
}
