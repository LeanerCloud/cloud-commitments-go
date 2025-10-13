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

// TestApplyCoverageWithCeiling tests the improved coverage algorithm that uses ceiling
func TestApplyCoverageWithCeiling(t *testing.T) {
	tests := []struct {
		name     string
		recs     []Recommendation
		coverage float64
		expected []Recommendation
	}{
		{
			name: "100% coverage returns all",
			recs: []Recommendation{
				{Count: 10, EstimatedCost: 1000},
				{Count: 5, EstimatedCost: 500},
			},
			coverage: 100.0,
			expected: []Recommendation{
				{Count: 10, EstimatedCost: 1000, Coverage: 100},
				{Count: 5, EstimatedCost: 500, Coverage: 100},
			},
		},
		{
			name: "0% coverage returns empty",
			recs: []Recommendation{
				{Count: 10, EstimatedCost: 1000},
			},
			coverage: 0.0,
			expected: []Recommendation{},
		},
		{
			name: "50% coverage with ceiling - prevents truncation",
			recs: []Recommendation{
				{Count: 1, EstimatedCost: 100}, // 1 * 0.5 = 0.5 -> 1 (ceiling)
				{Count: 3, EstimatedCost: 300}, // 3 * 0.5 = 1.5 -> 2 (ceiling)
				{Count: 10, EstimatedCost: 1000}, // 10 * 0.5 = 5
			},
			coverage: 50.0,
			expected: []Recommendation{
				{Count: 1, EstimatedCost: 100, Coverage: 50}, // 1/1 * 100 = 100
				{Count: 2, EstimatedCost: 200, Coverage: 50}, // 2/3 * 300 = 200
				{Count: 5, EstimatedCost: 500, Coverage: 50}, // 5/10 * 1000 = 500
			},
		},
		{
			name: "25% coverage with ceiling",
			recs: []Recommendation{
				{Count: 1, EstimatedCost: 100}, // 1 * 0.25 = 0.25 -> 1 (ceiling)
				{Count: 4, EstimatedCost: 400}, // 4 * 0.25 = 1
				{Count: 10, EstimatedCost: 1000}, // 10 * 0.25 = 2.5 -> 3 (ceiling)
			},
			coverage: 25.0,
			expected: []Recommendation{
				{Count: 1, EstimatedCost: 100, Coverage: 25}, // 1/1 * 100 = 100
				{Count: 1, EstimatedCost: 100, Coverage: 25}, // 1/4 * 400 = 100
				{Count: 3, EstimatedCost: 300, Coverage: 25}, // 3/10 * 1000 = 300
			},
		},
		{
			name: "Negative coverage returns empty",
			recs: []Recommendation{
				{Count: 10, EstimatedCost: 1000},
			},
			coverage: -10.0,
			expected: []Recommendation{},
		},
		{
			name: "Coverage > 100% returns all",
			recs: []Recommendation{
				{Count: 5, EstimatedCost: 500},
			},
			coverage: 150.0,
			expected: []Recommendation{
				{Count: 5, EstimatedCost: 500, Coverage: 150},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ApplyCoverage(tt.recs, tt.coverage)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestApplyInstanceLimit(t *testing.T) {
	tests := []struct {
		name         string
		recs         []Recommendation
		maxInstances int32
		wantCount    int32
		wantRecCount int
	}{
		{
			name: "No limit applied",
			recs: []Recommendation{
				{InstanceType: "db.t3.micro", Count: 5, EstimatedCost: 100, SavingsPercent: 20},
				{InstanceType: "db.t3.small", Count: 3, EstimatedCost: 150, SavingsPercent: 25},
			},
			maxInstances: 0, // No limit
			wantCount:    8,
			wantRecCount: 2,
		},
		{
			name: "Under limit",
			recs: []Recommendation{
				{InstanceType: "db.t3.micro", Count: 5, EstimatedCost: 100, SavingsPercent: 20},
				{InstanceType: "db.t3.small", Count: 3, EstimatedCost: 150, SavingsPercent: 25},
			},
			maxInstances: 10,
			wantCount:    8,
			wantRecCount: 2,
		},
		{
			name: "Exact limit",
			recs: []Recommendation{
				{InstanceType: "db.t3.micro", Count: 5, EstimatedCost: 100, SavingsPercent: 20},
				{InstanceType: "db.t3.small", Count: 3, EstimatedCost: 150, SavingsPercent: 25},
			},
			maxInstances: 8,
			wantCount:    8,
			wantRecCount: 2,
		},
		{
			name: "Partial limit - keep higher savings",
			recs: []Recommendation{
				{InstanceType: "db.t3.micro", Count: 5, EstimatedCost: 100, SavingsPercent: 20},
				{InstanceType: "db.t3.small", Count: 3, EstimatedCost: 150, SavingsPercent: 25},
				{InstanceType: "db.t3.large", Count: 4, EstimatedCost: 200, SavingsPercent: 30},
			},
			maxInstances: 7,
			wantCount:    7, // Should keep db.t3.large (4) + partial db.t3.small (3)
			wantRecCount: 2,
		},
		{
			name: "Very low limit",
			recs: []Recommendation{
				{InstanceType: "db.t3.micro", Count: 5, EstimatedCost: 100, SavingsPercent: 20},
				{InstanceType: "db.t3.small", Count: 3, EstimatedCost: 150, SavingsPercent: 25},
			},
			maxInstances: 2,
			wantCount:    2, // Should take partial from highest savings
			wantRecCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ApplyInstanceLimit(tt.recs, tt.maxInstances)

			// Calculate total instances
			totalCount := CalculateTotalInstances(result)

			if totalCount != tt.wantCount {
				t.Errorf("ApplyInstanceLimit() total instances = %d, want %d", totalCount, tt.wantCount)
			}

			if len(result) != tt.wantRecCount {
				t.Errorf("ApplyInstanceLimit() recommendations count = %d, want %d", len(result), tt.wantRecCount)
			}

			// Verify we don't exceed the limit
			if tt.maxInstances > 0 && totalCount > tt.maxInstances {
				t.Errorf("ApplyInstanceLimit() exceeded limit: %d > %d", totalCount, tt.maxInstances)
			}
		})
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

// TestApplyCountOverride tests the count override functionality
func TestApplyCountOverride(t *testing.T) {
	tests := []struct {
		name          string
		recs          []Recommendation
		overrideCount int32
		wantCount     int32     // total instances after override
		wantRecCount  int       // number of recommendations
		wantInstances []int32   // expected instance counts for each rec
	}{
		{
			name: "Override disabled (0)",
			recs: []Recommendation{
				{InstanceType: "db.t3.micro", Count: 5, EstimatedCost: 100, UpfrontCost: 500, RecurringMonthlyCost: 50},
				{InstanceType: "db.t3.small", Count: 10, EstimatedCost: 200, UpfrontCost: 1000, RecurringMonthlyCost: 100},
			},
			overrideCount: 0,
			wantCount:     15, // Original counts preserved
			wantRecCount:  2,
			wantInstances: []int32{5, 10},
		},
		{
			name: "Override to 1 instance each",
			recs: []Recommendation{
				{InstanceType: "db.t3.micro", Count: 5, EstimatedCost: 100, UpfrontCost: 500, RecurringMonthlyCost: 50},
				{InstanceType: "db.t3.small", Count: 10, EstimatedCost: 200, UpfrontCost: 1000, RecurringMonthlyCost: 100},
			},
			overrideCount: 1,
			wantCount:     2, // 1 + 1
			wantRecCount:  2,
			wantInstances: []int32{1, 1},
		},
		{
			name: "Override to 73 instances each (Valkey test case)",
			recs: []Recommendation{
				{InstanceType: "cache.t4g.micro", Count: 91, EstimatedCost: 467, UpfrontCost: 0, RecurringMonthlyCost: 467},
			},
			overrideCount: 73,
			wantCount:     73,
			wantRecCount:  1,
			wantInstances: []int32{73},
		},
		{
			name: "Override increases count",
			recs: []Recommendation{
				{InstanceType: "db.t3.micro", Count: 2, EstimatedCost: 40, UpfrontCost: 200, RecurringMonthlyCost: 20},
			},
			overrideCount: 5,
			wantCount:     5,
			wantRecCount:  1,
			wantInstances: []int32{5},
		},
		{
			name: "Override decreases count",
			recs: []Recommendation{
				{InstanceType: "db.t3.small", Count: 20, EstimatedCost: 400, UpfrontCost: 2000, RecurringMonthlyCost: 200},
			},
			overrideCount: 10,
			wantCount:     10,
			wantRecCount:  1,
			wantInstances: []int32{10},
		},
		{
			name: "Empty recommendations",
			recs: []Recommendation{},
			overrideCount: 5,
			wantCount:     0,
			wantRecCount:  0,
			wantInstances: []int32{},
		},
		{
			name: "Negative override (should behave like disabled)",
			recs: []Recommendation{
				{InstanceType: "db.t3.micro", Count: 5, EstimatedCost: 100, UpfrontCost: 500, RecurringMonthlyCost: 50},
			},
			overrideCount: -1,
			wantCount:     5, // Original count preserved
			wantRecCount:  1,
			wantInstances: []int32{5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ApplyCountOverride(tt.recs, tt.overrideCount)

			// Check recommendation count
			assert.Equal(t, tt.wantRecCount, len(result), "recommendation count mismatch")

			// Calculate total instances
			totalCount := CalculateTotalInstances(result)
			assert.Equal(t, tt.wantCount, totalCount, "total instance count mismatch")

			// Check individual instance counts
			for i, rec := range result {
				if i < len(tt.wantInstances) {
					assert.Equal(t, tt.wantInstances[i], rec.Count, "instance count mismatch for rec %d", i)
				}
			}

			// Verify cost proportions are maintained when override is active
			if tt.overrideCount > 0 && len(tt.recs) > 0 && len(result) > 0 {
				for i, rec := range result {
					if i < len(tt.recs) && tt.recs[i].Count > 0 && tt.recs[i].UpfrontCost > 0 {
						expectedRatio := float64(tt.overrideCount) / float64(tt.recs[i].Count)
						actualRatio := rec.UpfrontCost / tt.recs[i].UpfrontCost
						assert.InDelta(t, expectedRatio, actualRatio, 0.01, "cost ratio should match count ratio")
					}
				}
			}
		})
	}
}

// TestApplyCountOverrideWithServiceDetails tests override with different service types
func TestApplyCountOverrideWithServiceDetails(t *testing.T) {
	tests := []struct {
		name          string
		rec           Recommendation
		overrideCount int32
		wantCount     int32
	}{
		{
			name: "ElastiCache Redis",
			rec: Recommendation{
				Service:      ServiceElastiCache,
				InstanceType: "cache.t4g.micro",
				Count:        10,
				EstimatedCost: 200,
				ServiceDetails: &ElastiCacheDetails{
					Engine:   "redis",
					NodeType: "cache.t4g.micro",
				},
			},
			overrideCount: 5,
			wantCount:     5,
		},
		{
			name: "RDS Aurora MySQL",
			rec: Recommendation{
				Service:      ServiceRDS,
				InstanceType: "db.t4g.medium",
				Count:        18,
				EstimatedCost: 508,
				ServiceDetails: &RDSDetails{
					Engine:   "Aurora MySQL",
					AZConfig: "single-az",
				},
			},
			overrideCount: 2,
			wantCount:     2,
		},
		{
			name: "EC2 instance",
			rec: Recommendation{
				Service:      ServiceEC2,
				InstanceType: "t3.medium",
				Count:        50,
				EstimatedCost: 1000,
				ServiceDetails: &EC2Details{
					Platform: "Linux/UNIX",
					Tenancy:  "default",
					Scope:    "region",
				},
			},
			overrideCount: 25,
			wantCount:     25,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs := []Recommendation{tt.rec}
			result := ApplyCountOverride(recs, tt.overrideCount)

			assert.Equal(t, 1, len(result), "should have one recommendation")
			assert.Equal(t, tt.wantCount, result[0].Count, "count should be overridden")
			assert.Equal(t, tt.rec.Service, result[0].Service, "service type should be preserved")
			assert.Equal(t, tt.rec.ServiceDetails, result[0].ServiceDetails, "service details should be preserved")
		})
	}
}