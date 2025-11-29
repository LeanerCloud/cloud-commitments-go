package gcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/LeanerCloud/CUDly/pkg/common"
)

func TestShouldIncludeService(t *testing.T) {
	tests := []struct {
		name     string
		params   common.RecommendationParams
		service  common.ServiceType
		expected bool
	}{
		{
			name:     "Empty params includes all services - Compute",
			params:   common.RecommendationParams{},
			service:  common.ServiceCompute,
			expected: true,
		},
		{
			name:     "Empty params includes all services - RelationalDB",
			params:   common.RecommendationParams{},
			service:  common.ServiceRelationalDB,
			expected: true,
		},
		{
			name: "Specific service matches - Compute",
			params: common.RecommendationParams{
				Service: common.ServiceCompute,
			},
			service:  common.ServiceCompute,
			expected: true,
		},
		{
			name: "Specific service does not match",
			params: common.RecommendationParams{
				Service: common.ServiceCompute,
			},
			service:  common.ServiceRelationalDB,
			expected: false,
		},
		{
			name: "RelationalDB service matches",
			params: common.RecommendationParams{
				Service: common.ServiceRelationalDB,
			},
			service:  common.ServiceRelationalDB,
			expected: true,
		},
		{
			name: "Cache service requested - Compute not included",
			params: common.RecommendationParams{
				Service: common.ServiceCache,
			},
			service:  common.ServiceCompute,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldIncludeService(tt.params, tt.service)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRecommendationsClientAdapter_GetRecommendationsForService(t *testing.T) {
	ctx := context.Background()
	adapter := &RecommendationsClientAdapter{
		ctx:       ctx,
		projectID: "test-project",
	}

	// This will fail without credentials, but we're testing the structure
	_, err := adapter.GetRecommendationsForService(ctx, common.ServiceCompute)
	assert.Error(t, err) // Expected to fail without credentials/API access
}

func TestRecommendationsClientAdapter_GetAllRecommendations(t *testing.T) {
	ctx := context.Background()
	adapter := &RecommendationsClientAdapter{
		ctx:       ctx,
		projectID: "test-project",
	}

	// This will fail without credentials, but we're testing the structure
	_, err := adapter.GetAllRecommendations(ctx)
	assert.Error(t, err) // Expected to fail without credentials/API access
}

func TestRecommendationsClientAdapter_Fields(t *testing.T) {
	ctx := context.Background()
	adapter := &RecommendationsClientAdapter{
		ctx:       ctx,
		projectID: "my-gcp-project",
	}

	assert.Equal(t, ctx, adapter.ctx)
	assert.Equal(t, "my-gcp-project", adapter.projectID)
	assert.Nil(t, adapter.clientOpts)
}
