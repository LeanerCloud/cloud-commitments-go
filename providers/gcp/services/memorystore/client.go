// Package memorystore provides GCP Memorystore (Redis) commitments client
package memorystore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/recommender/apiv1"
	"cloud.google.com/go/recommender/apiv1/recommenderpb"
	"cloud.google.com/go/redis/apiv1"
	"cloud.google.com/go/redis/apiv1/redispb"
	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/LeanerCloud/CUDly/pkg/common"
)

// MemorystoreClient handles GCP Memorystore (Redis) commitments
type MemorystoreClient struct {
	ctx        context.Context
	projectID  string
	region     string
	clientOpts []option.ClientOption
}

// NewClient creates a new GCP Memorystore client
func NewClient(ctx context.Context, projectID, region string, opts ...option.ClientOption) (*MemorystoreClient, error) {
	return &MemorystoreClient{
		ctx:        ctx,
		projectID:  projectID,
		region:     region,
		clientOpts: opts,
	}, nil
}

// GetServiceType returns the service type
func (c *MemorystoreClient) GetServiceType() common.ServiceType {
	return common.ServiceCache
}

// GetRegion returns the region
func (c *MemorystoreClient) GetRegion() string {
	return c.region
}

// GetRecommendations gets Memorystore Redis recommendations from GCP Recommender API
func (c *MemorystoreClient) GetRecommendations(ctx context.Context, params common.RecommendationParams) ([]common.Recommendation, error) {
	client, err := recommender.NewClient(ctx, c.clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create recommender client: %w", err)
	}
	defer client.Close()

	recommendations := make([]common.Recommendation, 0)

	// Memorystore Redis recommender (if available)
	parent := fmt.Sprintf("projects/%s/locations/%s/recommenders/google.memorystore.redis.PerformanceRecommender",
		c.projectID, c.region)

	req := &recommenderpb.ListRecommendationsRequest{
		Parent: parent,
	}

	it := client.ListRecommendations(ctx, req)
	for {
		rec, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			break
		}

		converted := c.convertGCPRecommendation(ctx, rec)
		if converted != nil {
			recommendations = append(recommendations, *converted)
		}
	}

	return recommendations, nil
}

// GetExistingCommitments retrieves existing Memorystore Redis commitments
func (c *MemorystoreClient) GetExistingCommitments(ctx context.Context) ([]common.Commitment, error) {
	client, err := redis.NewCloudRedisClient(ctx, c.clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create redis client: %w", err)
	}
	defer client.Close()

	commitments := make([]common.Commitment, 0)

	// List all Redis instances in the project/region
	parent := fmt.Sprintf("projects/%s/locations/%s", c.projectID, c.region)
	req := &redispb.ListInstancesRequest{
		Parent: parent,
	}

	it := client.ListInstances(ctx, req)
	for {
		instance, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to list redis instances: %w", err)
		}

		// Check if instance has committed use pricing
		if instance.ReservedIPRange != "" {
			commitment := common.Commitment{
				Provider:       common.ProviderGCP,
				Account:        c.projectID,
				CommitmentType: common.CommitmentCUD,
				Service:        common.ServiceCache,
				Region:         c.region,
				CommitmentID:   instance.Name,
				State:          strings.ToLower(instance.State.String()),
				ResourceType:   instance.Tier.String(),
			}

			commitments = append(commitments, commitment)
		}
	}

	return commitments, nil
}

// PurchaseCommitment purchases a Memorystore Redis commitment
func (c *MemorystoreClient) PurchaseCommitment(ctx context.Context, rec common.Recommendation) (common.PurchaseResult, error) {
	result := common.PurchaseResult{
		Recommendation: rec,
		DryRun:         false,
		Success:        false,
		Timestamp:      time.Now(),
	}

	client, err := redis.NewCloudRedisClient(ctx, c.clientOpts...)
	if err != nil {
		result.Error = fmt.Errorf("failed to create redis client: %w", err)
		return result, result.Error
	}
	defer client.Close()

	// Create a new Memorystore Redis instance with committed pricing
	instanceName := fmt.Sprintf("redis-committed-%d", time.Now().Unix())
	parent := fmt.Sprintf("projects/%s/locations/%s", c.projectID, c.region)

	instance := &redispb.Instance{
		Name:       fmt.Sprintf("%s/instances/%s", parent, instanceName),
		Tier:       redispb.Instance_STANDARD_HA,
		MemorySizeGb: 1, // Minimum size
		// Setting reserved IP range indicates committed use
		ReservedIPRange: "10.0.0.0/29",
	}

	insertReq := &redispb.CreateInstanceRequest{
		Parent:     parent,
		InstanceId: instanceName,
		Instance:   instance,
	}

	op, err := client.CreateInstance(ctx, insertReq)
	if err != nil {
		result.Error = fmt.Errorf("failed to create redis instance with commitment: %w", err)
		return result, result.Error
	}

	// Wait for operation to complete
	_, err = op.Wait(ctx)
	if err != nil {
		result.Error = fmt.Errorf("instance creation failed: %w", err)
		return result, result.Error
	}

	result.Success = true
	result.CommitmentID = instanceName
	result.Cost = rec.CommitmentCost

	return result, nil
}

// ValidateOffering validates that a Redis tier exists
func (c *MemorystoreClient) ValidateOffering(ctx context.Context, rec common.Recommendation) error {
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

// GetOfferingDetails retrieves Memorystore offering details from GCP Billing API
func (c *MemorystoreClient) GetOfferingDetails(ctx context.Context, rec common.Recommendation) (*common.OfferingDetails, error) {
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

// GetValidResourceTypes returns valid Memorystore tiers
func (c *MemorystoreClient) GetValidResourceTypes(ctx context.Context) ([]string, error) {
	// Memorystore Redis has predefined tiers
	validTiers := []string{
		"BASIC",
		"STANDARD_HA",
	}

	return validTiers, nil
}

// RedisPricing contains pricing information for Memorystore Redis
type RedisPricing struct {
	HourlyRate        float64
	CommitmentPrice   float64
	OnDemandPrice     float64
	Currency          string
	SavingsPercentage float64
}

// getRedisPricing gets pricing from GCP Cloud Billing Catalog API
func (c *MemorystoreClient) getRedisPricing(ctx context.Context, tier, region string, termYears int) (*RedisPricing, error) {
	service, err := cloudbilling.NewService(ctx, c.clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create billing service: %w", err)
	}

	// Memorystore Redis service ID
	serviceID := "services/D559-82DA-3A56"
	skus, err := service.Services.Skus.List(serviceID).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to list SKUs: %w", err)
	}

	var onDemandPrice, commitmentPrice float64
	currency := "USD"

	// Search for pricing for the specific tier and region
	for _, sku := range skus.Skus {
		if !skuMatchesTier(sku, tier, region) {
			continue
		}

		if len(sku.PricingInfo) > 0 {
			pricingInfo := sku.PricingInfo[0]
			if pricingInfo.PricingExpression != nil && len(pricingInfo.PricingExpression.TieredRates) > 0 {
				rate := pricingInfo.PricingExpression.TieredRates[0]
				if rate.UnitPrice != nil {
					price := float64(rate.UnitPrice.Units) + float64(rate.UnitPrice.Nanos)/1e9

					if rate.UnitPrice.CurrencyCode != "" {
						currency = rate.UnitPrice.CurrencyCode
					}

					// Check if this is a commitment or on-demand price
					if strings.Contains(strings.ToLower(sku.Description), "commitment") {
						commitmentPrice = price
					} else {
						onDemandPrice = price
					}
				}
			}
		}
	}

	if onDemandPrice == 0 {
		return nil, fmt.Errorf("no pricing found for Memorystore tier %s", tier)
	}

	hoursInTerm := 8760.0 * float64(termYears)
	// GCP Memorystore commitments typically offer 25-35% savings
	if commitmentPrice == 0 {
		discount := 0.70 // 30% savings
		if termYears == 3 {
			discount = 0.65 // 35% savings
		}
		onDemandTotal := onDemandPrice * hoursInTerm
		commitmentPrice = onDemandTotal * discount
	}

	savingsPercentage := ((onDemandPrice*hoursInTerm - commitmentPrice) / (onDemandPrice * hoursInTerm)) * 100

	return &RedisPricing{
		HourlyRate:        commitmentPrice / hoursInTerm,
		CommitmentPrice:   commitmentPrice,
		OnDemandPrice:     onDemandPrice * hoursInTerm,
		Currency:          currency,
		SavingsPercentage: savingsPercentage,
	}, nil
}

// skuMatchesTier checks if a SKU matches the tier and region
func skuMatchesTier(sku *cloudbilling.Sku, tier, region string) bool {
	// Check if the SKU description contains the tier
	if !strings.Contains(strings.ToLower(sku.Description), strings.ToLower(tier)) {
		return false
	}

	// Check if the SKU is available in the region
	if sku.ServiceRegions != nil {
		for _, serviceRegion := range sku.ServiceRegions {
			if strings.EqualFold(serviceRegion, region) {
				return true
			}
		}
		return false
	}

	return true
}

// convertGCPRecommendation converts a GCP Recommender recommendation to common format
func (c *MemorystoreClient) convertGCPRecommendation(ctx context.Context, gcpRec *recommenderpb.Recommendation) *common.Recommendation {
	rec := &common.Recommendation{
		Provider:       common.ProviderGCP,
		Service:        common.ServiceCache,
		Account:        c.projectID,
		Region:         c.region,
		CommitmentType: common.CommitmentCUD,
		Timestamp:      time.Now(),
		Term:           "1yr",
		PaymentOption:  "monthly",
	}

	// Extract resource type from recommendation content
	if gcpRec.Content != nil {
		if gcpRec.Content.OperationGroups != nil {
			for _, opGroup := range gcpRec.Content.OperationGroups {
				for _, op := range opGroup.Operations {
					if op.Resource != "" {
						parts := strings.Split(op.Resource, "/")
						if len(parts) > 0 {
							rec.ResourceType = parts[len(parts)-1]
						}
					}
				}
			}
		}
	}

	// Extract cost impact
	if gcpRec.PrimaryImpact != nil {
		// Use GetCostProjection() method to access the cost projection
		if costProj := gcpRec.PrimaryImpact.GetCostProjection(); costProj != nil && costProj.Cost != nil {
			cost := costProj.Cost
			savings := -(float64(cost.Units) + float64(cost.Nanos)/1e9)
			rec.EstimatedSavings = savings
		}
	}

	return rec
}
