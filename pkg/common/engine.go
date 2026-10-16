package common //nolint:revive // pkg/common is the module's core domain package (Recommendation, ProviderType, ...), imported by name (common.X) throughout this repo and by consumer-mcp; renaming would break every consumer import

import "strings"

// engineNameMap maps database engine names to a consistent normalized format.
// AWS RIs use: "aurora-postgresql", "aurora-mysql", "mysql", "postgres"
// Cost Explorer uses: "Aurora PostgreSQL", "Aurora MySQL", "MySQL", "PostgreSQL".
var engineNameMap = map[string]string{
	// Cost Explorer format -> normalized
	"aurora postgresql": "aurora-postgresql",
	"aurora mysql":      "aurora-mysql",
	"mysql":             "mysql",
	"postgresql":        "postgresql",
	"mariadb":           "mariadb",
	"oracle":            "oracle",
	"sql server":        "sqlserver",
	// Already normalized (from AWS RIs)
	"aurora-postgresql": "aurora-postgresql",
	"aurora-mysql":      "aurora-mysql",
	"postgres":          "postgresql",
	"oracle-se":         "oracle",
	"oracle-se1":        "oracle",
	"oracle-se2":        "oracle",
	"oracle-ee":         "oracle",
	"sqlserver-se":      "sqlserver",
	"sqlserver-ee":      "sqlserver",
	"sqlserver-ex":      "sqlserver",
	"sqlserver-web":     "sqlserver",
}

// NormalizeEngineName normalizes database engine names to a consistent format.
// Returns lowercase of the input as a fallback when the engine is not recognized.
func NormalizeEngineName(engine string) string {
	normalized := strings.ToLower(engine)
	if normalizedEngine, ok := engineNameMap[normalized]; ok {
		return normalizedEngine
	}
	return normalized
}

// EngineFromDetails extracts and normalizes the engine name from
// recommendation details. Returns an empty string for non-database/cache
// service types. DatabaseDetails/CacheDetails are always pointers (every
// producer constructs them that way; see service_details_codec.go's
// package doc for the invariant). The nil-pointer guards are not dead
// code: the `details == nil` check above only catches an untyped nil, so a
// typed nil reaches the switch and would panic on the field read.
func EngineFromDetails(details ServiceDetails) string {
	if details == nil {
		return ""
	}
	var engine string
	switch d := details.(type) {
	case *DatabaseDetails:
		if d == nil {
			return ""
		}
		engine = d.Engine
	case *CacheDetails:
		if d == nil {
			return ""
		}
		engine = d.Engine
	default:
		return ""
	}
	return NormalizeEngineName(engine)
}

// platformAliases maps the spellings operators type to the normalized EC2
// platform. Normalization lowercases and drops the " (amazon vpc)" suffix that
// Cost Explorer appends, so "Windows (Amazon VPC)" and "windows" agree.
var platformAliases = map[string]string{
	"linux": "linux/unix",
	"unix":  "linux/unix",
}

// NormalizePlatformName normalizes an EC2 platform for filter comparison.
func NormalizePlatformName(platform string) string {
	p := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(platform)), " (amazon vpc)")
	if alias, ok := platformAliases[p]; ok {
		return alias
	}
	return p
}

// Canonical tokens returned by NormalizeScopeName.
const (
	ScopeRegional = "regional"
	ScopeZonal    = "zonal"
)

// NormalizeTenancyName normalizes an EC2 tenancy for comparison. "shared" is
// the operator spelling of the API value "default"; "dedicated" and "host" stay
// distinct. Unrecognized input returns "" (unknown, a wildcard). Mirrors the
// accepted inputs of parseEC2Tenancy in providers/aws/services/ec2/client.go,
// which pkg cannot import.
func NormalizeTenancyName(tenancy string) string {
	switch t := strings.ToLower(strings.TrimSpace(tenancy)); t {
	case "default", "shared":
		return "default"
	case "dedicated", "host":
		return t
	default:
		return ""
	}
}

// NormalizeScopeName normalizes an EC2 reservation scope to ScopeRegional or
// ScopeZonal. Unrecognized input returns "" (unknown, a wildcard). Mirrors
// parseEC2Scope in providers/aws/services/ec2/client.go ("Region",
// "Availability Zone", and the hyphenated legacy spelling), which pkg cannot
// import.
func NormalizeScopeName(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "region", "regional":
		return ScopeRegional
	case "availability zone", "availability-zone", "zonal":
		return ScopeZonal
	default:
		return ""
	}
}

// EngineFilterKey returns the value the --include-engines/--exclude-engines
// filters compare against, and the normalizer to apply to each filter entry.
// Databases and caches use their engine; compute (EC2) uses its platform,
// which the CSV and table label "Engine". EngineFromDetails is deliberately
// left alone: it feeds the dedupe key and Matches, and existing EC2
// commitments carry no engine to match a platform against.
// An empty key means the recommendation has no engine or platform.
func EngineFilterKey(details ServiceDetails) (key string, normalize func(string) string) {
	var platform string
	switch d := details.(type) {
	case *ComputeDetails:
		if d == nil {
			return "", NormalizeEngineName
		}
		platform = d.Platform
	case ComputeDetails:
		platform = d.Platform
	default:
		return EngineFromDetails(details), NormalizeEngineName
	}
	return NormalizePlatformName(platform), NormalizePlatformName
}
