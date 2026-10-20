package common //nolint:revive // see rds_size.go

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRDSInstanceNUAndFamily(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		in     string
		nu     float64
		family string
	}{
		{"db.r7g.large", 4, "db.r7g"},
		{"db.r7g.6xlarge", 48, "db.r7g"},
		{"db.t4g.nano", 0.25, "db.t4g"},
		{"db.r7g.metal", 0, "db.r7g"},
		{"db.r7g", 0, ""},
		{"", 0, ""},
	} {
		assert.Equal(t, tt.nu, RDSInstanceNU(tt.in), tt.in)
		assert.Equal(t, tt.family, RDSFamily(tt.in), tt.in)
	}
}

func TestRDSSizeFlexibleEngine(t *testing.T) {
	t.Parallel()
	for engine, want := range map[string]bool{
		"mysql": true, "MySQL": true, "mariadb": true, "postgres": true, "postgresql": true,
		"aurora-mysql": true, "aurora-postgresql": true, "Aurora MySQL": true, "aurora": true,
		"oracle-se2 (byol)": false, "oracle-ee-byol": false,
		"sqlserver-se": false, "sqlserver-se (li)": false, "sql server": false,
		"oracle": false, "oracle-se2": false, "oracle-se2 (li)": false,
		"db2-se": false, "somedb": false, "": false,
	} {
		assert.Equal(t, want, RDSSizeFlexibleEngine(engine), engine)
	}
}
