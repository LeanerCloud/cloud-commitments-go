package skumatch

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
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

	t.Run("zero on-demand with commitment is -Inf", func(t *testing.T) {
		t.Parallel()
		assert.True(t, math.IsInf(SavingsPercent(0, 8760, 100), -1))
	})
	t.Run("zero hours with commitment is -Inf", func(t *testing.T) {
		t.Parallel()
		assert.True(t, math.IsInf(SavingsPercent(1, 0, 100), -1))
	})
	t.Run("zero on-demand and zero commitment is NaN", func(t *testing.T) {
		t.Parallel()
		assert.True(t, math.IsNaN(SavingsPercent(0, 8760, 0)))
	})
	t.Run("negative on-demand flips the sign", func(t *testing.T) {
		t.Parallel()
		assert.InDelta(t, 200, SavingsPercent(-1, 100, 100), 1e-9)
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
