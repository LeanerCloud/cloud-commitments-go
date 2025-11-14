// Package computeengine provides GCP Compute Engine Committed Use Discounts client
package computeengine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"cloud.google.com/go/recommender/apiv1"
	"cloud.google.com/go/recommender/apiv1/recommenderpb"
	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/LeanerCloud/CUDly/pkg/common"
)

// ComputeEngineClient handles GCP Compute Engine Committed Use Discounts
type ComputeEngineClient struct {
	ctx        context.Context
	projectID  string
	region     string
	clientOpts []option.ClientOption
}

// NewClient creates a new GCP Compute Engine client
func NewClient(ctx context.Context, projectID, region string, opts ...option.ClientOption) (*ComputeEngineClient, error) {
	return &ComputeEngineClient{
		ctx:        ctx,
		projectID:  projectID,
		region:     region,
		clientOpts: opts,
	}, nil
}

// GetServiceType returns the service type
func (c *ComputeEngineClient) GetServiceType() common.ServiceType {
	return common.ServiceCompute
}

// GetRegion returns the region
func (c *ComputeEngineClient) GetRegion() string {
	return c.region
}

// GetRecommendations gets CUD recommendations from GCP Recommender API
func (c *ComputeEngineClient) GetRecommendations(ctx context.Context, params common.RecommendationParams) ([]common.Recommendation, error) {
	client, err := recommender.NewClient(ctx, c.clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create recommender client: %w", err)
	}
	defer client.Close()

	recommendations := make([]common.Recommendation, 0)

	// Recommender name for Compute Engine CUD recommendations
	parent := fmt.Sprintf("projects/%s/locations/%s/recommenders/google.compute.commitment.UsageCommitmentRecommender",
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
			// If recommender API fails, continue with empty recommendations
			break
		}

		converted := c.convertGCPRecommendation(ctx, rec)
		if converted != nil {
			recommendations = append(recommendations, *converted)
		}
	}

	return recommendations, nil
}

// GetExistingCommitments retrieves existing Compute Engine CUDs
func (c *ComputeEngineClient) GetExistingCommitments(ctx context.Context) ([]common.Commitment, error) {
	client, err := compute.NewRegionCommitmentsRESTClient(ctx, c.clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create commitments client: %w", err)
	}
	defer client.Close()

	commitments := make([]common.Commitment, 0)

	req := &computepb.ListRegionCommitmentsRequest{
		Project: c.projectID,
		Region:  c.region,
	}

	it := client.List(ctx, req)
	for {
		commitment, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to list commitments: %w", err)
		}

		if commitment.Name == nil {
			continue
		}

		status := "unknown"
		if commitment.Status != nil {
			status = strings.ToLower(*commitment.Status)
		}

		commitmentType := common.CommitmentCUD
		if commitment.Type != nil && *commitment.Type == "GENERAL_PURPOSE" {
			commitmentType = common.CommitmentCUD
		}

		com := common.Commitment{
			Provider:       common.ProviderGCP,
			Account:        c.projectID,
			CommitmentType: commitmentType,
			Service:        common.ServiceCompute,
			Region:         c.region,
			CommitmentID:   *commitment.Name,
			State:          status,
		}

		// Extract resource type from commitment resources
		if len(commitment.Resources) > 0 {
			resource := commitment.Resources[0]
			if resource.Type != nil {
				com.ResourceType = *resource.Type
			}
		}

		commitments = append(commitments, com)
	}

	return commitments, nil
}

// PurchaseCommitment purchases a Compute Engine CUD
func (c *ComputeEngineClient) PurchaseCommitment(ctx context.Context, rec common.Recommendation) (common.PurchaseResult, error) {
	result := common.PurchaseResult{
		Recommendation: rec,
		DryRun:         false,
		Success:        false,
		Timestamp:      time.Now(),
	}

	client, err := compute.NewRegionCommitmentsRESTClient(ctx, c.clientOpts...)
	if err != nil {
		result.Error = fmt.Errorf("failed to create commitments client: %w", err)
		return result, result.Error
	}
	defer client.Close()

	// Determine plan based on term
	plan := "TWELVE_MONTH"
	if rec.Term == "3yr" || rec.Term == "3" {
		plan = "THIRTY_SIX_MONTH"
	}

	// Create commitment request
	commitment := &computepb.Commitment{
		Name:        stringPtr(fmt.Sprintf("cud-%d", time.Now().Unix())),
		Plan:        stringPtr(plan),
		Type:        stringPtr("GENERAL_PURPOSE"),
		Description: stringPtr(fmt.Sprintf("CUD for %s", rec.ResourceType)),
		Resources: []*computepb.ResourceCommitment{
			{
				Type:   stringPtr(rec.ResourceType),
				Amount: int64Ptr(int64(rec.Count)),
			},
		},
	}

	req := &computepb.InsertRegionCommitmentRequest{
		Project:             c.projectID,
		Region:              c.region,
		CommitmentResource:  commitment,
	}

	op, err := client.Insert(ctx, req)
	if err != nil {
		result.Error = fmt.Errorf("failed to create commitment: %w", err)
		return result, result.Error
	}

	// Wait for operation to complete
	if err := op.Wait(ctx); err != nil {
		result.Error = fmt.Errorf("commitment creation failed: %w", err)
		return result, result.Error
	}

	result.Success = true
	result.CommitmentID = *commitment.Name
	result.Cost = rec.CommitmentCost

	return result, nil
}

// ValidateOffering validates that a machine type exists
func (c *ComputeEngineClient) ValidateOffering(ctx context.Context, rec common.Recommendation) error {
	validTypes, err := c.GetValidResourceTypes(ctx)
	if err != nil {
		return fmt.Errorf("failed to get valid machine types: %w", err)
	}

	for _, machineType := range validTypes {
		if machineType == rec.ResourceType {
			return nil
		}
	}

	return fmt.Errorf("invalid GCP machine type: %s", rec.ResourceType)
}

// GetOfferingDetails retrieves CUD offering details from GCP Billing API
func (c *ComputeEngineClient) GetOfferingDetails(ctx context.Context, rec common.Recommendation) (*common.OfferingDetails, error) {
	termYears := 1
	if rec.Term == "3yr" || rec.Term == "3" {
		termYears = 3
	}

	pricing, err := c.getComputePricing(ctx, rec.ResourceType, c.region, termYears)
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
		OfferingID:          fmt.Sprintf("gcp-compute-%s-%s-%s", rec.ResourceType, c.region, rec.Term),
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

// GetValidResourceTypes returns valid machine types from GCP Compute API
func (c *ComputeEngineClient) GetValidResourceTypes(ctx context.Context) ([]string, error) {
	client, err := compute.NewMachineTypesRESTClient(ctx, c.clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create machine types client: %w", err)
	}
	defer client.Close()

	req := &computepb.ListMachineTypesRequest{
		Project: c.projectID,
		Zone:    c.region + "-a", // Use zone a for the region
	}

	machineTypes := make([]string, 0)
	it := client.List(ctx, req)

	for {
		machineType, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to list machine types: %w", err)
		}

		if machineType.Name != nil {
			machineTypes = append(machineTypes, *machineType.Name)
		}
	}

	if len(machineTypes) == 0 {
		return nil, fmt.Errorf("no machine types found for region %s", c.region)
	}

	return machineTypes, nil
}

// ComputePricing contains pricing information for Compute Engine
type ComputePricing struct {
	HourlyRate        float64
	CommitmentPrice   float64
	OnDemandPrice     float64
	Currency          string
	SavingsPercentage float64
}

// getComputePricing gets pricing from GCP Cloud Billing Catalog API
func (c *ComputeEngineClient) getComputePricing(ctx context.Context, machineType, region string, termYears int) (*ComputePricing, error) {
	// Use Cloud Billing Catalog API to get pricing
	service, err := cloudbilling.NewService(ctx, c.clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create billing service: %w", err)
	}

	// List SKUs for Compute Engine
	skus, err := service.Services.Skus.List("services/6F81-5844-456A").Do()
	if err != nil {
		return nil, fmt.Errorf("failed to list SKUs: %w", err)
	}

	var onDemandPrice, commitmentPrice float64
	currency := "USD"

	// Search for pricing for the specific machine type and region
	for _, sku := range skus.Skus {
		// Check if this SKU matches our machine type and region
		if !skuMatchesMachineType(sku, machineType, region) {
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

	// If we couldn't find specific prices, estimate based on typical GCP CUD discounts
	if onDemandPrice == 0 {
		return nil, fmt.Errorf("no on-demand pricing found for machine type %s", machineType)
	}

	hoursInTerm := 8760.0 * float64(termYears)
	if commitmentPrice == 0 {
		// GCP Compute CUDs typically offer 37% discount for 1-year, 55% for 3-year
		discount := 0.63 // 37% savings
		if termYears == 3 {
			discount = 0.45 // 55% savings
		}
		onDemandTotal := onDemandPrice * hoursInTerm
		commitmentPrice = onDemandTotal * discount
	}

	savingsPercentage := ((onDemandPrice*hoursInTerm - commitmentPrice) / (onDemandPrice * hoursInTerm)) * 100

	return &ComputePricing{
		HourlyRate:        commitmentPrice / hoursInTerm,
		CommitmentPrice:   commitmentPrice,
		OnDemandPrice:     onDemandPrice * hoursInTerm,
		Currency:          currency,
		SavingsPercentage: savingsPercentage,
	}, nil
}

// skuMatchesMachineType checks if a SKU matches the machine type and region
func skuMatchesMachineType(sku *cloudbilling.Sku, machineType, region string) bool {
	// Check if the SKU description contains the machine type
	if !strings.Contains(strings.ToLower(sku.Description), strings.ToLower(machineType)) {
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
func (c *ComputeEngineClient) convertGCPRecommendation(ctx context.Context, gcpRec *recommenderpb.Recommendation) *common.Recommendation {
	rec := &common.Recommendation{
		Provider:       common.ProviderGCP,
		Service:        common.ServiceCompute,
		Account:        c.projectID,
		Region:         c.region,
		CommitmentType: common.CommitmentCUD,
		Timestamp:      time.Now(),
		Term:           "1yr",
		PaymentOption:  "upfront",
	}

	// Extract resource type and cost savings from recommendation content
	if gcpRec.Content != nil {
		if gcpRec.Content.OperationGroups != nil {
			for _, opGroup := range gcpRec.Content.OperationGroups {
				for _, op := range opGroup.Operations {
					if op.Resource != "" {
						// Extract machine type from resource path
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
			// Cost savings is negative of cost projection
			cost := costProj.Cost
			if cost.Units != 0 || cost.Nanos != 0 {
				savings := -(float64(cost.Units) + float64(cost.Nanos)/1e9)
				rec.EstimatedSavings = savings
			}
		}
	}

	return rec
}

// Helper functions
func stringPtr(s string) *string {
	return &s
}

func int64Ptr(i int64) *int64 {
	return &i
}
