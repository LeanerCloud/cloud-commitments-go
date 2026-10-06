package skumatch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/api/cloudbilling/v1"
)

func TestInRegion(t *testing.T) {
	taxonomy := func(typ string) *cloudbilling.GeoTaxonomy { return &cloudbilling.GeoTaxonomy{Type: typ} }
	tests := []struct {
		name string
		sku  *cloudbilling.Sku
		want bool
	}{
		{"named region listed", &cloudbilling.Sku{ServiceRegions: []string{"us-east1", "Europe-West4"}}, true},
		{"named region not listed", &cloudbilling.Sku{ServiceRegions: []string{"us-east1"}}, false},
		{"named regions beat a GLOBAL taxonomy", &cloudbilling.Sku{ServiceRegions: []string{"us-east1"}, GeoTaxonomy: taxonomy("GLOBAL")}, false},
		{"nil regions with GLOBAL taxonomy", &cloudbilling.Sku{GeoTaxonomy: taxonomy("GLOBAL")}, true},
		{"empty regions with GLOBAL taxonomy", &cloudbilling.Sku{ServiceRegions: []string{}, GeoTaxonomy: taxonomy("GLOBAL")}, true},
		{"nil regions with REGIONAL taxonomy", &cloudbilling.Sku{GeoTaxonomy: taxonomy("REGIONAL")}, false},
		{"nil regions with MULTI_REGIONAL taxonomy", &cloudbilling.Sku{GeoTaxonomy: taxonomy("MULTI_REGIONAL")}, false},
		{"nil regions with unspecified taxonomy", &cloudbilling.Sku{GeoTaxonomy: taxonomy("TYPE_UNSPECIFIED")}, false},
		{"nil regions with no taxonomy", &cloudbilling.Sku{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, InRegion(tt.sku, "europe-west4"))
		})
	}
}

func TestHasToken(t *testing.T) {
	tests := []struct {
		description, token string
		want               bool
	}{
		{"db-n1-standard-1 Cloud SQL", "db-n1-standard-1", true},
		{"DB-N1-Standard-1", "db-n1-standard-1", true},
		{"db-n1-standard-16 Cloud SQL", "db-n1-standard-1", false},
		{"Redis STANDARD_HA", "STANDARD", false},
		{"Redis STANDARD_HA", "STANDARD_HA", true},
		{"Standard storage, standard-ha", "standard", true},
		{"Cloud SQL for PostgreSQL: Regional - HA", "mysql", false},
		{"anything", "", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, HasToken(tt.description, tt.token), "%q in %q", tt.token, tt.description)
	}
}

func TestSlot(t *testing.T) {
	tests := []struct {
		name    string
		sku     *cloudbilling.Sku
		want    PriceSlot
		wantErr string
	}{
		{name: "on demand", sku: &cloudbilling.Sku{Category: &cloudbilling.Category{UsageType: "OnDemand"}}, want: SlotOnDemand},
		{name: "1 year commitment without the word in the description", sku: &cloudbilling.Sku{Description: "Committed Use Discount: Cloud SQL DB custom CORE", Category: &cloudbilling.Category{UsageType: "Commit1Yr"}}, want: SlotCommitment},
		{name: "3 year commitment", sku: &cloudbilling.Sku{Category: &cloudbilling.Category{UsageType: "Commit3Yr"}}, want: SlotCommitment},
		{name: "description saying commitment on an on-demand SKU", sku: &cloudbilling.Sku{Description: "No commitment required", Category: &cloudbilling.Category{UsageType: "OnDemand"}}, want: SlotOnDemand},
		{name: "preemptible", sku: &cloudbilling.Sku{Category: &cloudbilling.Category{UsageType: "Preemptible"}}, want: SlotNone},
		{name: "spot", sku: &cloudbilling.Sku{Category: &cloudbilling.Category{UsageType: "Spot"}}, want: SlotNone},
		{name: "unknown usage type", sku: &cloudbilling.Sku{SkuId: "A1", Category: &cloudbilling.Category{UsageType: "Commit5Yr"}}, wantErr: `unrecognized usage type "Commit5Yr"`},
		{name: "empty usage type", sku: &cloudbilling.Sku{SkuId: "A2", Category: &cloudbilling.Category{}}, wantErr: "unrecognized usage type"},
		{name: "no category", sku: &cloudbilling.Sku{SkuId: "A3"}, wantErr: "no category"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Slot(tt.sku)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Slot() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("Slot() = %v, %v, want %v", got, err, tt.want)
			}
		})
	}
}
