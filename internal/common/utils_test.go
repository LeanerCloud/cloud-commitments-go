package common

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
)

func TestNormalizeRegionName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// Exact matches
		{"exact US East", "US East (N. Virginia)", "us-east-1"},
		{"exact EU West", "Europe (Ireland)", "eu-west-1"},
		{"exact Asia Pacific", "Asia Pacific (Tokyo)", "ap-northeast-1"},
		{"exact SA", "South America (São Paulo)", "sa-east-1"},

		// Already region codes
		{"already code us-east-1", "us-east-1", "us-east-1"},
		{"already code eu-west-2", "eu-west-2", "eu-west-2"},
		{"already code ap-southeast-1", "ap-southeast-1", "ap-southeast-1"},

		// Case insensitive
		{"case insensitive", "us east (n. virginia)", "us-east-1"},
		{"case insensitive upper", "EUROPE (LONDON)", "eu-west-2"},

		// Partial matches
		{"partial virginia", "virginia", "us-east-1"},
		{"partial n. virginia", "n. virginia", "us-east-1"},
		{"partial ohio", "ohio", "us-east-2"},
		{"partial california", "california", "us-west-1"},
		{"partial n. california", "n. california", "us-west-1"},
		{"partial oregon", "oregon", "us-west-2"},
		{"partial ireland", "ireland", "eu-west-1"},
		{"partial frankfurt", "frankfurt", "eu-central-1"},
		{"partial london", "london", "eu-west-2"},
		{"partial paris", "paris", "eu-west-3"},
		{"partial tokyo", "tokyo", "ap-northeast-1"},
		{"partial singapore", "singapore", "ap-southeast-1"},
		{"partial sydney", "sydney", "ap-southeast-2"},
		{"partial mumbai", "mumbai", "ap-south-1"},
		{"partial seoul", "seoul", "ap-northeast-2"},
		{"partial são paulo", "são paulo", "sa-east-1"},
		{"partial sao paulo", "sao paulo", "sa-east-1"},

		// Edge cases
		{"empty string", "", ""},
		{"unknown region", "unknown-region", "unknown-region"},
		{"random text", "some random text", "some random text"},

		// New regions
		{"cape town", "Africa (Cape Town)", "af-south-1"},
		{"hong kong", "Asia Pacific (Hong Kong)", "ap-east-1"},
		{"milan", "Europe (Milan)", "eu-south-1"},
		{"bahrain", "Middle East (Bahrain)", "me-south-1"},
		{"canada", "Canada (Central)", "ca-central-1"},
		{"stockholm", "Europe (Stockholm)", "eu-north-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeRegionName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsRegionCode(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"valid us-east-1", "us-east-1", true},
		{"valid eu-west-2", "eu-west-2", true},
		{"valid ap-southeast-1", "ap-southeast-1", true},
		{"valid af-south-1", "af-south-1", true},

		{"invalid uppercase", "US-EAST-1", false},
		{"invalid mixed case", "Us-East-1", false},
		{"invalid spaces", "us east 1", false},
		{"invalid parentheses", "us-east-1 (ohio)", false},
		{"invalid human name", "US East (N. Virginia)", false},
		{"invalid no dash", "useast1", false},

		{"empty string", "", false},
		{"single word", "virginia", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsRegionCode(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertPaymentOption(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected types.PaymentOption
	}{
		{"all upfront", "all-upfront", types.PaymentOptionAllUpfront},
		{"partial upfront", "partial-upfront", types.PaymentOptionPartialUpfront},
		{"no upfront", "no-upfront", types.PaymentOptionNoUpfront},
		{"unknown defaults to partial", "unknown", types.PaymentOptionPartialUpfront},
		{"empty defaults to partial", "", types.PaymentOptionPartialUpfront},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertPaymentOption(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertPaymentOptionToString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"all upfront", "all-upfront", "All Upfront"},
		{"partial upfront", "partial-upfront", "Partial Upfront"},
		{"no upfront", "no-upfront", "No Upfront"},
		{"unknown defaults to partial", "unknown", "Partial Upfront"},
		{"empty defaults to partial", "", "Partial Upfront"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertPaymentOptionToString(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertTermInYears(t *testing.T) {
	tests := []struct {
		name     string
		years    int
		expected types.TermInYears
	}{
		{"1 year", 1, types.TermInYearsOneYear},
		{"3 years", 3, types.TermInYearsThreeYears},
		{"unknown defaults to 3", 2, types.TermInYearsThreeYears},
		{"zero defaults to 3", 0, types.TermInYearsThreeYears},
		{"5 years defaults to 3", 5, types.TermInYearsThreeYears},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertTermInYears(tt.years)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertLookbackPeriod(t *testing.T) {
	tests := []struct {
		name     string
		days     int
		expected types.LookbackPeriodInDays
	}{
		{"7 days", 7, types.LookbackPeriodInDaysSevenDays},
		{"30 days", 30, types.LookbackPeriodInDaysThirtyDays},
		{"60 days", 60, types.LookbackPeriodInDaysSixtyDays},
		{"unknown defaults to 7", 14, types.LookbackPeriodInDaysSevenDays},
		{"zero defaults to 7", 0, types.LookbackPeriodInDaysSevenDays},
		{"90 days defaults to 7", 90, types.LookbackPeriodInDaysSevenDays},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertLookbackPeriod(tt.days)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetServiceStringForCostExplorer(t *testing.T) {
	tests := []struct {
		name     string
		service  ServiceType
		expected string
	}{
		{"RDS", ServiceRDS, "Amazon Relational Database Service"},
		{"ElastiCache", ServiceElastiCache, "Amazon ElastiCache"},
		{"EC2", ServiceEC2, "Amazon Elastic Compute Cloud - Compute"},
		{"OpenSearch", ServiceOpenSearch, "Amazon OpenSearch Service"},
		{"Elasticsearch", ServiceElasticsearch, "Amazon Elasticsearch Service"},
		{"Redshift", ServiceRedshift, "Amazon Redshift"},
		{"MemoryDB", ServiceMemoryDB, "Amazon MemoryDB Service"},
		{"Unknown service", ServiceType("Unknown"), "Unknown"},
		{"Custom service", ServiceType("Custom Service"), "Custom Service"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetServiceStringForCostExplorer(tt.service)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRegionNameToCodeMap(t *testing.T) {
	// Test that the map is populated
	assert.NotEmpty(t, RegionNameToCode)

	// Test some key entries
	expectedMappings := map[string]string{
		"US East (N. Virginia)":    "us-east-1",
		"US East (Ohio)":           "us-east-2",
		"Europe (Ireland)":         "eu-west-1",
		"Asia Pacific (Singapore)": "ap-southeast-1",
	}

	for name, code := range expectedMappings {
		assert.Equal(t, code, RegionNameToCode[name], "Region mapping for %s", name)
	}
}

// Benchmark tests
func BenchmarkNormalizeRegionName(b *testing.B) {
	testCases := []string{
		"US East (N. Virginia)",
		"us-east-1",
		"virginia",
		"unknown-region",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, tc := range testCases {
			_ = NormalizeRegionName(tc)
		}
	}
}

func BenchmarkIsRegionCode(b *testing.B) {
	testCases := []string{
		"us-east-1",
		"US-EAST-1",
		"US East (N. Virginia)",
		"virginia",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, tc := range testCases {
			_ = IsRegionCode(tc)
		}
	}
}

func BenchmarkConvertPaymentOption(b *testing.B) {
	options := []string{
		"all-upfront",
		"partial-upfront",
		"no-upfront",
		"unknown",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, opt := range options {
			_ = ConvertPaymentOption(opt)
		}
	}
}