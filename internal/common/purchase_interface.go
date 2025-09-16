package common

import (
	"context"
	"time"
)

// PurchaseClient defines the interface for service-specific purchase clients
type PurchaseClient interface {
	// PurchaseRI purchases a Reserved Instance based on the recommendation
	PurchaseRI(ctx context.Context, rec Recommendation) PurchaseResult

	// ValidateOffering checks if an offering exists without purchasing
	ValidateOffering(ctx context.Context, rec Recommendation) error

	// GetOfferingDetails retrieves detailed information about an offering
	GetOfferingDetails(ctx context.Context, rec Recommendation) (*OfferingDetails, error)

	// BatchPurchase purchases multiple RIs with error handling and rate limiting
	BatchPurchase(ctx context.Context, recommendations []Recommendation, delayBetweenPurchases time.Duration) []PurchaseResult
}

// BasePurchaseClient provides common functionality for all purchase clients
type BasePurchaseClient struct {
	Region string
}

// BatchPurchase provides a default implementation for batch purchases
func (c *BasePurchaseClient) BatchPurchase(ctx context.Context, client PurchaseClient, recommendations []Recommendation, delayBetweenPurchases time.Duration) []PurchaseResult {
	results := make([]PurchaseResult, 0, len(recommendations))

	for i, rec := range recommendations {
		result := client.PurchaseRI(ctx, rec)
		results = append(results, result)

		// Add delay between purchases to avoid rate limits (except for the last one)
		if i < len(recommendations)-1 && delayBetweenPurchases > 0 {
			time.Sleep(delayBetweenPurchases)
		}
	}

	return results
}