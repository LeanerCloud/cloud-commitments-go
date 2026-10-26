package skumatch

import (
	"google.golang.org/api/cloudbilling/v1"

	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/internal/billingcurrency"
)

// PriceFunc returns the unit price and currency of sku, or price 0 when the
// SKU has no paid rate and must be skipped.
type PriceFunc func(sku *cloudbilling.Sku) (price float64, currency string, err error)

// ExtractPricing collects the on-demand price and the termYears commitment
// price (0 when absent) from the SKUs for which match is true, pricing each
// with price. It errors on a missing or mixed currency, an unclassifiable or
// ambiguous SKU, or an unsupported term.
func ExtractPricing(skus []*cloudbilling.Sku, match func(*cloudbilling.Sku) bool, price PriceFunc, termYears int) (onDemand, commitment float64, currency string, err error) {
	var prices SlotPrices
	for _, sku := range skus {
		if !match(sku) {
			continue
		}

		p, curr, priceErr := price(sku)
		if priceErr != nil {
			return 0, 0, "", priceErr
		}
		if p == 0 {
			continue
		}

		currency, err = billingcurrency.Unify(currency, curr, sku.SkuId)
		if err != nil {
			return 0, 0, "", err
		}

		slot, slotErr := Slot(sku)
		if slotErr != nil {
			return 0, 0, "", slotErr
		}
		if addErr := prices.Add(slot, sku, p); addErr != nil {
			return 0, 0, "", addErr
		}
	}

	commitmentPrice, termErr := prices.Commitment(termYears)
	if termErr != nil {
		return 0, 0, "", termErr
	}
	return prices.OnDemand, commitmentPrice, currency, nil
}

// HourlyUnitPrice is the PriceFunc for services whose catalog unit is already
// per hour: it returns UnitPrice without unit conversion.
func HourlyUnitPrice(sku *cloudbilling.Sku) (price float64, currency string, err error) {
	price, currency = UnitPrice(sku)
	return price, currency, nil
}
