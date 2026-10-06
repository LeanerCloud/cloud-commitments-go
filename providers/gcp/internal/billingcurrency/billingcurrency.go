// Package billingcurrency checks the currency of Cloud Billing Catalog prices.
package billingcurrency

import "fmt"

// Unify folds the currency of one priced SKU into the currency seen so far.
// A SKU without a currency, or one that disagrees with an earlier SKU, is an
// error: the catalog answers in a single request currency, so either case is
// a data error and no currency may be assumed.
func Unify(seen, skuCurrency, skuID string) (string, error) {
	if skuCurrency == "" {
		return "", fmt.Errorf("SKU %q has a price but no currency code", skuID)
	}
	if seen != "" && seen != skuCurrency {
		return "", fmt.Errorf("SKU %q is priced in %s but earlier SKUs are priced in %s", skuID, skuCurrency, seen)
	}
	return skuCurrency, nil
}
