package skumatch

import (
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
