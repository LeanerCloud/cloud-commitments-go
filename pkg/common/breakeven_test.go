package common //nolint:revive // see breakeven.go

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBreakEven(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		rec       Recommendation
		wantMonth float64
		wantKnown bool
	}{
		{"provider value wins", Recommendation{BreakEvenMonths: 9, CommitmentCost: 1, Term: "1yr"}, 9, true},
		{"no upfront is zero and known", Recommendation{CommitmentCost: 0, EstimatedSavings: 50, Term: "1yr"}, 0, true},
		// OD 100/mo, 1yr all-upfront 720, net savings 40/mo: gross 100/mo, 7.2 months.
		{"all upfront 1yr", Recommendation{CommitmentCost: 720, EstimatedSavings: 40, Term: "1yr"}, 7.2, true},
		// 3yr partial: upfront 360, net 30/mo, amortized 10/mo, gross 40/mo -> 9 months.
		{"partial upfront 3yr", Recommendation{CommitmentCost: 360, EstimatedSavings: 30, Term: "3yr"}, 9, true},
		{"term alias", Recommendation{CommitmentCost: 360, EstimatedSavings: 30, Term: "P3Y"}, 9, true},
		{"never pays back", Recommendation{CommitmentCost: 120, EstimatedSavings: -10, Term: "1yr"}, math.Inf(1), true},
		{"unknown term", Recommendation{CommitmentCost: 100, EstimatedSavings: 10, Term: "5yr"}, 0, false},
		{"empty term", Recommendation{CommitmentCost: 100, EstimatedSavings: 10}, 0, false},
		{"negative upfront", Recommendation{CommitmentCost: -1, EstimatedSavings: 10, Term: "1yr"}, 0, false},
		{"nan savings", Recommendation{CommitmentCost: 10, EstimatedSavings: math.NaN(), Term: "1yr"}, 0, false},
		// #392: a zero upfront on an upfront-bearing payment option is a missing price.
		{"azure advisor upfront variant", Recommendation{PaymentOption: "upfront", EstimatedSavings: 50, Term: "1yr"}, 0, false},
		{"all upfront without price", Recommendation{PaymentOption: "all-upfront", EstimatedSavings: 50, Term: "3yr"}, 0, false},
		{"partial upfront without price", Recommendation{PaymentOption: "partial-upfront", EstimatedSavings: 50, Term: "1yr"}, 0, false},
		{"no upfront option is known zero", Recommendation{PaymentOption: "no-upfront", EstimatedSavings: 50, Term: "1yr"}, 0, true},
		{"azure monthly variant is known zero", Recommendation{PaymentOption: "monthly", EstimatedSavings: 50, Term: "1yr"}, 0, true},
		{"provider value wins over missing upfront", Recommendation{BreakEvenMonths: 9, PaymentOption: "all-upfront", Term: "1yr"}, 9, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, known := BreakEven(tt.rec)
			assert.Equal(t, tt.wantKnown, known)
			assert.InDelta(t, tt.wantMonth, got, 1e-9)
		})
	}
}

func TestBreakEven_InvariantUnderScaling(t *testing.T) {
	t.Parallel()
	rec := Recommendation{CommitmentCost: 720, EstimatedSavings: 40, Term: "1yr"}
	before, _ := BreakEven(rec)
	after, _ := BreakEven(ScaleRecommendationCosts(rec, 0.4))
	assert.InDelta(t, before, after, 1e-9)
}

func TestUpfrontPriceUnknown(t *testing.T) {
	t.Parallel()
	tests := []struct {
		payment string
		cost    float64
		want    bool
	}{
		{"all-upfront", 0, true},
		{"partial-upfront", 0, true},
		{"upfront", 0, true},
		{"partial", 0, true},
		{"All Upfront", 0, true},
		{" Partial Upfront ", 0, true},
		{"ALL_UPFRONT", 0, true},
		{"all-upfront", 720, false},
		{"upfront", 0.01, false},
		{"no-upfront", 0, false},
		{"No Upfront", 0, false},
		{"NO_UPFRONT", 0, false},
		{"monthly", 0, false},
		{"", 0, false},
		{"weekly-bananas", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.payment, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, UpfrontPriceUnknown(Recommendation{PaymentOption: tt.payment, CommitmentCost: tt.cost}))
		})
	}
}

// Pins each typed Term and PaymentOption constant to its wire value and to
// the break-even term and missing-price result it drives.
func TestBreakEven_TypedConstants(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "1yr", string(Term1Year))
	assert.Equal(t, "3yr", string(Term3Year))
	assert.Equal(t, "all-upfront", string(PaymentAllUpfront))
	assert.Equal(t, "partial-upfront", string(PaymentPartialUpfront))
	assert.Equal(t, "no-upfront", string(PaymentNoUpfront))

	parsed, err := ParseTerm(string(Term1Year))
	assert.NoError(t, err)
	assert.Equal(t, Term1Year, parsed)

	months, ok := breakEvenTermMonths(string(Term1Year))
	assert.Equal(t, 12.0, months)
	assert.True(t, ok)
	months, ok = breakEvenTermMonths(string(Term3Year))
	assert.Equal(t, 36.0, months)
	assert.True(t, ok)

	assert.True(t, UpfrontPriceUnknown(Recommendation{PaymentOption: string(PaymentAllUpfront)}))
	assert.True(t, UpfrontPriceUnknown(Recommendation{PaymentOption: string(PaymentPartialUpfront)}))
	assert.False(t, UpfrontPriceUnknown(Recommendation{PaymentOption: string(PaymentNoUpfront)}))
}
