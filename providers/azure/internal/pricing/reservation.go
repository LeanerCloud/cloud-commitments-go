package pricing

import (
	"fmt"
	"math"
)

// SelectReservation rejects conflicting quotes rather than choosing by catalog order.
func SelectReservation(items []RetailPriceItem, termYears int, matches func(RetailPriceItem) bool) (RetailPriceItem, error) {
	term := fmt.Sprintf("%d Years", termYears)
	if termYears == 1 {
		term = "1 Year"
	}
	var selected RetailPriceItem
	found := false
	for i := range items {
		item := items[i]
		if item.Type != "Reservation" || item.ReservationTerm != term || !matches(item) {
			continue
		}
		item, err := normalizeReservation(item)
		if err != nil {
			return RetailPriceItem{}, err
		}
		if found && !sameReservation(selected, item) {
			return RetailPriceItem{}, fmt.Errorf("ambiguous reservation pricing for %s", term)
		}
		selected, found = item, true
	}
	if !found {
		return RetailPriceItem{}, fmt.Errorf("no reservation pricing found for %s", term)
	}
	return selected, nil
}

func sameReservation(a, b RetailPriceItem) bool {
	return [10]string{
		a.ServiceName, a.ArmRegionName, a.ArmSKUName, a.SKUName, a.ProductName,
		a.MeterName, a.Type, a.ReservationTerm, a.UnitOfMeasure, a.CurrencyCode,
	} == [10]string{
		b.ServiceName, b.ArmRegionName, b.ArmSKUName, b.SKUName, b.ProductName,
		b.MeterName, b.Type, b.ReservationTerm, b.UnitOfMeasure, b.CurrencyCode,
	} && a.RetailPrice == b.RetailPrice
}

func normalizeReservation(item RetailPriceItem) (RetailPriceItem, error) {
	switch item.UnitOfMeasure {
	case "1 Hour", "1/Hour":
		item.UnitOfMeasure = "1 Hour"
	default:
		return RetailPriceItem{}, fmt.Errorf("unsupported reservation unit %q", item.UnitOfMeasure)
	}
	if item.CurrencyCode == "" {
		return RetailPriceItem{}, fmt.Errorf("reservation currency is missing")
	}
	if item.RetailPrice <= 0 || math.IsNaN(item.RetailPrice) || math.IsInf(item.RetailPrice, 0) {
		return RetailPriceItem{}, fmt.Errorf("invalid reservation retail price %v", item.RetailPrice)
	}
	return item, nil
}
