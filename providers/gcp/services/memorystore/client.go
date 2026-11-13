// Package memorystore provides GCP Memorystore (Redis) commitments client
package memorystore

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/recommender/apiv1/recommenderpb"
	"cloud.google.com/go/redis/apiv1/redispb"
	gax "github.com/googleapis/gax-go/v2"
	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/option"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/internal/skulist"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/internal/skumatch"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/services/shared"
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
	projectID      string
	region         string
	redisService   RedisService
	billingService BillingService
	clientOpts     []option.ClientOption
}

// NewClient creates a new GCP Memorystore client.
func NewClient(ctx context.Context, projectID, region string, opts ...option.ClientOption) (*Client, error) {
	return &Client{
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

// GetOfferingDetails retrieves Memorystore offering details from GCP Billing API.
func (c *Client) GetOfferingDetails(ctx context.Context, rec common.Recommendation) (*common.OfferingDetails, error) {
	return shared.OfferingDetails(ctx, rec, "gcp-memorystore", c.region, c.getRedisPricing)
}

// RedisPricing contains pricing information for Memorystore Redis.
type RedisPricing = shared.Term

// getRedisPricing gets pricing from GCP Cloud Billing Catalog API.
// It returns an error when commitment pricing is absent from the catalog rather
// than fabricating a price from a hardcoded discount factor (issue #1020).
func (c *Client) getRedisPricing(ctx context.Context, tier, region string, termYears int) (*RedisPricing, error) {
	return shared.Price(ctx, shared.PriceInput{
		Name:    "memorystore",
		Billing: c.billingService,
		Opts:    c.clientOpts,
		List:    c.listSKUs,
		Extract: func(skus []*cloudbilling.Sku) (float64, float64, string, error) {
			return extractPricingFromSKUs(skus, tier, region, termYears)
		},
		Label:     "Memorystore tier " + tier,
		Region:    region,
		TermYears: termYears,
	})
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
