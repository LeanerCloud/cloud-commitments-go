package common

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockRecommendationsClient for testing
type MockRecommendationsClient struct {
	mock.Mock
}

func (m *MockRecommendationsClient) GetRecommendations(ctx context.Context, params RecommendationParams) ([]Recommendation, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]Recommendation), args.Error(1)
}

func (m *MockRecommendationsClient) GetRecommendationsForDiscovery(ctx context.Context, service ServiceType) ([]Recommendation, error) {
	args := m.Called(ctx, service)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]Recommendation), args.Error(1)
}

func TestNewServiceProcessor(t *testing.T) {
	cfg := aws.Config{
		Region: "us-east-1",
	}
	config := ProcessorConfig{
		Services: []ServiceType{ServiceRDS, ServiceEC2},
		Regions:  []string{"us-east-1", "us-west-2"},
		Coverage: 75.0,
		IsDryRun: true,
	}

	processor := NewServiceProcessor(cfg, config)

	assert.NotNil(t, processor)
	assert.Equal(t, config, processor.config)
	assert.NotNil(t, processor.recClient)
}

func TestProcessorConfig(t *testing.T) {
	tests := []struct {
		name     string
		config   ProcessorConfig
		expected ProcessorConfig
	}{
		{
			name: "Full config",
			config: ProcessorConfig{
				Services:   []ServiceType{ServiceRDS, ServiceEC2, ServiceElastiCache},
				Regions:    []string{"us-east-1", "eu-west-1"},
				Coverage:   80.0,
				IsDryRun:   true,
				OutputPath: "/tmp/output",
			},
			expected: ProcessorConfig{
				Services:   []ServiceType{ServiceRDS, ServiceEC2, ServiceElastiCache},
				Regions:    []string{"us-east-1", "eu-west-1"},
				Coverage:   80.0,
				IsDryRun:   true,
				OutputPath: "/tmp/output",
			},
		},
		{
			name: "Minimal config",
			config: ProcessorConfig{
				Services: []ServiceType{ServiceRDS},
				Coverage: 100.0,
				IsDryRun: false,
			},
			expected: ProcessorConfig{
				Services:   []ServiceType{ServiceRDS},
				Regions:    nil,
				Coverage:   100.0,
				IsDryRun:   false,
				OutputPath: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.config)
		})
	}
}

func TestApplyCoverage(t *testing.T) {
	tests := []struct {
		name     string
		recs     []Recommendation
		coverage float64
		expected []Recommendation
	}{
		{
			name: "100% coverage",
			recs: []Recommendation{
				{Count: 10, EstimatedCost: 1000},
				{Count: 5, EstimatedCost: 500},
			},
			coverage: 100.0,
			expected: []Recommendation{
				{Count: 10, EstimatedCost: 1000},
				{Count: 5, EstimatedCost: 500},
			},
		},
		{
			name: "50% coverage",
			recs: []Recommendation{
				{Count: 10, EstimatedCost: 1000},
				{Count: 5, EstimatedCost: 500},
				{Count: 1, EstimatedCost: 100},
			},
			coverage: 50.0,
			expected: []Recommendation{
				{Count: 5, EstimatedCost: 1000},
				{Count: 2, EstimatedCost: 500},
			},
		},
		{
			name: "75% coverage",
			recs: []Recommendation{
				{Count: 8, EstimatedCost: 800},
				{Count: 4, EstimatedCost: 400},
			},
			coverage: 75.0,
			expected: []Recommendation{
				{Count: 6, EstimatedCost: 800},
				{Count: 3, EstimatedCost: 400},
			},
		},
		{
			name:     "Empty recommendations",
			recs:     []Recommendation{},
			coverage: 80.0,
			expected: []Recommendation{},
		},
		{
			name: "Coverage rounds down to zero",
			recs: []Recommendation{
				{Count: 1, EstimatedCost: 100},
				{Count: 1, EstimatedCost: 200},
			},
			coverage: 40.0,
			expected: []Recommendation{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ApplyCoverage(tt.recs, tt.coverage)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCalculateTotalSavings(t *testing.T) {
	tests := []struct {
		name     string
		recs     []Recommendation
		expected float64
	}{
		{
			name: "Multiple recommendations",
			recs: []Recommendation{
				{EstimatedCost: 1000.0, SavingsPercent: 10.0}, // 100
				{EstimatedCost: 2000.0, SavingsPercent: 10.0}, // 200
				{EstimatedCost: 3000.0, SavingsPercent: 10.0}, // 300
			},
			expected: 600.0,
		},
		{
			name:     "Empty recommendations",
			recs:     []Recommendation{},
			expected: 0.0,
		},
		{
			name: "Single recommendation",
			recs: []Recommendation{
				{EstimatedCost: 5000.0, SavingsPercent: 25.0}, // 1250
			},
			expected: 1250.0,
		},
		{
			name: "Different savings percentages",
			recs: []Recommendation{
				{EstimatedCost: 1000.0, SavingsPercent: 50.0}, // 500
				{EstimatedCost: 2000.0, SavingsPercent: 30.0}, // 600
				{EstimatedCost: 3000.0, SavingsPercent: 10.0}, // 300
			},
			expected: 1400.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculateTotalSavings(tt.recs)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCalculateTotalInstances(t *testing.T) {
	tests := []struct {
		name     string
		recs     []Recommendation
		expected int32
	}{
		{
			name: "Multiple recommendations",
			recs: []Recommendation{
				{Count: 5},
				{Count: 10},
				{Count: 3},
			},
			expected: 18,
		},
		{
			name:     "Empty recommendations",
			recs:     []Recommendation{},
			expected: 0,
		},
		{
			name: "Single recommendation",
			recs: []Recommendation{
				{Count: 42},
			},
			expected: 42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculateTotalInstances(tt.recs)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGroupRecommendationsByRegion(t *testing.T) {
	recs := []Recommendation{
		{Region: "us-east-1", InstanceType: "t3.micro"},
		{Region: "us-west-2", InstanceType: "t3.small"},
		{Region: "us-east-1", InstanceType: "t3.medium"},
		{Region: "eu-west-1", InstanceType: "t3.large"},
		{Region: "us-west-2", InstanceType: "t3.xlarge"},
	}

	grouped := GroupRecommendationsByRegion(recs)

	assert.Len(t, grouped, 3)
	assert.Len(t, grouped["us-east-1"], 2)
	assert.Len(t, grouped["us-west-2"], 2)
	assert.Len(t, grouped["eu-west-1"], 1)
}

func TestGroupRecommendationsByService(t *testing.T) {
	recs := []Recommendation{
		{Service: ServiceRDS, InstanceType: "db.t3.micro"},
		{Service: ServiceEC2, InstanceType: "t3.small"},
		{Service: ServiceRDS, InstanceType: "db.t3.medium"},
		{Service: ServiceElastiCache, InstanceType: "cache.t3.large"},
		{Service: ServiceEC2, InstanceType: "t3.xlarge"},
	}

	grouped := GroupRecommendationsByService(recs)

	assert.Len(t, grouped, 3)
	assert.Len(t, grouped[ServiceRDS], 2)
	assert.Len(t, grouped[ServiceEC2], 2)
	assert.Len(t, grouped[ServiceElastiCache], 1)
}

func TestFilterRecommendationsByThreshold(t *testing.T) {
	tests := []struct {
		name      string
		recs      []Recommendation
		threshold float64
		expected  int
	}{
		{
			name: "Filter by savings threshold",
			recs: []Recommendation{
				{EstimatedCost: 1000, SavingsPercent: 10},  // 100
				{EstimatedCost: 1000, SavingsPercent: 50},  // 500
				{EstimatedCost: 1000, SavingsPercent: 5},   // 50
				{EstimatedCost: 2000, SavingsPercent: 50},  // 1000
				{EstimatedCost: 1000, SavingsPercent: 7.5}, // 75
			},
			threshold: 100,
			expected:  3, // 100, 500, 1000
		},
		{
			name: "All above threshold",
			recs: []Recommendation{
				{EstimatedCost: 1000, SavingsPercent: 20}, // 200
				{EstimatedCost: 1000, SavingsPercent: 30}, // 300
				{EstimatedCost: 1000, SavingsPercent: 40}, // 400
			},
			threshold: 100,
			expected:  3,
		},
		{
			name: "None above threshold",
			recs: []Recommendation{
				{EstimatedCost: 100, SavingsPercent: 10}, // 10
				{EstimatedCost: 100, SavingsPercent: 20}, // 20
				{EstimatedCost: 100, SavingsPercent: 30}, // 30
			},
			threshold: 100,
			expected:  0,
		},
		{
			name:      "Empty recommendations",
			recs:      []Recommendation{},
			threshold: 100,
			expected:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FilterRecommendationsByThreshold(tt.recs, tt.threshold)
			assert.Len(t, result, tt.expected)

			// Verify all results meet threshold
			for _, r := range result {
				savings := r.EstimatedCost * (r.SavingsPercent / 100.0)
				assert.GreaterOrEqual(t, savings, tt.threshold)
			}
		})
	}
}

func TestSortRecommendationsBySavings(t *testing.T) {
	recs := []Recommendation{
		{EstimatedCost: 1000, SavingsPercent: 10, InstanceType: "t3.micro"},   // 100
		{EstimatedCost: 1000, SavingsPercent: 50, InstanceType: "t3.small"},   // 500
		{EstimatedCost: 1000, SavingsPercent: 5, InstanceType: "t3.medium"},   // 50
		{EstimatedCost: 2000, SavingsPercent: 50, InstanceType: "t3.large"},   // 1000
		{EstimatedCost: 1000, SavingsPercent: 25, InstanceType: "t3.xlarge"},  // 250
	}

	sorted := SortRecommendationsBySavings(recs)

	// Verify descending order by calculating savings
	savings := make([]float64, len(sorted))
	for i, rec := range sorted {
		savings[i] = rec.EstimatedCost * (rec.SavingsPercent / 100.0)
	}

	assert.Equal(t, float64(1000), savings[0])
	assert.Equal(t, float64(500), savings[1])
	assert.Equal(t, float64(250), savings[2])
	assert.Equal(t, float64(100), savings[3])
	assert.Equal(t, float64(50), savings[4])

	// Verify original slice is not modified
	originalSavings := recs[0].EstimatedCost * (recs[0].SavingsPercent / 100.0)
	assert.Equal(t, float64(100), originalSavings)
}

func TestMergeRecommendations(t *testing.T) {
	tests := []struct {
		name     string
		recsA    []Recommendation
		recsB    []Recommendation
		expected int
	}{
		{
			name: "Merge two non-empty slices",
			recsA: []Recommendation{
				{InstanceType: "t3.micro"},
				{InstanceType: "t3.small"},
			},
			recsB: []Recommendation{
				{InstanceType: "t3.medium"},
				{InstanceType: "t3.large"},
			},
			expected: 4,
		},
		{
			name: "Merge with empty slice",
			recsA: []Recommendation{
				{InstanceType: "t3.micro"},
				{InstanceType: "t3.small"},
			},
			recsB:    []Recommendation{},
			expected: 2,
		},
		{
			name:     "Merge two empty slices",
			recsA:    []Recommendation{},
			recsB:    []Recommendation{},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MergeRecommendations(tt.recsA, tt.recsB)
			assert.Len(t, result, tt.expected)
		})
	}
}

func TestValidateRecommendation(t *testing.T) {
	tests := []struct {
		name     string
		rec      Recommendation
		expected bool
	}{
		{
			name: "Valid RDS recommendation",
			rec: Recommendation{
				Service:      ServiceRDS,
				Region:       "us-east-1",
				InstanceType: "db.t3.micro",
				Count:        1,
				ServiceDetails: &RDSDetails{
					Engine:   "mysql",
					AZConfig: "multi-az",
				},
			},
			expected: true,
		},
		{
			name: "Valid EC2 recommendation",
			rec: Recommendation{
				Service:      ServiceEC2,
				Region:       "us-west-2",
				InstanceType: "t3.small",
				Count:        2,
				ServiceDetails: &EC2Details{
					Platform: "Linux/UNIX",
					Tenancy:  "shared",
				},
			},
			expected: true,
		},
		{
			name: "Invalid - missing region",
			rec: Recommendation{
				Service:      ServiceRDS,
				InstanceType: "db.t3.micro",
				Count:        1,
			},
			expected: false,
		},
		{
			name: "Invalid - missing instance type",
			rec: Recommendation{
				Service: ServiceRDS,
				Region:  "us-east-1",
				Count:   1,
			},
			expected: false,
		},
		{
			name: "Invalid - zero count",
			rec: Recommendation{
				Service:      ServiceRDS,
				Region:       "us-east-1",
				InstanceType: "db.t3.micro",
				Count:        0,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateRecommendation(tt.rec)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Additional tests for processor functions

func TestProcessorStructure(t *testing.T) {
	cfg := aws.Config{Region: "us-east-1"}
	config := ProcessorConfig{
		Services: []ServiceType{ServiceRDS, ServiceEC2},
		Regions:  []string{"us-east-1"},
		Coverage: 100.0,
		IsDryRun: false,
	}

	processor := NewServiceProcessor(cfg, config)
	assert.NotNil(t, processor)
	assert.NotNil(t, processor.recClient)
	assert.Equal(t, config, processor.config)
}

func TestGetServiceDisplayNameExtended(t *testing.T) {
	tests := []struct {
		service  ServiceType
		expected string
	}{
		{ServiceRDS, "RDS"},
		{ServiceElastiCache, "ElastiCache"},
		{ServiceEC2, "EC2"},
		{ServiceOpenSearch, "OpenSearch"},
		{ServiceElasticsearch, "OpenSearch"},
		{ServiceRedshift, "Redshift"},
		{ServiceMemoryDB, "MemoryDB"},
		{ServiceType("custom"), "custom"},
	}

	for _, tt := range tests {
		t.Run(string(tt.service), func(t *testing.T) {
			result := GetServiceDisplayName(tt.service)
			assert.Equal(t, tt.expected, result)
		})
	}
}
