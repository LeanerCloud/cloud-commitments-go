// Package savingsplans provides AWS Savings Plans purchase client
package savingsplans

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/savingsplans"
	"github.com/aws/aws-sdk-go-v2/service/savingsplans/types"

	internalCommon "github.com/LeanerCloud/CUDly/internal/common"
)

// SavingsPlansAPI defines the interface for Savings Plans operations
type SavingsPlansAPI interface {
	CreateSavingsPlan(ctx context.Context, params *savingsplans.CreateSavingsPlanInput, optFns ...func(*savingsplans.Options)) (*savingsplans.CreateSavingsPlanOutput, error)
	DescribeSavingsPlans(ctx context.Context, params *savingsplans.DescribeSavingsPlansInput, optFns ...func(*savingsplans.Options)) (*savingsplans.DescribeSavingsPlansOutput, error)
	DescribeSavingsPlansOfferings(ctx context.Context, params *savingsplans.DescribeSavingsPlansOfferingsInput, optFns ...func(*savingsplans.Options)) (*savingsplans.DescribeSavingsPlansOfferingsOutput, error)
	DescribeSavingsPlansOfferingRates(ctx context.Context, params *savingsplans.DescribeSavingsPlansOfferingRatesInput, optFns ...func(*savingsplans.Options)) (*savingsplans.DescribeSavingsPlansOfferingRatesOutput, error)
}

// PurchaseClient wraps the AWS Savings Plans client
type PurchaseClient struct {
	client SavingsPlansAPI
	internalCommon.BasePurchaseClient
}

// NewPurchaseClient creates a new Savings Plans purchase client
func NewPurchaseClient(cfg aws.Config) *PurchaseClient {
	return &PurchaseClient{
		client: savingsplans.NewFromConfig(cfg),
		BasePurchaseClient: internalCommon.BasePurchaseClient{
			Region: cfg.Region,
		},
	}
}

// PurchaseRI attempts to purchase a Savings Plan (implements the PurchaseClient interface)
func (c *PurchaseClient) PurchaseRI(ctx context.Context, rec internalCommon.Recommendation) internalCommon.PurchaseResult {
	result := internalCommon.PurchaseResult{
		Config:    rec,
		Timestamp: time.Now(),
	}

	// Validate it's a Savings Plans recommendation
	if rec.Service != internalCommon.ServiceSavingsPlans {
		result.Success = false
		result.Message = "Invalid service type for Savings Plans purchase"
		return result
	}

	spDetails, ok := rec.ServiceDetails.(*internalCommon.SavingsPlanDetails)
	if !ok {
		result.Success = false
		result.Message = "Invalid service details for Savings Plans"
		return result
	}

	// Find the offering ID
	offeringID, err := c.findOfferingID(ctx, rec)
	if err != nil {
		result.Success = false
		result.Message = fmt.Sprintf("Failed to find Savings Plans offering: %v", err)
		return result
	}

	// Create the Savings Plan purchase request
	input := &savingsplans.CreateSavingsPlanInput{
		SavingsPlanOfferingId: aws.String(offeringID),
		Commitment:            aws.String(fmt.Sprintf("%.2f", spDetails.HourlyCommitment)),
		UpfrontPaymentAmount:  nil, // AWS calculates this based on payment option
		PurchaseTime:          aws.Time(time.Now()),
	}

	// Execute the purchase
	response, err := c.client.CreateSavingsPlan(ctx, input)
	if err != nil {
		result.Success = false
		result.Message = fmt.Sprintf("Failed to purchase Savings Plan: %v", err)
		return result
	}

	// Extract purchase information
	if response.SavingsPlanId != nil {
		result.Success = true
		result.PurchaseID = *response.SavingsPlanId
		result.ReservationID = *response.SavingsPlanId
		result.Message = fmt.Sprintf("Successfully purchased Savings Plan with commitment $%.2f/hour", spDetails.HourlyCommitment)
	} else {
		result.Success = false
		result.Message = "Purchase response was empty"
	}

	return result
}

// findOfferingID finds the appropriate Savings Plans offering ID
func (c *PurchaseClient) findOfferingID(ctx context.Context, rec internalCommon.Recommendation) (string, error) {
	spDetails, ok := rec.ServiceDetails.(*internalCommon.SavingsPlanDetails)
	if !ok {
		return "", fmt.Errorf("invalid service details for Savings Plans")
	}

	// Convert plan type
	var planType types.SavingsPlanType
	switch spDetails.PlanType {
	case "Compute":
		planType = types.SavingsPlanTypeCompute
	case "EC2Instance":
		planType = types.SavingsPlanTypeEc2Instance
	case "SageMaker", "Sagemaker":
		planType = types.SavingsPlanTypeSagemaker
	default:
		return "", fmt.Errorf("unsupported Savings Plan type: %s", spDetails.PlanType)
	}

	// Convert term to months (Term is in months in the internal struct)
	termMonths := int64(12)
	if rec.Term >= 36 {
		termMonths = 36
	}

	// Convert payment option
	paymentOption := types.SavingsPlanPaymentOptionAllUpfront
	switch rec.PaymentType {
	case "All Upfront", "all-upfront":
		paymentOption = types.SavingsPlanPaymentOptionAllUpfront
	case "Partial Upfront", "partial-upfront":
		paymentOption = types.SavingsPlanPaymentOptionPartialUpfront
	case "No Upfront", "no-upfront":
		paymentOption = types.SavingsPlanPaymentOptionNoUpfront
	}

	// Search for offerings
	input := &savingsplans.DescribeSavingsPlansOfferingsInput{
		PlanTypes:      []types.SavingsPlanType{planType},
		Durations:      []int64{termMonths},
		PaymentOptions: []types.SavingsPlanPaymentOption{paymentOption},
	}

	result, err := c.client.DescribeSavingsPlansOfferings(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to describe Savings Plans offerings: %w", err)
	}

	if len(result.SearchResults) == 0 {
		return "", fmt.Errorf("no Savings Plans offerings found matching criteria")
	}

	// Return the first matching offering
	return *result.SearchResults[0].OfferingId, nil
}

// ValidateOffering checks if a Savings Plans offering exists
func (c *PurchaseClient) ValidateOffering(ctx context.Context, rec internalCommon.Recommendation) error {
	_, err := c.findOfferingID(ctx, rec)
	return err
}

// GetOfferingDetails retrieves detailed information about a Savings Plans offering
func (c *PurchaseClient) GetOfferingDetails(ctx context.Context, rec internalCommon.Recommendation) (*internalCommon.OfferingDetails, error) {
	offeringID, err := c.findOfferingID(ctx, rec)
	if err != nil {
		return nil, err
	}

	spDetails, ok := rec.ServiceDetails.(*internalCommon.SavingsPlanDetails)
	if !ok {
		return nil, fmt.Errorf("invalid service details for Savings Plans")
	}

	// Get offering rates
	input := &savingsplans.DescribeSavingsPlansOfferingRatesInput{
		SavingsPlanOfferingIds: []string{offeringID},
	}

	_, err = c.client.DescribeSavingsPlansOfferingRates(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to get offering rates: %w", err)
	}

	// Calculate costs based on payment option
	var upfrontCost, recurringCost, totalCost float64

	// Total cost is hourly commitment * hours in term (Term is in months)
	hoursInTerm := 8760.0 // 1 year (12 months)
	if rec.Term >= 36 {
		hoursInTerm = 26280.0 // 3 years (36 months)
	}
	totalCost = spDetails.HourlyCommitment * hoursInTerm

	switch rec.PaymentType {
	case "All Upfront", "all-upfront":
		upfrontCost = totalCost
		recurringCost = 0
	case "Partial Upfront", "partial-upfront":
		upfrontCost = totalCost * 0.5 // Approximation
		recurringCost = (totalCost * 0.5) / hoursInTerm
	case "No Upfront", "no-upfront":
		upfrontCost = 0
		recurringCost = totalCost / hoursInTerm
	}

	// Convert term months to string
	termStr := "1yr"
	if rec.Term >= 36 {
		termStr = "3yr"
	}

	return &internalCommon.OfferingDetails{
		OfferingID:          offeringID,
		InstanceType:        spDetails.PlanType,
		Term:                termStr,
		PaymentOption:       rec.PaymentType,
		UpfrontCost:         upfrontCost,
		RecurringCost:       recurringCost,
		TotalCost:           totalCost,
		EffectiveHourlyRate: spDetails.HourlyCommitment,
		Currency:            "USD",
	}, nil
}

// BatchPurchase purchases multiple Savings Plans with rate limiting
func (c *PurchaseClient) BatchPurchase(ctx context.Context, recommendations []internalCommon.Recommendation, delayBetweenPurchases time.Duration) []internalCommon.PurchaseResult {
	return c.BasePurchaseClient.BatchPurchase(ctx, c, recommendations, delayBetweenPurchases)
}

// GetExistingReservedInstances retrieves existing Savings Plans (implements PurchaseClient interface)
func (c *PurchaseClient) GetExistingReservedInstances(ctx context.Context) ([]internalCommon.ExistingRI, error) {
	// List all Savings Plans
	input := &savingsplans.DescribeSavingsPlansInput{
		States: []types.SavingsPlanState{
			types.SavingsPlanStateActive,
			types.SavingsPlanStatePendingReturn,
			types.SavingsPlanStateQueued,
		},
	}

	result, err := c.client.DescribeSavingsPlans(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to describe Savings Plans: %w", err)
	}

	existingPlans := make([]internalCommon.ExistingRI, 0, len(result.SavingsPlans))

	for _, sp := range result.SavingsPlans {
		if sp.SavingsPlanId == nil {
			continue
		}

		existingPlan := internalCommon.ExistingRI{
			ReservationID: *sp.SavingsPlanId,
			Service:       internalCommon.ServiceSavingsPlans,
			Region:        aws.ToString(sp.Region),
			InstanceType:  string(sp.SavingsPlanType),
			Count:         1, // Savings Plans don't have a count
			State:         string(sp.State),
		}

		if sp.Start != nil {
			if startTime, err := time.Parse(time.RFC3339, *sp.Start); err == nil {
				existingPlan.StartDate = startTime
			}
		}
		if sp.End != nil {
			if endTime, err := time.Parse(time.RFC3339, *sp.End); err == nil {
				existingPlan.EndDate = endTime
			}
		}

		existingPlans = append(existingPlans, existingPlan)
	}

	return existingPlans, nil
}

// GetValidInstanceTypes returns valid Savings Plan types
func (c *PurchaseClient) GetValidInstanceTypes(ctx context.Context) ([]string, error) {
	return []string{
		"Compute",
		"EC2Instance",
		"SageMaker",
	}, nil
}
