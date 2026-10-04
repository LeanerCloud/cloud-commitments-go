package pricing_test

import (
	"math"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/pricing"
	"github.com/stretchr/testify/require"
)

func TestSelectReservation_NonfinitePrices(t *testing.T) {
	t.Parallel()
	for _, price := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		_, err := pricing.SelectReservation([]pricing.RetailPriceItem{{
			Type: "Reservation", ReservationTerm: "1 Year", UnitOfMeasure: "1 Hour",
			CurrencyCode: "USD", RetailPrice: price,
		}}, 1, func(pricing.RetailPriceItem) bool { return true })
		require.Error(t, err)
	}
}

func TestSelectReservation_EquivalentQuoteIgnoresUnselectedFields(t *testing.T) {
	t.Parallel()
	first := pricing.RetailPriceItem{
		Type: "Reservation", ReservationTerm: "1 Year", UnitOfMeasure: "1 Hour",
		CurrencyCode: "USD", RetailPrice: 1200,
	}
	second := first
	second.UnitOfMeasure, second.Location, second.UnitPrice = "1/Hour", "alternate display location", 1
	selected, err := pricing.SelectReservation([]pricing.RetailPriceItem{first, second}, 1,
		func(pricing.RetailPriceItem) bool { return true })
	require.NoError(t, err)
	require.Equal(t, 1200.0, selected.RetailPrice)
	require.Equal(t, "USD", selected.CurrencyCode)
}
