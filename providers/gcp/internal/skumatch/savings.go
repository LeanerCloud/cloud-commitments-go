package skumatch

import (
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// SavingsPercent returns the percentage a commitment saves against on-demand
// over a term. commitmentTotal must already be a term total (per-hour price *
// hoursInTerm), on the same basis as onDemandPrice * hoursInTerm. A zero
// on-demand total yields NaN or Inf, so callers must reject a zero on-demand
// price first.
func SavingsPercent(onDemandPrice, hoursInTerm, commitmentTotal float64) float64 {
	onDemandTotal := onDemandPrice * hoursInTerm
	return ((onDemandTotal - commitmentTotal) / onDemandTotal) * 100
}

// TermPrices holds per-hour catalog prices scaled to a commitment term.
type TermPrices struct {
	HourlyRate        float64
	CommitmentPrice   float64
	OnDemandPrice     float64
	SavingsPercentage float64
}

// ScaleToTerm rejects missing catalog prices and scales both per-hour prices
// to term totals, so the savings percentage compares like with like (a
// per-hour commitment against a term-total on-demand would read near 100%).
func ScaleToTerm(what, region string, onDemandHourly, commitmentHourly float64, termYears int) (TermPrices, error) {
	if onDemandHourly <= 0 {
		return TermPrices{}, fmt.Errorf("no pricing found for %s", what)
	}
	if commitmentHourly <= 0 {
		return TermPrices{}, fmt.Errorf("no commitment pricing found for %s in region %s: catalog has no CUD SKU; cannot compute savings percentage", what, region)
	}
	hoursInTerm := common.HoursPerYear * float64(termYears)
	commitmentTotal := commitmentHourly * hoursInTerm
	return TermPrices{
		HourlyRate:        commitmentHourly,
		CommitmentPrice:   commitmentTotal,
		OnDemandPrice:     onDemandHourly * hoursInTerm,
		SavingsPercentage: SavingsPercent(onDemandHourly, hoursInTerm, commitmentTotal),
	}, nil
}

// OfferingDetails builds the offering for a commitment total, paying it up
// front or spreading it monthly by payment option. An unknown option pays the
// total up front.
func OfferingDetails(rec common.Recommendation, offeringID string, termYears int, commitmentTotal, hourlyRate float64, currency string) *common.OfferingDetails {
	var upfrontCost, recurringCost float64

	switch rec.PaymentOption {
	case "all-upfront", "upfront":
		upfrontCost = commitmentTotal
	case "monthly", "no-upfront":
		recurringCost = commitmentTotal / (float64(termYears) * 12)
	default:
		upfrontCost = commitmentTotal
	}

	return &common.OfferingDetails{
		OfferingID:          offeringID,
		ResourceType:        rec.ResourceType,
		Term:                rec.Term,
		PaymentOption:       rec.PaymentOption,
		UpfrontCost:         upfrontCost,
		RecurringCost:       recurringCost,
		TotalCost:           commitmentTotal,
		EffectiveHourlyRate: hourlyRate,
		Currency:            currency,
	}
}
