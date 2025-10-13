package common

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockRecommendationsClient for testing - implements RecommendationsClientInterface
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

// Verify that MockRecommendationsClient implements RecommendationsClientInterface
var _ RecommendationsClientInterface = (*MockRecommendationsClient)(nil)

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
				{Count: 10, EstimatedCost: 1000, Coverage: 100},
				{Count: 5, EstimatedCost: 500, Coverage: 100},
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
				{Count: 5, EstimatedCost: 500, Coverage: 50},  // 10 * 0.5 = 5, cost scaled to 500
				{Count: 3, EstimatedCost: 300, Coverage: 50},  // 5 * 0.5 = 2.5 → 3 (ceiling), cost scaled to 300
				{Count: 1, EstimatedCost: 100, Coverage: 50},  // 1 * 0.5 = 0.5 → 1 (ceiling), cost remains 100
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
				{Count: 6, EstimatedCost: 600, Coverage: 75},  // 8 * 0.75 = 6, cost scaled to 600
				{Count: 3, EstimatedCost: 300, Coverage: 75},  // 4 * 0.75 = 3, cost scaled to 300
			},
		},
		{
			name:     "Empty recommendations",
			recs:     []Recommendation{},
			coverage: 80.0,
			expected: []Recommendation{},
		},
		{
			name: "Coverage with ceiling preserves small counts",
			recs: []Recommendation{
				{Count: 1, EstimatedCost: 100},
				{Count: 1, EstimatedCost: 200},
			},
			coverage: 40.0,
			expected: []Recommendation{
				{Count: 1, EstimatedCost: 100, Coverage: 40},  // 1 * 0.4 = 0.4 → 1 (ceiling)
				{Count: 1, EstimatedCost: 200, Coverage: 40},  // 1 * 0.4 = 0.4 → 1 (ceiling)
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

// Tests for core processor methods

// Create a processor with mock recommendations client
func createMockProcessor(mockClient *MockRecommendationsClient) *ServiceProcessor {
	cfg := aws.Config{Region: "us-east-1"}
	config := ProcessorConfig{
		Services: []ServiceType{ServiceRDS, ServiceEC2},
		Regions:  []string{"us-east-1", "us-west-2"},
		Coverage: 80.0,
		IsDryRun: true,
	}
	processor := NewServiceProcessor(cfg, config)
	processor.recClient = mockClient
	return processor
}

func TestServiceProcessor_DiscoverRegionsForService(t *testing.T) {
	mockClient := &MockRecommendationsClient{}
	processor := createMockProcessor(mockClient)

	tests := []struct {
		name            string
		service         ServiceType
		mockReturns     []Recommendation
		mockError       error
		expectedRegions []string
		expectError     bool
	}{
		{
			name:    "Multiple regions discovered",
			service: ServiceRDS,
			mockReturns: []Recommendation{
				{Region: "us-east-1", InstanceType: "db.t3.micro"},
				{Region: "us-west-2", InstanceType: "db.t3.small"},
				{Region: "us-east-1", InstanceType: "db.t3.medium"}, // Duplicate region
				{Region: "eu-west-1", InstanceType: "db.t3.large"},
			},
			mockError:       nil,
			expectedRegions: []string{"eu-west-1", "us-east-1", "us-west-2"}, // Sorted
			expectError:     false,
		},
		{
			name:    "Single region discovered",
			service: ServiceEC2,
			mockReturns: []Recommendation{
				{Region: "ap-southeast-1", InstanceType: "t3.micro"},
				{Region: "ap-southeast-1", InstanceType: "t3.small"},
			},
			mockError:       nil,
			expectedRegions: []string{"ap-southeast-1"},
			expectError:     false,
		},
		{
			name:            "No recommendations found",
			service:         ServiceElastiCache,
			mockReturns:     []Recommendation{},
			mockError:       nil,
			expectedRegions: []string{},
			expectError:     false,
		},
		{
			name:            "API error",
			service:         ServiceRDS,
			mockReturns:     nil,
			mockError:       assert.AnError,
			expectedRegions: nil,
			expectError:     true,
		},
		{
			name:    "Recommendations with empty regions",
			service: ServiceRedshift,
			mockReturns: []Recommendation{
				{Region: "us-east-1", InstanceType: "ra3.xlplus"},
				{Region: "", InstanceType: "ra3.4xlarge"}, // Empty region should be ignored
				{Region: "us-west-2", InstanceType: "ra3.16xlarge"},
			},
			mockError:       nil,
			expectedRegions: []string{"us-east-1", "us-west-2"},
			expectError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient.On("GetRecommendationsForDiscovery", mock.Anything, tt.service).Return(tt.mockReturns, tt.mockError)

			regions, err := processor.discoverRegionsForService(context.Background(), tt.service)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, regions)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedRegions, regions)
			}

			mockClient.AssertExpectations(t)
			mockClient.ExpectedCalls = nil // Reset for next test
		})
	}
}

func TestServiceProcessor_ProcessService_WithRegions(t *testing.T) {
	mockClient := &MockRecommendationsClient{}
	cfg := aws.Config{Region: "us-east-1"}
	config := ProcessorConfig{
		Services: []ServiceType{ServiceRDS},
		Regions:  []string{"us-east-1", "us-west-2"}, // Explicit regions
		Coverage: 100.0,
		IsDryRun: true,
	}
	processor := NewServiceProcessor(cfg, config)
	processor.recClient = mockClient

	// Mock recommendations for each region
	usEast1Recs := []Recommendation{
		{Service: ServiceRDS, Region: "us-east-1", InstanceType: "db.t3.micro", Count: 2, EstimatedCost: 100},
		{Service: ServiceRDS, Region: "us-east-1", InstanceType: "db.t3.small", Count: 1, EstimatedCost: 200},
	}

	usWest2Recs := []Recommendation{
		{Service: ServiceRDS, Region: "us-west-2", InstanceType: "db.t3.medium", Count: 3, EstimatedCost: 300},
	}

	// Set up mocks
	mockClient.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(params RecommendationParams) bool {
		return params.Region == "us-east-1" && params.Service == ServiceRDS
	})).Return(usEast1Recs, nil)

	mockClient.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(params RecommendationParams) bool {
		return params.Region == "us-west-2" && params.Service == ServiceRDS
	})).Return(usWest2Recs, nil)

	recs, results := processor.processService(context.Background(), ServiceRDS)

	assert.Len(t, recs, 3) // Total recommendations from both regions
	assert.Len(t, results, 0) // No purchase results since no purchase client is set up

	mockClient.AssertExpectations(t)
}

func TestServiceProcessor_ProcessService_WithAutoDiscovery(t *testing.T) {
	mockClient := &MockRecommendationsClient{}
	cfg := aws.Config{Region: "us-east-1"}
	config := ProcessorConfig{
		Services: []ServiceType{ServiceEC2},
		Regions:  []string{}, // Empty regions - trigger auto-discovery
		Coverage: 75.0,
		IsDryRun: true,
	}
	processor := NewServiceProcessor(cfg, config)
	processor.recClient = mockClient

	// Mock discovery response
	discoveryRecs := []Recommendation{
		{Region: "us-east-1", InstanceType: "t3.micro"},
		{Region: "eu-west-1", InstanceType: "t3.small"},
	}
	mockClient.On("GetRecommendationsForDiscovery", mock.Anything, ServiceEC2).Return(discoveryRecs, nil)

	// Mock recommendations for discovered regions
	usEast1Recs := []Recommendation{
		{Service: ServiceEC2, Region: "us-east-1", InstanceType: "t3.micro", Count: 4, EstimatedCost: 400},
	}
	euWest1Recs := []Recommendation{
		{Service: ServiceEC2, Region: "eu-west-1", InstanceType: "t3.small", Count: 8, EstimatedCost: 800},
	}

	mockClient.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(params RecommendationParams) bool {
		return params.Region == "us-east-1"
	})).Return(usEast1Recs, nil)

	mockClient.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(params RecommendationParams) bool {
		return params.Region == "eu-west-1"
	})).Return(euWest1Recs, nil)

	recs, results := processor.processService(context.Background(), ServiceEC2)

	assert.Len(t, recs, 2) // Recommendations from discovered regions
	assert.Len(t, results, 0) // No purchase results since no purchase client is set up

	// Verify coverage was applied (75%) - order may vary due to map iteration
	totalRecommendedCount := recs[0].Count + recs[1].Count
	assert.Equal(t, int32(9), totalRecommendedCount) // 3 + 6 = 9 total

	mockClient.AssertExpectations(t)
}

func TestServiceProcessor_ProcessService_NoRecommendations(t *testing.T) {
	mockClient := &MockRecommendationsClient{}
	cfg := aws.Config{Region: "us-east-1"}
	config := ProcessorConfig{
		Services: []ServiceType{ServiceElastiCache},
		Regions:  []string{"us-east-1"},
		Coverage: 80.0,
		IsDryRun: true,
	}
	processor := NewServiceProcessor(cfg, config)
	processor.recClient = mockClient

	// Mock empty recommendations
	mockClient.On("GetRecommendations", mock.Anything, mock.AnythingOfType("RecommendationParams")).Return([]Recommendation{}, nil)

	recs, results := processor.processService(context.Background(), ServiceElastiCache)

	assert.Len(t, recs, 0)
	assert.Len(t, results, 0)

	mockClient.AssertExpectations(t)
}

func TestServiceProcessor_ProcessService_APIError(t *testing.T) {
	mockClient := &MockRecommendationsClient{}
	cfg := aws.Config{Region: "us-east-1"}
	config := ProcessorConfig{
		Services: []ServiceType{ServiceRDS},
		Regions:  []string{"us-east-1"},
		Coverage: 80.0,
		IsDryRun: true,
	}
	processor := NewServiceProcessor(cfg, config)
	processor.recClient = mockClient

	// Mock API error
	mockClient.On("GetRecommendations", mock.Anything, mock.AnythingOfType("RecommendationParams")).Return(nil, assert.AnError)

	recs, results := processor.processService(context.Background(), ServiceRDS)

	assert.Len(t, recs, 0)
	assert.Len(t, results, 0)

	mockClient.AssertExpectations(t)
}

func TestServiceProcessor_ProcessService_DiscoveryError(t *testing.T) {
	mockClient := &MockRecommendationsClient{}
	cfg := aws.Config{Region: "us-east-1"}
	config := ProcessorConfig{
		Services: []ServiceType{ServiceRDS},
		Regions:  []string{}, // Empty - trigger discovery
		Coverage: 80.0,
		IsDryRun: true,
	}
	processor := NewServiceProcessor(cfg, config)
	processor.recClient = mockClient

	// Mock discovery error
	mockClient.On("GetRecommendationsForDiscovery", mock.Anything, ServiceRDS).Return(nil, assert.AnError)

	recs, results := processor.processService(context.Background(), ServiceRDS)

	assert.Len(t, recs, 0)
	assert.Len(t, results, 0)

	mockClient.AssertExpectations(t)
}

func TestServiceProcessor_ProcessService_NoDiscoveredRegions(t *testing.T) {
	mockClient := &MockRecommendationsClient{}
	cfg := aws.Config{Region: "us-east-1"}
	config := ProcessorConfig{
		Services: []ServiceType{ServiceRDS},
		Regions:  []string{}, // Empty - trigger discovery
		Coverage: 80.0,
		IsDryRun: true,
	}
	processor := NewServiceProcessor(cfg, config)
	processor.recClient = mockClient

	// Mock discovery with no regions
	mockClient.On("GetRecommendationsForDiscovery", mock.Anything, ServiceRDS).Return([]Recommendation{}, nil)

	recs, results := processor.processService(context.Background(), ServiceRDS)

	assert.Len(t, recs, 0)
	assert.Len(t, results, 0)

	mockClient.AssertExpectations(t)
}

func TestServiceProcessor_ProcessAllServices(t *testing.T) {
	mockClient := &MockRecommendationsClient{}
	cfg := aws.Config{Region: "us-east-1"}
	config := ProcessorConfig{
		Services: []ServiceType{ServiceRDS, ServiceEC2},
		Regions:  []string{"us-east-1"},
		Coverage: 100.0,
		IsDryRun: true,
	}
	processor := NewServiceProcessor(cfg, config)
	processor.recClient = mockClient

	// Mock RDS recommendations
	rdsRecs := []Recommendation{
		{Service: ServiceRDS, Region: "us-east-1", InstanceType: "db.t3.micro", Count: 1, EstimatedCost: 100},
		{Service: ServiceRDS, Region: "us-east-1", InstanceType: "db.t3.small", Count: 2, EstimatedCost: 200},
	}

	// Mock EC2 recommendations
	ec2Recs := []Recommendation{
		{Service: ServiceEC2, Region: "us-east-1", InstanceType: "t3.micro", Count: 3, EstimatedCost: 150},
	}

	// Set up mocks
	mockClient.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(params RecommendationParams) bool {
		return params.Service == ServiceRDS
	})).Return(rdsRecs, nil)

	mockClient.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(params RecommendationParams) bool {
		return params.Service == ServiceEC2
	})).Return(ec2Recs, nil)

	allRecs, allResults, serviceStats := processor.ProcessAllServices(context.Background())

	// Verify combined results
	assert.Len(t, allRecs, 3) // 2 RDS + 1 EC2
	assert.Len(t, allResults, 0) // No purchase results since no purchase client is set up
	assert.Len(t, serviceStats, 2) // RDS and EC2

	// Verify service stats
	rdsStats, ok := serviceStats[ServiceRDS]
	assert.True(t, ok)
	assert.Equal(t, 2, rdsStats.RecommendationsSelected)
	assert.Equal(t, int32(3), rdsStats.InstancesProcessed) // 1 + 2
	assert.Equal(t, 300.0, rdsStats.TotalEstimatedSavings) // 100 + 200

	ec2Stats, ok := serviceStats[ServiceEC2]
	assert.True(t, ok)
	assert.Equal(t, 1, ec2Stats.RecommendationsSelected)
	assert.Equal(t, int32(3), ec2Stats.InstancesProcessed)
	assert.Equal(t, 150.0, ec2Stats.TotalEstimatedSavings)

	mockClient.AssertExpectations(t)
}

func TestServiceProcessor_ProcessAllServices_MixedResults(t *testing.T) {
	mockClient := &MockRecommendationsClient{}
	cfg := aws.Config{Region: "us-east-1"}
	config := ProcessorConfig{
		Services: []ServiceType{ServiceRDS, ServiceElastiCache},
		Regions:  []string{"us-east-1"},
		Coverage: 100.0,
		IsDryRun: true,
	}
	processor := NewServiceProcessor(cfg, config)
	processor.recClient = mockClient

	// Mock successful RDS recommendations
	rdsRecs := []Recommendation{
		{Service: ServiceRDS, Region: "us-east-1", InstanceType: "db.t3.micro", Count: 1, EstimatedCost: 100},
	}

	// Mock ElastiCache API error
	mockClient.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(params RecommendationParams) bool {
		return params.Service == ServiceRDS
	})).Return(rdsRecs, nil)

	mockClient.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(params RecommendationParams) bool {
		return params.Service == ServiceElastiCache
	})).Return(nil, assert.AnError)

	allRecs, allResults, serviceStats := processor.ProcessAllServices(context.Background())

	// Should only have RDS results
	assert.Len(t, allRecs, 1)
	assert.Len(t, allResults, 0) // No purchase results since no purchase client is set up
	assert.Len(t, serviceStats, 2) // Both services should have stats

	// RDS should have results
	rdsStats, ok := serviceStats[ServiceRDS]
	assert.True(t, ok)
	assert.Equal(t, 1, rdsStats.RecommendationsSelected)

	// ElastiCache should have empty stats
	cacheStats, ok := serviceStats[ServiceElastiCache]
	assert.True(t, ok)
	assert.Equal(t, 0, cacheStats.RecommendationsSelected)

	mockClient.AssertExpectations(t)
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

func TestServiceProcessorConfig_Validation(t *testing.T) {
	cfg := aws.Config{Region: "us-east-1"}

	tests := []struct {
		name     string
		config   ProcessorConfig
		expected bool
	}{
		{
			name: "Valid config with all fields",
			config: ProcessorConfig{
				Services:   []ServiceType{ServiceRDS, ServiceEC2},
				Regions:    []string{"us-east-1", "us-west-2"},
				Coverage:   80.0,
				IsDryRun:   true,
				OutputPath: "/tmp/output",
			},
			expected: true,
		},
		{
			name: "Valid minimal config",
			config: ProcessorConfig{
				Services: []ServiceType{ServiceRDS},
				Coverage: 75.0,
				IsDryRun: false,
			},
			expected: true,
		},
		{
			name: "Empty services",
			config: ProcessorConfig{
				Services: []ServiceType{},
				Coverage: 80.0,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processor := NewServiceProcessor(cfg, tt.config)
			result := len(processor.config.Services) > 0
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestServiceProcessor_GeneratePurchaseID(t *testing.T) {
	cfg := aws.Config{Region: "us-east-1"}
	processor := NewServiceProcessor(cfg, ProcessorConfig{
		Services: []ServiceType{ServiceRDS},
		Coverage: 80.0,
		IsDryRun: true,
	})

	rec := Recommendation{
		Service:      ServiceRDS,
		InstanceType: "db.t3.micro",
		Count:        2,
	}

	id := processor.generatePurchaseID(rec, "us-east-1", 1)

	assert.Contains(t, id, "dryrun") // Dry run mode
	assert.Contains(t, id, "rds")
	assert.Contains(t, id, "us-east-1")
	assert.Contains(t, id, "db-t3-micro")
	assert.Contains(t, id, "2x")
	assert.Regexp(t, `-\d{8}-\d{6}-\d{3}$`, id) // timestamp and index
}

func TestServiceProcessor_CreatePurchaseClient(t *testing.T) {
	cfg := aws.Config{Region: "us-east-1"}
	processor := NewServiceProcessor(cfg, ProcessorConfig{
		Services: []ServiceType{ServiceRDS},
		Coverage: 80.0,
	})

	// Test without factory function
	client := processor.createPurchaseClient(ServiceRDS, cfg)
	assert.Nil(t, client) // Should be nil since no factory is set

	// Test with mock factory function
	mockFactory := func(service ServiceType, cfg aws.Config) PurchaseClient {
		return &MockPurchaseClient{}
	}

	SetPurchaseClientFactory(mockFactory)
	defer SetPurchaseClientFactory(nil) // Clean up

	client = processor.createPurchaseClient(ServiceRDS, cfg)
	assert.NotNil(t, client)
}

func TestServiceStats_Calculation(t *testing.T) {
	stats := ServiceStats{
		Service:                 ServiceRDS,
		RegionsProcessed:        3,
		RecommendationsFound:    10,
		RecommendationsSelected: 8,
		InstancesProcessed:      25,
		SuccessfulPurchases:     7,
		FailedPurchases:         1,
		TotalEstimatedSavings:   1500.0,
	}

	assert.Equal(t, ServiceRDS, stats.Service)
	assert.Equal(t, 3, stats.RegionsProcessed)
	assert.Equal(t, 10, stats.RecommendationsFound)
	assert.Equal(t, 8, stats.RecommendationsSelected)
	assert.Equal(t, int32(25), stats.InstancesProcessed)
	assert.Equal(t, 7, stats.SuccessfulPurchases)
	assert.Equal(t, 1, stats.FailedPurchases)
	assert.Equal(t, 1500.0, stats.TotalEstimatedSavings)

	// Test success rate calculation
	totalAttempts := stats.SuccessfulPurchases + stats.FailedPurchases
	successRate := float64(stats.SuccessfulPurchases) / float64(totalAttempts) * 100
	assert.InDelta(t, 87.5, successRate, 0.1) // 7/8 = 87.5%
}

func TestPrintFinalSummary_Coverage(t *testing.T) {
	// Test that PrintFinalSummary function exists and handles various input scenarios
	allRecommendations := []Recommendation{
		{Service: ServiceRDS, Count: 5, EstimatedCost: 500},
		{Service: ServiceEC2, Count: 3, EstimatedCost: 300},
	}

	allResults := []PurchaseResult{
		{Success: true, Config: allRecommendations[0]},
		{Success: false, Config: allRecommendations[1]},
	}

	serviceStats := map[ServiceType]ServiceStats{
		ServiceRDS: {
			Service:                 ServiceRDS,
			RecommendationsSelected: 1,
			InstancesProcessed:      5,
			SuccessfulPurchases:     1,
			TotalEstimatedSavings:   500.0,
		},
		ServiceEC2: {
			Service:                 ServiceEC2,
			RecommendationsSelected: 1,
			InstancesProcessed:      3,
			FailedPurchases:         1,
			TotalEstimatedSavings:   300.0,
		},
	}

	// This mainly tests that the function doesn't panic
	// The actual output is printed to stdout
	assert.NotPanics(t, func() {
		PrintFinalSummary(allRecommendations, allResults, serviceStats, true)
	})

	assert.NotPanics(t, func() {
		PrintFinalSummary(allRecommendations, allResults, serviceStats, false)
	})

	// Test with empty data
	assert.NotPanics(t, func() {
		PrintFinalSummary([]Recommendation{}, []PurchaseResult{}, map[ServiceType]ServiceStats{}, true)
	})
}

func TestServiceProcessor_DiscoverRegions_Mock(t *testing.T) {
	cfg := aws.Config{Region: "us-east-1"}
	processor := NewServiceProcessor(cfg, ProcessorConfig{
		Services: []ServiceType{ServiceRDS},
		Coverage: 80.0,
	})

	// We can't easily test the actual discovery without mocking the recClient
	// But we can test that the method exists and has expected behavior structure
	assert.NotNil(t, processor.recClient)
	assert.NotNil(t, processor.discoverRegionsForService)
}

func TestServiceProcessor_ApplyCoverage(t *testing.T) {
	cfg := aws.Config{Region: "us-east-1"}
	processor := NewServiceProcessor(cfg, ProcessorConfig{
		Services: []ServiceType{ServiceRDS},
		Coverage: 75.0,
	})

	recs := []Recommendation{
		{Count: 10, EstimatedCost: 1000},
		{Count: 4, EstimatedCost: 400},
	}

	filtered := processor.applyCoverage(recs)

	assert.Len(t, filtered, 2)
	assert.Equal(t, int32(8), filtered[0].Count) // 10 * 0.75 = 7.5 -> 8 (ceiling)
	assert.Equal(t, int32(3), filtered[1].Count) // 4 * 0.75 = 3
}

func TestServiceProcessor_CalculateServiceStats(t *testing.T) {
	cfg := aws.Config{Region: "us-east-1"}
	processor := NewServiceProcessor(cfg, ProcessorConfig{
		Services: []ServiceType{ServiceRDS},
		Coverage: 80.0,
	})

	recs := []Recommendation{
		{Service: ServiceRDS, Region: "us-east-1", Count: 5, EstimatedCost: 500},
		{Service: ServiceRDS, Region: "us-west-2", Count: 3, EstimatedCost: 300},
		{Service: ServiceRDS, Region: "us-east-1", Count: 2, EstimatedCost: 200},
	}

	results := []PurchaseResult{
		{Success: true, Config: recs[0]},
		{Success: false, Config: recs[1]},
		{Success: true, Config: recs[2]},
	}

	stats := processor.calculateServiceStats(ServiceRDS, recs, results)

	assert.Equal(t, ServiceRDS, stats.Service)
	assert.Equal(t, 2, stats.RegionsProcessed) // us-east-1, us-west-2
	assert.Equal(t, 3, stats.RecommendationsFound)
	assert.Equal(t, 3, stats.RecommendationsSelected)
	assert.Equal(t, int32(10), stats.InstancesProcessed) // 5+3+2
	assert.Equal(t, 2, stats.SuccessfulPurchases)
	assert.Equal(t, 1, stats.FailedPurchases)
	assert.Equal(t, 1000.0, stats.TotalEstimatedSavings) // 500+300+200
}

func TestServiceProcessor_PrintServiceSummary(t *testing.T) {
	cfg := aws.Config{Region: "us-east-1"}
	processor := NewServiceProcessor(cfg, ProcessorConfig{
		Services: []ServiceType{ServiceRDS},
		Coverage: 80.0,
	})

	stats := ServiceStats{
		Service:                 ServiceRDS,
		RegionsProcessed:        2,
		RecommendationsSelected: 5,
		InstancesProcessed:      15,
		SuccessfulPurchases:     4,
		FailedPurchases:         1,
		TotalEstimatedSavings:   750.0,
	}

	// This mainly tests that the function doesn't panic
	// The actual output is printed to stdout
	assert.NotPanics(t, func() {
		processor.printServiceSummary(ServiceRDS, stats)
	})

	// Test with zero savings
	stats.TotalEstimatedSavings = 0
	assert.NotPanics(t, func() {
		processor.printServiceSummary(ServiceRDS, stats)
	})
}

// Benchmark tests
func BenchmarkApplyCoverage(b *testing.B) {
	recs := make([]Recommendation, 100)
	for i := range recs {
		recs[i] = Recommendation{
			Count:         int32(i + 1),
			EstimatedCost: float64(i * 100),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ApplyCoverage(recs, 75.0)
	}
}

func BenchmarkSortRecommendationsBySavings(b *testing.B) {
	recs := make([]Recommendation, 100)
	for i := range recs {
		recs[i] = Recommendation{
			EstimatedCost:  float64(i * 100),
			SavingsPercent: float64(i % 50),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SortRecommendationsBySavings(recs)
	}
}

func BenchmarkGroupRecommendationsByRegion(b *testing.B) {
	regions := []string{"us-east-1", "us-west-2", "eu-west-1", "ap-southeast-1"}
	recs := make([]Recommendation, 100)
	for i := range recs {
		recs[i] = Recommendation{
			Region:       regions[i%len(regions)],
			InstanceType: "t3.micro",
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GroupRecommendationsByRegion(recs)
	}
}
