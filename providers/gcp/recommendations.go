// Package gcp provides GCP recommendations client
package gcp

import (
	"context"
	"fmt"

	"google.golang.org/api/option"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/providers/gcp/services/cloudsql"
	"github.com/LeanerCloud/CUDly/providers/gcp/services/computeengine"
)

// RecommendationsClientAdapter aggregates GCP CUD and commitment recommendations across all services
type RecommendationsClientAdapter struct {
	ctx        context.Context
	projectID  string
	clientOpts []option.ClientOption
}

// GetRecommendations retrieves all GCP commitment recommendations across all services and regions
func (r *RecommendationsClientAdapter) GetRecommendations(ctx context.Context, params common.RecommendationParams) ([]common.Recommendation, error) {
	allRecommendations := make([]common.Recommendation, 0)

	// Get list of regions to check
	regions, err := r.getRegions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get regions: %w", err)
	}

	// Collect recommendations from each service type across all regions
	for _, region := range regions {
		// Compute Engine CUD recommendations
		if shouldIncludeService(params, common.ServiceCompute) {
			computeClient, err := computeengine.NewClient(ctx, r.projectID, region, r.clientOpts...)
			if err == nil {
				computeRecs, err := computeClient.GetRecommendations(ctx, params)
				if err == nil {
					allRecommendations = append(allRecommendations, computeRecs...)
				}
			}
		}

		// Cloud SQL commitment recommendations
		if shouldIncludeService(params, common.ServiceRelationalDB) {
			sqlClient, err := cloudsql.NewClient(ctx, r.projectID, region, r.clientOpts...)
			if err == nil {
				sqlRecs, err := sqlClient.GetRecommendations(ctx, params)
				if err == nil {
					allRecommendations = append(allRecommendations, sqlRecs...)
				}
			}
		}
	}

	return allRecommendations, nil
}

// GetRecommendationsForService retrieves GCP commitment recommendations for a specific service
func (r *RecommendationsClientAdapter) GetRecommendationsForService(ctx context.Context, service common.ServiceType) ([]common.Recommendation, error) {
	params := common.RecommendationParams{
		Service: service,
	}
	return r.GetRecommendations(ctx, params)
}

// GetAllRecommendations retrieves all GCP commitment recommendations across all services
func (r *RecommendationsClientAdapter) GetAllRecommendations(ctx context.Context) ([]common.Recommendation, error) {
	params := common.RecommendationParams{}
	return r.GetRecommendations(ctx, params)
}

// getRegions retrieves available GCP regions for the project
func (r *RecommendationsClientAdapter) getRegions(ctx context.Context) ([]string, error) {
	// Create a temporary provider to get regions
	provider := NewProviderWithProject(ctx, r.projectID, r.clientOpts...)

	regions, err := provider.GetRegions(ctx)
	if err != nil {
		return nil, err
	}

	regionNames := make([]string, 0, len(regions))
	for _, region := range regions {
		regionNames = append(regionNames, region.ID)
	}

	return regionNames, nil
}

// shouldIncludeService checks if a service should be included based on params
func shouldIncludeService(params common.RecommendationParams, service common.ServiceType) bool {
	// If no service specified in params, include all
	if params.Service == "" {
		return true
	}

	// Check if this is the requested service
	return params.Service == service
}
