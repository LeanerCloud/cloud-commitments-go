package skumatch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/api/cloudbilling/v1"
)

func TestInRegion(t *testing.T) {
	assert.True(t, InRegion(&cloudbilling.Sku{ServiceRegions: []string{"us-east1", "Europe-West4"}}, "europe-west4"))
	assert.False(t, InRegion(&cloudbilling.Sku{ServiceRegions: []string{"us-east1"}}, "europe-west4"))
	assert.False(t, InRegion(&cloudbilling.Sku{ServiceRegions: nil}, "europe-west4"))
	assert.False(t, InRegion(&cloudbilling.Sku{ServiceRegions: []string{}}, "europe-west4"))
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
