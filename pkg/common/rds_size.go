package common //nolint:revive // pkg/common is the module's core domain package, imported by name (common.X) throughout this repo and by consumer-mcp

import "strings"

// rdsInstanceNU maps an RDS instance size suffix to the normalized units AWS
// uses for size-flexible RDS reservations within an instance family (Single-AZ):
// https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/USER_WorkingWithReservedDBInstances.html#USER_WorkingWithReservedDBInstances.SizeFlexible
// The doc table runs micro to 32xlarge and has no 48xlarge or nano row; nano is kept
// from the earlier family-NU table.
var rdsInstanceNU = map[string]float64{
	"nano":     0.25,
	"micro":    0.5,
	"small":    1,
	"medium":   2,
	"large":    4,
	"xlarge":   8,
	"2xlarge":  16,
	"4xlarge":  32,
	"6xlarge":  48,
	"8xlarge":  64,
	"10xlarge": 80,
	"12xlarge": 96,
	"16xlarge": 128,
	"24xlarge": 192,
	"32xlarge": 256,
}

// RDSInstanceNU returns the normalized units of an RDS instance type such as
// "db.r7g.2xlarge" (16). It returns 0 when the size is not recognized.
func RDSInstanceNU(instanceType string) float64 {
	parts := strings.Split(instanceType, ".")
	if len(parts) < 3 {
		return 0
	}
	return rdsInstanceNU[parts[len(parts)-1]]
}

// RDSFamily returns the family prefix of an RDS instance type, e.g.
// "db.r7g.2xlarge" gives "db.r7g". It returns "" when the type has no size suffix.
func RDSFamily(instanceType string) string {
	parts := strings.Split(instanceType, ".")
	if len(parts) < 3 {
		return ""
	}
	return strings.Join(parts[:len(parts)-1], ".")
}

// RDSSizeFlexibleEngine reports whether reservations of an engine are size-flexible. It is
// an allowlist: MySQL, MariaDB, PostgreSQL, Aurora, and Oracle only when the string
// explicitly marks BYOL (the normalized Oracle name merges BYOL and License Included, so it
// is not flexible). SQL Server, Oracle License Included, Db2 (the AWS doc lists it both as
// flexible and as not) and unknown engines are exact-class only, so a reservation is never credited to a size it
// may not cover.
func RDSSizeFlexibleEngine(engine string) bool {
	e := strings.ToLower(strings.TrimSpace(engine))
	switch {
	case e == "mysql", e == "mariadb", e == "postgres", e == "postgresql", strings.HasPrefix(e, "aurora"):
		return true
	case strings.HasPrefix(e, "oracle"):
		return strings.Contains(e, "byol") || strings.Contains(e, "bring-your-own") || strings.Contains(e, "bring your own")
	}
	return false
}
