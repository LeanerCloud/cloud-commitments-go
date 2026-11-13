package skumatch

import (
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

func TestSavingsPercent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		onDemand, hours, cm float64
		want                float64
	}{
		{"typical 16 percent", 0.05, 8760, 0.042 * 8760, 16},
		{"commitment equals on-demand", 1, 100, 100, 0},
		{"commitment above on-demand is negative", 1, 100, 150, -50},
		{"free commitment saves everything", 1, 100, 0, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.InDelta(t, tt.want, SavingsPercent(tt.onDemand, tt.hours, tt.cm), 1e-9)
		})
	}

	t.Run("non-positive on-demand total yields 0, never NaN or Inf", func(t *testing.T) {
		t.Parallel()
		for _, got := range []float64{SavingsPercent(0, 8760, 100), SavingsPercent(1, 0, 100), SavingsPercent(0, 8760, 0), SavingsPercent(-1, 100, 100)} {
			assert.Equal(t, 0.0, got)
		}
	})
}

func TestMatchesTier(t *testing.T) {
	t.Parallel()
	global := &cloudbilling.GeoTaxonomy{Type: "GLOBAL"}
	tests := []struct {
		name         string
		sku          *cloudbilling.Sku
		tier, region string
		want         bool
	}{
		{"token and region match", &cloudbilling.Sku{Description: "Commitment db-n1-standard-1", ServiceRegions: []string{"us-central1"}}, "db-n1-standard-1", "us-central1", true},
		{"case-insensitive", &cloudbilling.Sku{Description: "COMMITMENT DB-N1-STANDARD-1", ServiceRegions: []string{"US-CENTRAL1"}}, "db-n1-standard-1", "us-central1", true},
		{"tier is a prefix of another tier", &cloudbilling.Sku{Description: "db-n1-standard-16", ServiceRegions: []string{"us-central1"}}, "db-n1-standard-1", "us-central1", false},
		{"wrong region", &cloudbilling.Sku{Description: "db-n1-standard-1", ServiceRegions: []string{"europe-west1"}}, "db-n1-standard-1", "us-central1", false},
		{"global SKU", &cloudbilling.Sku{Description: "db-n1-standard-1", GeoTaxonomy: global}, "db-n1-standard-1", "us-central1", true},
		{"empty tier never matches", &cloudbilling.Sku{Description: "anything", ServiceRegions: []string{"us-central1"}}, "", "us-central1", false},
		{"empty regions without taxonomy", &cloudbilling.Sku{Description: "db-n1-standard-1"}, "db-n1-standard-1", "us-central1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, MatchesTier(tt.sku, tt.tier, tt.region))
		})
	}
}

func TestScaleToTerm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		onDemand    float64
		commitment  float64
		years       int
		want        TermPrices
		wantErrText string
	}{
		{name: "zero on-demand", onDemand: 0, commitment: 0.05, years: 1, wantErrText: "no pricing found for Thing x"},
		{name: "negative on-demand", onDemand: -1, commitment: 0.05, years: 1, wantErrText: "no pricing found for Thing x"},
		{name: "zero commitment", onDemand: 0.1, commitment: 0, years: 1, wantErrText: "no commitment pricing found for Thing x in region r1: catalog has no CUD SKU; cannot compute savings percentage"},
		{name: "negative commitment", onDemand: 0.1, commitment: -0.05, years: 3, wantErrText: "no commitment pricing found for Thing x in region r1: catalog has no CUD SKU; cannot compute savings percentage"},
		{name: "1 year", onDemand: 0.1, commitment: 0.05, years: 1, want: TermPrices{HourlyRate: 0.05, CommitmentPrice: 0.05 * 8760, OnDemandPrice: 0.1 * 8760, SavingsPercentage: 50}},
		{name: "3 year", onDemand: 0.2, commitment: 0.1, years: 3, want: TermPrices{HourlyRate: 0.1, CommitmentPrice: 0.1 * 26280, OnDemandPrice: 0.2 * 26280, SavingsPercentage: 50}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ScaleToTerm("Thing x", "r1", tt.onDemand, tt.commitment, tt.years)
			if tt.wantErrText != "" {
				assert.EqualError(t, err, tt.wantErrText)
				return
			}
			assert.NoError(t, err)
			assert.InDelta(t, tt.want.HourlyRate, got.HourlyRate, 1e-9)
			assert.InDelta(t, tt.want.CommitmentPrice, got.CommitmentPrice, 1e-6)
			assert.InDelta(t, tt.want.OnDemandPrice, got.OnDemandPrice, 1e-6)
			assert.InDelta(t, tt.want.SavingsPercentage, got.SavingsPercentage, 1e-9)
		})
	}
}

func TestOfferingDetails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		option        string
		years         int
		wantUpfront   float64
		wantRecurring float64
	}{
		{"all-upfront", 1, 1200, 0},
		{"upfront", 3, 1200, 0},
		{"monthly", 1, 0, 100},
		{"monthly", 3, 0, 1200.0 / 36},
		{"no-upfront", 1, 0, 100},
		{"no-upfront", 3, 0, 1200.0 / 36},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q/%dyr", tt.option, tt.years), func(t *testing.T) {
			t.Parallel()
			rec := common.Recommendation{ResourceType: "rt", Term: "term-x", PaymentOption: common.PaymentOption(tt.option)}
			got, err := OfferingDetails(rec, "id-1", tt.years, 1200, 0.5, "EUR")
			require.NoError(t, err)
			assert.Equal(t, &common.OfferingDetails{
				OfferingID:          "id-1",
				ResourceType:        "rt",
				Term:                "term-x",
				PaymentOption:       tt.option,
				UpfrontCost:         tt.wantUpfront,
				RecurringCost:       tt.wantRecurring,
				TotalCost:           1200,
				EffectiveHourlyRate: 0.5,
				Currency:            "EUR",
			}, got)
		})
	}
}

func TestOfferingDetails_UnknownPaymentOptionErrors(t *testing.T) {
	t.Parallel()
	for _, option := range []string{"bogus", "", "Partial Upfront"} {
		t.Run(fmt.Sprintf("%q", option), func(t *testing.T) {
			t.Parallel()
			rec := common.Recommendation{ResourceType: "rt", Term: "1yr", PaymentOption: common.PaymentOption(option)}
			got, err := OfferingDetails(rec, "id-1", 1, 1200, 0.5, "EUR")
			require.Error(t, err)
			assert.Nil(t, got)
		})
	}
}
