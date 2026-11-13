// Package shared holds the catalog pricing pipeline and offering assembly that
// the Cloud SQL, Cloud Storage and Memorystore clients have in common.
package shared

import (
	"context"
	"fmt"

	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/option"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/internal/skulist"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/internal/skumatch"
)

// Term is the catalog price of one resource scaled to a commitment term.
type Term struct {
	Currency          string
	HourlyRate        float64
	CommitmentPrice   float64
	OnDemandPrice     float64
	SavingsPercentage float64
}

// PriceInput describes one catalog price lookup.
type PriceInput struct {
	// Billing is the injected billing service; nil makes Price create one from Opts.
	Billing skulist.BillingService
	// List returns the service's SKU catalog (clients cache it per client).
	List func(skulist.BillingService) (*cloudbilling.ListSkusResponse, error)
	// Extract picks the on-demand and commitment prices out of the catalog.
	Extract func(skus []*cloudbilling.Sku) (onDemand, commitment float64, currency string, err error)
	// Name labels the owning GCP service in billing errors.
	Name string
	// Label names the priced resource in scaling errors.
	Label  string
	Region string
	Opts   []option.ClientOption

	TermYears int
}

// Price resolves the billing service, reads the catalog and scales the matched
// prices to the term. Absent commitment pricing is an error, never a guess.
func Price(ctx context.Context, in PriceInput) (*Term, error) {
	svc, err := skulist.Resolve(ctx, in.Billing, in.Name, in.Opts...)
	if err != nil {
		return nil, err
	}

	skus, err := in.List(svc)
	if err != nil {
		return nil, fmt.Errorf("failed to list SKUs: %w", err)
	}

	onDemand, commitment, currency, err := in.Extract(skus.Skus)
	if err != nil {
		return nil, err
	}
	prices, err := skumatch.ScaleToTerm(in.Label, in.Region, onDemand, commitment, in.TermYears)
	if err != nil {
		return nil, err
	}

	return &Term{
		HourlyRate:        prices.HourlyRate,
		CommitmentPrice:   prices.CommitmentPrice,
		OnDemandPrice:     prices.OnDemandPrice,
		Currency:          currency,
		SavingsPercentage: prices.SavingsPercentage,
	}, nil
}

// OfferingDetails prices rec through price and assembles the offering whose ID
// is "<prefix>-<resource>-<region>-<term>".
func OfferingDetails(ctx context.Context, rec common.Recommendation, prefix, region string, price func(ctx context.Context, resource, region string, termYears int) (*Term, error)) (*common.OfferingDetails, error) {
	termYears, err := skumatch.TermYears(rec.Term)
	if err != nil {
		return nil, err
	}

	pricing, err := price(ctx, rec.ResourceType, region, termYears)
	if err != nil {
		return nil, fmt.Errorf("failed to get pricing: %w", err)
	}

	offeringID := fmt.Sprintf("%s-%s-%s-%s", prefix, rec.ResourceType, region, rec.Term)
	return skumatch.OfferingDetails(rec, offeringID, termYears, pricing.CommitmentPrice, pricing.HourlyRate, pricing.Currency)
}
