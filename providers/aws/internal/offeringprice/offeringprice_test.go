package offeringprice

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/purchasecfg"
)

func TestPrice(t *testing.T) {
	one, three := int64(purchasecfg.OneYearSeconds), int64(purchasecfg.ThreeYearSeconds)
	for _, tc := range []struct {
		name         string
		in           Input
		total, hours float64
		hourly       float64
		err          string
	}{
		{"all upfront", Input{Term: "1yr", FixedPrice: 1000, DurationSeconds: one}, 1000, 8760, 0, ""},
		{"hourly charge, usage 0", Input{Term: "1yr", Charges: []Charge{{0.05, "Hourly"}}, DurationSeconds: one}, 438, 8760, 0.05, ""},
		{"usage only, no charges", Input{Term: "1yr", UsagePrice: 0.05, DurationSeconds: one}, 438, 8760, 0.05, ""},
		{"partial 3yr", Input{Term: "3yr", FixedPrice: 500, Charges: []Charge{{0.03, "Hourly"}}, DurationSeconds: three}, 500 + 0.03*26280, 26280, 0.03, ""},
		{"both nonzero", Input{Term: "1yr", UsagePrice: 0.05, Charges: []Charge{{0.05, "Hourly"}}, DurationSeconds: one}, 0, 0, 0, "refusing to guess"},
		{"non hourly", Input{Term: "1yr", Charges: []Charge{{1, "Monthly"}}, DurationSeconds: one}, 0, 0, 0, "only \"Hourly\""},
		{"missing duration", Input{Term: "1yr"}, 0, 0, 0, "no duration"},
		{"term mismatch", Input{Term: "3yr", DurationSeconds: one}, 0, 0, 0, "does not match"},
		{"negative fixed", Input{Term: "1yr", FixedPrice: -1, DurationSeconds: one}, 0, 0, 0, "invalid FixedPrice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.in.Service = "TEST"
			got, err := Price(tc.in)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tc.total, got.Total, 1e-9)
			assert.InDelta(t, tc.hourly, got.Hourly, 1e-12)
			assert.InDelta(t, tc.total/tc.hours, got.EffectiveHourly, 1e-12)
		})
	}
}

func TestCurrency(t *testing.T) {
	_, err := Currency("S", "")
	require.Error(t, err)
	c, err := Currency("S", "EUR")
	require.NoError(t, err)
	assert.Equal(t, "EUR", c)

	c, err = AssumedUSD("S", "us-east-1")
	require.NoError(t, err)
	assert.Equal(t, "USD", c)
	_, err = AssumedUSD("S", "cn-north-1")
	require.Error(t, err)
}
