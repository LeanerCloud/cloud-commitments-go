package redshift

import (
	"context"
	"fmt"
	"time"

	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/redshift"
	"github.com/aws/aws-sdk-go-v2/service/redshift/types"
)

// PurchaseClient wraps the AWS Redshift client for purchasing Reserved Nodes
type PurchaseClient struct {
	client *redshift.Client
	common.BasePurchaseClient
}

// NewPurchaseClient creates a new Redshift purchase client
func NewPurchaseClient(cfg aws.Config) *PurchaseClient {
	return &PurchaseClient{
		client: redshift.NewFromConfig(cfg),
		BasePurchaseClient: common.BasePurchaseClient{
			Region: cfg.Region,
		},
	}
}

// PurchaseRI attempts to purchase a Redshift Reserved Node based on the recommendation
func (c *PurchaseClient) PurchaseRI(ctx context.Context, rec common.Recommendation) common.PurchaseResult {
	result := common.PurchaseResult{
		Config:    rec,
		Timestamp: time.Now(),
	}

	// Validate it's a Redshift recommendation
	if rec.Service != common.ServiceRedshift {
		result.Success = false
		result.Message = "Invalid service type for Redshift purchase"
		return result
	}

	// Find the offering ID
	offeringID, err := c.findOfferingID(ctx, rec)
	if err != nil {
		result.Success = false
		result.Message = fmt.Sprintf("Failed to find offering: %v", err)
		return result
	}

	rsDetails, ok := rec.ServiceDetails.(*common.RedshiftDetails)
	if !ok {
		result.Success = false
		result.Message = "Invalid service details for Redshift"
		return result
	}

	// Create the purchase request
	input := &redshift.PurchaseReservedNodeOfferingInput{
		ReservedNodeOfferingId: aws.String(offeringID),
		NodeCount:              aws.Int32(rsDetails.NumberOfNodes),
	}

	// Execute the purchase
	response, err := c.client.PurchaseReservedNodeOffering(ctx, input)
	if err != nil {
		result.Success = false
		result.Message = fmt.Sprintf("Failed to purchase Redshift Reserved Node: %v", err)
		return result
	}

	// Extract purchase information
	if response.ReservedNode != nil {
		result.Success = true
		result.PurchaseID = aws.ToString(response.ReservedNode.ReservedNodeId)
		result.ReservationID = aws.ToString(response.ReservedNode.ReservedNodeOfferingId)
		result.Message = fmt.Sprintf("Successfully purchased %d Redshift nodes", rsDetails.NumberOfNodes)

		// Extract cost information if available
		if response.ReservedNode.FixedPrice != nil {
			result.ActualCost = *response.ReservedNode.FixedPrice
		}
	} else {
		result.Success = false
		result.Message = "Purchase response was empty"
	}

	return result
}

// findOfferingID finds the appropriate Reserved Node offering ID
func (c *PurchaseClient) findOfferingID(ctx context.Context, rec common.Recommendation) (string, error) {
	rsDetails, ok := rec.ServiceDetails.(*common.RedshiftDetails)
	if !ok {
		return "", fmt.Errorf("invalid service details for Redshift")
	}

	// Get offerings for the node type
	input := &redshift.DescribeReservedNodeOfferingsInput{
		MaxRecords: aws.Int32(100),
	}

	result, err := c.client.DescribeReservedNodeOfferings(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to describe offerings: %w", err)
	}

	// Find matching offering
	for _, offering := range result.ReservedNodeOfferings {
		if offering.NodeType != nil && *offering.NodeType == rsDetails.NodeType {
			// Check if duration and payment match
			if c.matchesDuration(offering.Duration, rec.Term) &&
				c.matchesOfferingType(string(offering.ReservedNodeOfferingType), rec.PaymentOption) {
				return aws.ToString(offering.ReservedNodeOfferingId), nil
			}
		}
	}

	return "", fmt.Errorf("no offerings found for %s", rsDetails.NodeType)
}

// matchesDuration checks if the offering duration matches our requirement
func (c *PurchaseClient) matchesDuration(offeringDuration *int32, requiredMonths int) bool {
	if offeringDuration == nil {
		return false
	}

	// Duration is in seconds, convert to months
	offeringMonths := *offeringDuration / 2592000 // 30 days in seconds

	return int(offeringMonths) == requiredMonths
}

// matchesOfferingType checks if the offering type matches our payment option
func (c *PurchaseClient) matchesOfferingType(offeringType string, paymentOption string) bool {
	// Map payment options to Redshift offering types
	switch paymentOption {
	case "all-upfront":
		return offeringType == "All Upfront"
	case "partial-upfront":
		return offeringType == "Partial Upfront"
	case "no-upfront":
		return offeringType == "No Upfront"
	default:
		return false
	}
}

// ValidateOffering checks if an offering exists without purchasing
func (c *PurchaseClient) ValidateOffering(ctx context.Context, rec common.Recommendation) error {
	_, err := c.findOfferingID(ctx, rec)
	return err
}

// GetOfferingDetails retrieves detailed information about an offering
func (c *PurchaseClient) GetOfferingDetails(ctx context.Context, rec common.Recommendation) (*common.OfferingDetails, error) {
	offeringID, err := c.findOfferingID(ctx, rec)
	if err != nil {
		return nil, err
	}

	// Get specific offering details
	input := &redshift.DescribeReservedNodeOfferingsInput{
		ReservedNodeOfferingId: aws.String(offeringID),
		MaxRecords:             aws.Int32(1),
	}

	result, err := c.client.DescribeReservedNodeOfferings(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to get offering details: %w", err)
	}

	if len(result.ReservedNodeOfferings) == 0 {
		return nil, fmt.Errorf("offering not found: %s", offeringID)
	}

	offering := result.ReservedNodeOfferings[0]
	rsDetails := rec.ServiceDetails.(*common.RedshiftDetails)

	details := &common.OfferingDetails{
		OfferingID:    aws.ToString(offering.ReservedNodeOfferingId),
		NodeType:      aws.ToString(offering.NodeType),
		Duration:      fmt.Sprintf("%d", aws.ToInt32(offering.Duration)),
		PaymentOption: string(offering.ReservedNodeOfferingType),
		FixedPrice:    aws.ToFloat64(offering.FixedPrice),
		UsagePrice:    aws.ToFloat64(offering.UsagePrice),
		CurrencyCode:  aws.ToString(offering.CurrencyCode),
		OfferingType:  fmt.Sprintf("%s-%d-nodes", rsDetails.NodeType, rsDetails.NumberOfNodes),
	}

	// Calculate recurring charges
	for _, charge := range offering.RecurringCharges {
		if charge.RecurringChargeAmount != nil && charge.RecurringChargeFrequency != nil {
			if *charge.RecurringChargeFrequency == "Hourly" {
				details.UsagePrice = *charge.RecurringChargeAmount
			}
		}
	}

	return details, nil
}

// BatchPurchase purchases multiple Redshift Reserved Nodes with error handling and rate limiting
func (c *PurchaseClient) BatchPurchase(ctx context.Context, recommendations []common.Recommendation, delayBetweenPurchases time.Duration) []common.PurchaseResult {
	return c.BasePurchaseClient.BatchPurchase(ctx, c, recommendations, delayBetweenPurchases)
}

// createPurchaseTags creates standard tags for the purchase
func (c *PurchaseClient) createPurchaseTags(rec common.Recommendation) []types.Tag {
	rsDetails := rec.ServiceDetails.(*common.RedshiftDetails)

	return []types.Tag{
		{
			Key:   aws.String("Purpose"),
			Value: aws.String("Reserved Node Purchase"),
		},
		{
			Key:   aws.String("NodeType"),
			Value: aws.String(rsDetails.NodeType),
		},
		{
			Key:   aws.String("NumberOfNodes"),
			Value: aws.String(fmt.Sprintf("%d", rsDetails.NumberOfNodes)),
		},
		{
			Key:   aws.String("ClusterType"),
			Value: aws.String(rsDetails.ClusterType),
		},
		{
			Key:   aws.String("Region"),
			Value: aws.String(rec.Region),
		},
		{
			Key:   aws.String("PurchaseDate"),
			Value: aws.String(time.Now().Format("2006-01-02")),
		},
		{
			Key:   aws.String("Tool"),
			Value: aws.String("ri-helper-tool"),
		},
		{
			Key:   aws.String("PaymentOption"),
			Value: aws.String(rec.PaymentOption),
		},
		{
			Key:   aws.String("Term"),
			Value: aws.String(fmt.Sprintf("%d-months", rec.Term)),
		},
	}
}