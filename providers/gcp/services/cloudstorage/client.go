// Package cloudstorage provides GCP Cloud Storage commitments client
package cloudstorage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/recommender/apiv1/recommenderpb"
	"cloud.google.com/go/storage"
	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/option"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/internal/skumatch"
)

// Match the annual-average convention used for term totals, not calendar-month lengths.
const averageHoursPerMonth = 8760.0 / 12

// StorageService interface for storage operations (enables mocking).
type StorageService interface {
	Buckets(ctx context.Context, projectID string) BucketIterator
	Bucket(name string) BucketHandle
	Close() error
}

// BucketIterator interface for bucket iteration (enables mocking).
type BucketIterator interface {
	Next() (*storage.BucketAttrs, error)
}

// BucketHandle interface for bucket operations (enables mocking).
type BucketHandle interface {
	Create(ctx context.Context, projectID string, attrs *storage.BucketAttrs) error
}

// RecommenderClient preserves the legacy injection API for source compatibility.
//
// Deprecated: Cloud Storage has no commitment recommender.
type RecommenderClient interface {
	ListRecommendations(ctx context.Context, req *recommenderpb.ListRecommendationsRequest) RecommenderIterator
	Close() error
}

// RecommenderIterator preserves the legacy iteration API for source compatibility.
//
// Deprecated: Cloud Storage has no commitment recommender.
type RecommenderIterator interface {
	Next() (*recommenderpb.Recommendation, error)
}

// BillingService interface for billing operations (enables mocking).
type BillingService interface {
	ListSKUs(serviceID string) (*cloudbilling.ListSkusResponse, error)
}

// Client handles GCP Cloud Storage commitments.
type Client struct {
	ctx            context.Context
	projectID      string
	region         string
	clientOpts     []option.ClientOption
	storageService StorageService
	billingService BillingService
}

// NewClient creates a new GCP Cloud Storage client.
func NewClient(ctx context.Context, projectID, region string, opts ...option.ClientOption) (*Client, error) {
	return &Client{
		ctx:        ctx,
		projectID:  projectID,
		region:     region,
		clientOpts: opts,
	}, nil
}

// SetStorageService sets the storage service (for testing).
func (c *Client) SetStorageService(svc StorageService) {
	c.storageService = svc
}

// SetRecommenderClient ignores the supplied client and does not close it.
//
// Deprecated: Cloud Storage has no commitment recommender.
func (c *Client) SetRecommenderClient(_ RecommenderClient) {}

// SetBillingService sets the billing service (for testing).
func (c *Client) SetBillingService(svc BillingService) {
	c.billingService = svc
}

// realBillingService wraps the real cloudbilling.APIService.
type realBillingService struct {
	service *cloudbilling.APIService
}

func (r *realBillingService) ListSKUs(serviceID string) (*cloudbilling.ListSkusResponse, error) {
	return r.service.Services.Skus.List(serviceID).Do()
}

// GetServiceType returns the service type.
func (c *Client) GetServiceType() common.ServiceType {
	return common.ServiceStorage
}

// GetRegion returns the region.
func (c *Client) GetRegion() string {
	return c.region
}

// GetRecommendations always returns an error: Google has no commitment product
// for Cloud Storage (issue #78), so there is nothing to recommend. The error is
// explicit so callers do not read an empty result as "nothing to buy".
func (c *Client) GetRecommendations(_ context.Context, _ *common.RecommendationParams) ([]common.Recommendation, error) {
	return nil, fmt.Errorf("%w: Cloud Storage has no commitment product to recommend", common.ErrCommitmentPurchaseNotSupported)
}

// GetExistingCommitments returns an empty slice for Cloud Storage. GCP Cloud
// Storage has no commitment API: there is no committed-use discount purchase
// for GCS, and enumerating regional buckets does not represent a commitment --
// it caused every bucket in a region to appear as a "commitment" in the UI
// (10-L2). Return empty until a proper commitment-detection path is available.
func (c *Client) GetExistingCommitments(_ context.Context) ([]common.Commitment, error) {
	return nil, nil
}

// PurchaseCommitment is intentionally a no-op for Cloud Storage: GCP has no CUD
// or commitment purchase API for GCS at all. The previous implementation created
// a brand-new empty bucket, which is not a commitment and incurs ongoing cost, so
// a "purchase" silently provisioned billable infrastructure. Cloud Storage
// recommendations are therefore advisory only; this returns a clear not-supported
// error and never calls any resource-creation API (issue #640).
func (c *Client) PurchaseCommitment(ctx context.Context, rec common.Recommendation, opts common.PurchaseOptions) (common.PurchaseResult, error) {
	return common.PurchaseResult{
		Recommendation: rec,
		DryRun:         false,
		Success:        false,
		Timestamp:      time.Now(),
		Error: fmt.Errorf("%w: GCP Cloud Storage offers no committed-use discount or "+
			"commitment purchase API; this recommendation is advisory only",
			common.ErrCommitmentPurchaseNotSupported),
	}, fmt.Errorf("%w: Cloud Storage", common.ErrCommitmentPurchaseNotSupported)
}

// ValidateOffering validates that a storage class exists.
func (c *Client) ValidateOffering(ctx context.Context, rec common.Recommendation) error {
	validClasses, err := c.GetValidResourceTypes(ctx)
	if err != nil {
		return fmt.Errorf("failed to get valid storage classes: %w", err)
	}

	for _, class := range validClasses {
		if class == rec.ResourceType {
			return nil
		}
	}

	return fmt.Errorf("invalid Cloud Storage class: %s", rec.ResourceType)
}

// GetOfferingDetails retrieves Cloud Storage offering details from GCP Billing API.
func (c *Client) GetOfferingDetails(ctx context.Context, rec common.Recommendation) (*common.OfferingDetails, error) {
	termYears := 1
	if rec.Term == "3yr" || rec.Term == "3" {
		termYears = 3
	}

	pricing, err := c.getStoragePricing(ctx, rec.ResourceType, c.region, termYears)
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
		OfferingID:          fmt.Sprintf("gcp-storage-%s-%s-%s", rec.ResourceType, c.region, rec.Term),
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

// GetValidResourceTypes returns valid Cloud Storage classes.
func (c *Client) GetValidResourceTypes(ctx context.Context) ([]string, error) {
	// Cloud Storage has predefined storage classes
	validClasses := []string{
		"STANDARD",
		"NEARLINE",
		"COLDLINE",
		"ARCHIVE",
	}

	return validClasses, nil
}

// StoragePricing contains pricing information for Cloud Storage.
type StoragePricing struct {
	HourlyRate        float64
	CommitmentPrice   float64
	OnDemandPrice     float64
	Currency          string
	SavingsPercentage float64
}

// getStoragePricing gets pricing from GCP Cloud Billing Catalog API.
// It returns an error when commitment pricing is absent from the catalog rather
// than fabricating a price from a hardcoded discount factor (issue #1020).
func (c *Client) getStoragePricing(ctx context.Context, storageClass, region string, termYears int) (*StoragePricing, error) {
	svc, err := c.getOrCreateBillingService(ctx)
	if err != nil {
		return nil, err
	}

	skus, err := svc.ListSKUs("services/95FF-2EF5-5EA1")
	if err != nil {
		return nil, fmt.Errorf("failed to list SKUs: %w", err)
	}

	onDemandPrice, commitmentPrice, currency, err := extractStoragePricingFromSKUs(skus.Skus, storageClass, region)
	if err != nil {
		return nil, err
	}
	if onDemandPrice == 0 {
		return nil, fmt.Errorf("no pricing found for Cloud Storage class %s", storageClass)
	}
	if commitmentPrice == 0 {
		return nil, fmt.Errorf("no commitment pricing found for Cloud Storage class %s in region %s: catalog has no CUD SKU; cannot compute savings percentage", storageClass, region)
	}

	hoursInTerm := 8760.0 * float64(termYears)
	commitmentPriceTerm := commitmentPrice * hoursInTerm
	savingsPercentage := calculateStorageSavingsPercentage(onDemandPrice, hoursInTerm, commitmentPriceTerm)

	return &StoragePricing{
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

// extractStoragePricingFromSKUs extracts on-demand and commitment pricing from SKU list.
func extractStoragePricingFromSKUs(skus []*cloudbilling.Sku, storageClass, region string) (onDemand, commitment float64, currency string, err error) {
	currency = "USD"

	for _, sku := range skus {
		if !skuMatchesStorageClass(sku, storageClass, region) {
			continue
		}

		price, curr, err := extractStoragePriceFromSKU(sku)
		if err != nil {
			return 0, 0, "", err
		}
		if price == 0 {
			continue
		}

		if curr != "" {
			currency = curr
		}

		slot, slotErr := skumatch.Slot(sku)
		if slotErr != nil {
			return 0, 0, "", slotErr
		}
		switch slot {
		case skumatch.SlotCommitment:
			commitment = price
		case skumatch.SlotOnDemand:
			onDemand = price
		}
	}

	return onDemand, commitment, currency, nil
}

// extractStoragePriceFromSKU returns a per-GiB-hour rate regardless of the catalog unit.
func extractStoragePriceFromSKU(sku *cloudbilling.Sku) (price float64, currency string, err error) {
	if len(sku.PricingInfo) == 0 {
		return 0, "", nil
	}

	pricingInfo := sku.PricingInfo[0]
	if pricingInfo.PricingExpression == nil || len(pricingInfo.PricingExpression.TieredRates) == 0 {
		return 0, "", nil
	}

	rate := pricingInfo.PricingExpression.TieredRates[0]
	if rate.UnitPrice == nil {
		return 0, "", nil
	}

	price = float64(rate.UnitPrice.Units) + float64(rate.UnitPrice.Nanos)/1e9
	switch unit := pricingInfo.PricingExpression.UsageUnit; unit {
	case "GiBy.mo":
		price /= averageHoursPerMonth
	case "GiBy.h":
	default:
		return 0, "", fmt.Errorf("unsupported Cloud Storage usage unit %q for SKU %q (%s)", unit, sku.SkuId, sku.Description)
	}
	return price, rate.UnitPrice.CurrencyCode, nil
}

// calculateStorageSavingsPercentage calculates the savings percentage.
func calculateStorageSavingsPercentage(onDemandPrice, hoursInTerm, commitmentPrice float64) float64 {
	onDemandTotal := onDemandPrice * hoursInTerm
	return ((onDemandTotal - commitmentPrice) / onDemandTotal) * 100
}

func skuMatchesStorageClass(sku *cloudbilling.Sku, storageClass, region string) bool {
	description := strings.ToLower(sku.Description)
	if !strings.HasPrefix(description, strings.ToLower(storageClass)+" storage ") || strings.Contains(description, "(early delete)") {
		return false
	}

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
