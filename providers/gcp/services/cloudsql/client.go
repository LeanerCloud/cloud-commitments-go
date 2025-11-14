// Package cloudsql provides GCP Cloud SQL commitments client
package cloudsql

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/recommender/apiv1"
	"cloud.google.com/go/recommender/apiv1/recommenderpb"
	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/api/sqladmin/v1"

	"github.com/LeanerCloud/CUDly/pkg/common"
)

// CloudSQLClient handles GCP Cloud SQL commitments
type CloudSQLClient struct {
	ctx        context.Context
	projectID  string
	region     string
	clientOpts []option.ClientOption
}

// NewClient creates a new GCP Cloud SQL client
func NewClient(ctx context.Context, projectID, region string, opts ...option.ClientOption) (*CloudSQLClient, error) {
	return &CloudSQLClient{
		ctx:        ctx,
		projectID:  projectID,
		region:     region,
		clientOpts: opts,
	}, nil
}

// GetServiceType returns the service type
func (c *CloudSQLClient) GetServiceType() common.ServiceType {
	return common.ServiceRelationalDB
}

// GetRegion returns the region
func (c *CloudSQLClient) GetRegion() string {
	return c.region
}

// GetRecommendations gets Cloud SQL recommendations from GCP Recommender API
func (c *CloudSQLClient) GetRecommendations(ctx context.Context, params common.RecommendationParams) ([]common.Recommendation, error) {
	client, err := recommender.NewClient(ctx, c.clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create recommender client: %w", err)
	}
	defer client.Close()

	recommendations := make([]common.Recommendation, 0)

	// Cloud SQL commitment recommender
	parent := fmt.Sprintf("projects/%s/locations/%s/recommenders/google.cloudsql.instance.PerformanceRecommender",
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

// GetExistingCommitments retrieves existing Cloud SQL commitments
func (c *CloudSQLClient) GetExistingCommitments(ctx context.Context) ([]common.Commitment, error) {
	service, err := sqladmin.NewService(ctx, c.clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create SQL admin service: %w", err)
	}

	commitments := make([]common.Commitment, 0)

	// List all SQL instances in the project
	instancesCall := service.Instances.List(c.projectID)
	instances, err := instancesCall.Do()
	if err != nil {
		return nil, fmt.Errorf("failed to list SQL instances: %w", err)
	}

	for _, instance := range instances.Items {
		// Check if instance has a commitment (long-term pricing plan)
		if instance.Settings != nil && instance.Settings.PricingPlan == "PACKAGE" {
			commitment := common.Commitment{
				Provider:       common.ProviderGCP,
				Account:        c.projectID,
				CommitmentType: common.CommitmentCUD,
				Service:        common.ServiceRelationalDB,
				Region:         instance.Region,
				CommitmentID:   instance.Name,
				State:          strings.ToLower(instance.State),
				ResourceType:   instance.DatabaseVersion,
			}

			// Extract tier (machine type)
			if instance.Settings.Tier != "" {
				commitment.ResourceType = instance.Settings.Tier
			}

			commitments = append(commitments, commitment)
		}
	}

	return commitments, nil
}

// PurchaseCommitment purchases a Cloud SQL commitment
func (c *CloudSQLClient) PurchaseCommitment(ctx context.Context, rec common.Recommendation) (common.PurchaseResult, error) {
	result := common.PurchaseResult{
		Recommendation: rec,
		DryRun:         false,
		Success:        false,
		Timestamp:      time.Now(),
	}

	service, err := sqladmin.NewService(ctx, c.clientOpts...)
	if err != nil {
		result.Error = fmt.Errorf("failed to create SQL admin service: %w", err)
		return result, result.Error
	}

	// Create a new Cloud SQL instance with commitment pricing
	instanceName := fmt.Sprintf("sql-committed-%d", time.Now().Unix())

	instance := &sqladmin.DatabaseInstance{
		Name:            instanceName,
		Region:          c.region,
		DatabaseVersion: "MYSQL_8_0", // Default to MySQL 8.0
		Settings: &sqladmin.Settings{
			Tier:        rec.ResourceType,
			PricingPlan: "PACKAGE", // This indicates a commitment
		},
	}

	insertCall := service.Instances.Insert(c.projectID, instance)
	op, err := insertCall.Do()
	if err != nil {
		result.Error = fmt.Errorf("failed to create SQL instance with commitment: %w", err)
		return result, result.Error
	}

	// Wait for operation to complete (in production, you'd poll this)
	if op.Status != "DONE" {
		result.Error = fmt.Errorf("instance creation in progress: %s", op.Status)
		return result, result.Error
	}

	result.Success = true
	result.CommitmentID = instanceName
	result.Cost = rec.CommitmentCost

	return result, nil
}

// ValidateOffering validates that a Cloud SQL tier exists
func (c *CloudSQLClient) ValidateOffering(ctx context.Context, rec common.Recommendation) error {
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

// GetOfferingDetails retrieves Cloud SQL offering details from GCP Billing API
func (c *CloudSQLClient) GetOfferingDetails(ctx context.Context, rec common.Recommendation) (*common.OfferingDetails, error) {
	termYears := 1
	if rec.Term == "3yr" || rec.Term == "3" {
		termYears = 3
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

// GetValidResourceTypes returns valid Cloud SQL tiers
func (c *CloudSQLClient) GetValidResourceTypes(ctx context.Context) ([]string, error) {
	service, err := sqladmin.NewService(ctx, c.clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create SQL admin service: %w", err)
	}

	// List available tiers for the region
	tiersCall := service.Tiers.List(c.projectID)
	tiers, err := tiersCall.Do()
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

// SQLPricing contains pricing information for Cloud SQL
type SQLPricing struct {
	HourlyRate        float64
	CommitmentPrice   float64
	OnDemandPrice     float64
	Currency          string
	SavingsPercentage float64
}

// getSQLPricing gets pricing from GCP Cloud Billing Catalog API
func (c *CloudSQLClient) getSQLPricing(ctx context.Context, tier, region string, termYears int) (*SQLPricing, error) {
	service, err := cloudbilling.NewService(ctx, c.clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create billing service: %w", err)
	}

	// Cloud SQL service ID
	serviceID := "services/9662-B51E-5089"
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

					// Cloud SQL doesn't have separate commitment pricing in the API
					// The package plan is billed differently
					onDemandPrice = price
				}
			}
		}
	}

	if onDemandPrice == 0 {
		return nil, fmt.Errorf("no pricing found for Cloud SQL tier %s", tier)
	}

	hoursInTerm := 8760.0 * float64(termYears)
	// Cloud SQL package plans typically offer 15-20% savings
	discount := 0.85 // 15% savings
	if termYears == 3 {
		discount = 0.80 // 20% savings
	}

	onDemandTotal := onDemandPrice * hoursInTerm
	commitmentPrice = onDemandTotal * discount

	savingsPercentage := ((onDemandTotal - commitmentPrice) / onDemandTotal) * 100

	return &SQLPricing{
		HourlyRate:        commitmentPrice / hoursInTerm,
		CommitmentPrice:   commitmentPrice,
		OnDemandPrice:     onDemandTotal,
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
func (c *CloudSQLClient) convertGCPRecommendation(ctx context.Context, gcpRec *recommenderpb.Recommendation) *common.Recommendation {
	rec := &common.Recommendation{
		Provider:       common.ProviderGCP,
		Service:        common.ServiceRelationalDB,
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

// contains checks if a slice contains a string
func contains(slice []string, str string) bool {
	for _, s := range slice {
		if strings.EqualFold(s, str) {
			return true
		}
	}
	return false
}
