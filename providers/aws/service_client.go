// Package aws provides service client implementations
package aws

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/provider"
	internalCommon "github.com/LeanerCloud/CUDly/internal/common"
	"github.com/LeanerCloud/CUDly/internal/ec2"
	"github.com/LeanerCloud/CUDly/internal/elasticache"
	"github.com/LeanerCloud/CUDly/internal/memorydb"
	"github.com/LeanerCloud/CUDly/internal/opensearch"
	"github.com/LeanerCloud/CUDly/internal/rds"
	"github.com/LeanerCloud/CUDly/internal/redshift"
	"github.com/LeanerCloud/CUDly/providers/aws/services/savingsplans"
)

// ServiceClientAdapter adapts internal purchase clients to the new provider.ServiceClient interface
type ServiceClientAdapter struct {
	client      internalCommon.PurchaseClient
	serviceType common.ServiceType
	region      string
}

// NewEC2Client creates a new EC2 service client
func NewEC2Client(cfg aws.Config) provider.ServiceClient {
	return &ServiceClientAdapter{
		client:      ec2.NewPurchaseClient(cfg),
		serviceType: common.ServiceEC2,
		region:      cfg.Region,
	}
}

// NewRDSClient creates a new RDS service client
func NewRDSClient(cfg aws.Config) provider.ServiceClient {
	return &ServiceClientAdapter{
		client:      rds.NewPurchaseClient(cfg),
		serviceType: common.ServiceRDS,
		region:      cfg.Region,
	}
}

// NewElastiCacheClient creates a new ElastiCache service client
func NewElastiCacheClient(cfg aws.Config) provider.ServiceClient {
	return &ServiceClientAdapter{
		client:      elasticache.NewPurchaseClient(cfg),
		serviceType: common.ServiceElastiCache,
		region:      cfg.Region,
	}
}

// NewOpenSearchClient creates a new OpenSearch service client
func NewOpenSearchClient(cfg aws.Config) provider.ServiceClient {
	return &ServiceClientAdapter{
		client:      opensearch.NewPurchaseClient(cfg),
		serviceType: common.ServiceOpenSearch,
		region:      cfg.Region,
	}
}

// NewRedshiftClient creates a new Redshift service client
func NewRedshiftClient(cfg aws.Config) provider.ServiceClient {
	return &ServiceClientAdapter{
		client:      redshift.NewPurchaseClient(cfg),
		serviceType: common.ServiceRedshift,
		region:      cfg.Region,
	}
}

// NewMemoryDBClient creates a new MemoryDB service client
func NewMemoryDBClient(cfg aws.Config) provider.ServiceClient {
	return &ServiceClientAdapter{
		client:      memorydb.NewPurchaseClient(cfg),
		serviceType: common.ServiceMemoryDB,
		region:      cfg.Region,
	}
}

// NewSavingsPlansClient creates a new Savings Plans service client
func NewSavingsPlansClient(cfg aws.Config) provider.ServiceClient {
	return &ServiceClientAdapter{
		client:      savingsplans.NewPurchaseClient(cfg),
		serviceType: common.ServiceSavingsPlans,
		region:      cfg.Region,
	}
}

// GetServiceType returns the service type
func (a *ServiceClientAdapter) GetServiceType() common.ServiceType {
	return a.serviceType
}

// GetRegion returns the region
func (a *ServiceClientAdapter) GetRegion() string {
	return a.region
}

// GetRecommendations gets recommendations for this service
// Note: This returns empty as AWS uses a centralized recommendations client (Cost Explorer)
// The actual recommendations come from GetRecommendationsClient()
func (a *ServiceClientAdapter) GetRecommendations(ctx context.Context, params common.RecommendationParams) ([]common.Recommendation, error) {
	// Service-specific clients don't provide recommendations directly
	// Recommendations come from Cost Explorer API via RecommendationsClient
	return []common.Recommendation{}, nil
}

// GetExistingCommitments retrieves existing reserved instances
func (a *ServiceClientAdapter) GetExistingCommitments(ctx context.Context) ([]common.Commitment, error) {
	internalRIs, err := a.client.GetExistingReservedInstances(ctx)
	if err != nil {
		return nil, err
	}

	commitments := make([]common.Commitment, 0, len(internalRIs))
	for _, ri := range internalRIs {
		commitments = append(commitments, ConvertCommitmentFromInternal(ri))
	}

	return commitments, nil
}

// PurchaseCommitment purchases a commitment (Reserved Instance)
func (a *ServiceClientAdapter) PurchaseCommitment(ctx context.Context, rec common.Recommendation) (common.PurchaseResult, error) {
	// Convert to internal format
	internalRec := ConvertRecommendationToInternal(rec)

	// Purchase using internal client
	internalResult := a.client.PurchaseRI(ctx, internalRec)

	// Convert result back
	result := ConvertPurchaseResultFromInternal(internalResult)

	// If not successful, create an error
	if !result.Success {
		result.Error = &PurchaseError{Message: internalResult.Message}
	}

	return result, nil
}

// ValidateOffering validates that an offering exists
func (a *ServiceClientAdapter) ValidateOffering(ctx context.Context, rec common.Recommendation) error {
	internalRec := ConvertRecommendationToInternal(rec)
	return a.client.ValidateOffering(ctx, internalRec)
}

// GetOfferingDetails retrieves offering details
func (a *ServiceClientAdapter) GetOfferingDetails(ctx context.Context, rec common.Recommendation) (*common.OfferingDetails, error) {
	internalRec := ConvertRecommendationToInternal(rec)
	internalDetails, err := a.client.GetOfferingDetails(ctx, internalRec)
	if err != nil {
		return nil, err
	}

	return ConvertOfferingDetailsFromInternal(internalDetails), nil
}

// GetValidResourceTypes returns valid resource types (instance types, node types, etc.)
func (a *ServiceClientAdapter) GetValidResourceTypes(ctx context.Context) ([]string, error) {
	return a.client.GetValidInstanceTypes(ctx)
}

// PurchaseError represents a purchase error
type PurchaseError struct {
	Message string
}

func (e *PurchaseError) Error() string {
	return e.Message
}

// RecommendationsClientAdapter adapts the internal recommendations client
type RecommendationsClientAdapter struct {
	client *internalCommon.RecommendationsClient
}

// NewRecommendationsClient creates a new recommendations client
func NewRecommendationsClient(cfg aws.Config) provider.RecommendationsClient {
	return &RecommendationsClientAdapter{
		client: internalCommon.NewRecommendationsClient(cfg),
	}
}

// GetRecommendations gets recommendations with filtering
func (r *RecommendationsClientAdapter) GetRecommendations(ctx context.Context, params common.RecommendationParams) ([]common.Recommendation, error) {
	// Convert parameters to internal format
	internalParams := internalCommon.RecommendationParams{
		Service:            convertServiceTypeToInternal(params.Service),
		LookbackPeriodDays: parseIntFromString(params.LookbackPeriod, 30),
		TermInYears:        parseTermToYears(params.Term),
		PaymentOption:      params.PaymentOption,
		AccountID:          "", // Will handle filtering after
	}

	// Get recommendations from internal client
	internalRecs, err := r.client.GetRecommendations(ctx, internalParams)
	if err != nil {
		return nil, err
	}

	// Convert to new format
	recommendations := make([]common.Recommendation, 0, len(internalRecs))
	for _, rec := range internalRecs {
		recommendations = append(recommendations, ConvertRecommendationFromInternal(rec))
	}

	// Apply filters
	if len(params.AccountFilter) > 0 {
		filtered := make([]common.Recommendation, 0)
		accountMap := make(map[string]bool)
		for _, acc := range params.AccountFilter {
			accountMap[acc] = true
		}
		for _, rec := range recommendations {
			if accountMap[rec.Account] {
				filtered = append(filtered, rec)
			}
		}
		recommendations = filtered
	}

	if len(params.IncludeRegions) > 0 {
		filtered := make([]common.Recommendation, 0)
		regionMap := make(map[string]bool)
		for _, region := range params.IncludeRegions {
			regionMap[region] = true
		}
		for _, rec := range recommendations {
			if regionMap[rec.Region] {
				filtered = append(filtered, rec)
			}
		}
		recommendations = filtered
	}

	if len(params.ExcludeRegions) > 0 {
		regionMap := make(map[string]bool)
		for _, region := range params.ExcludeRegions {
			regionMap[region] = true
		}
		filtered := make([]common.Recommendation, 0)
		for _, rec := range recommendations {
			if !regionMap[rec.Region] {
				filtered = append(filtered, rec)
			}
		}
		recommendations = filtered
	}

	return recommendations, nil
}

// GetRecommendationsForService gets recommendations for a specific service
func (r *RecommendationsClientAdapter) GetRecommendationsForService(ctx context.Context, service common.ServiceType) ([]common.Recommendation, error) {
	internalService := convertServiceTypeToInternal(service)
	internalRecs, err := r.client.GetRecommendationsForDiscovery(ctx, internalService)
	if err != nil {
		return nil, err
	}

	recommendations := make([]common.Recommendation, 0, len(internalRecs))
	for _, rec := range internalRecs {
		recommendations = append(recommendations, ConvertRecommendationFromInternal(rec))
	}

	return recommendations, nil
}

// GetAllRecommendations gets recommendations for all supported services
func (r *RecommendationsClientAdapter) GetAllRecommendations(ctx context.Context) ([]common.Recommendation, error) {
	services := []common.ServiceType{
		common.ServiceEC2,
		common.ServiceRDS,
		common.ServiceElastiCache,
		common.ServiceOpenSearch,
		common.ServiceRedshift,
	}

	allRecommendations := make([]common.Recommendation, 0)

	for _, service := range services {
		recs, err := r.GetRecommendationsForService(ctx, service)
		if err != nil {
			// Log error but continue with other services
			continue
		}
		allRecommendations = append(allRecommendations, recs...)

		// Add small delay between service queries to avoid rate limiting
		time.Sleep(100 * time.Millisecond)
	}

	return allRecommendations, nil
}

// Helper functions

func parseIntFromString(s string, defaultVal int) int {
	// Parse strings like "30d", "60d" to integers
	if len(s) == 0 {
		return defaultVal
	}
	// Simple parsing - extract digits
	var result int
	for _, c := range s {
		if c >= '0' && c <= '9' {
			result = result*10 + int(c-'0')
		}
	}
	if result == 0 {
		return defaultVal
	}
	return result
}

func parseTermToYears(term string) int {
	// Parse "1yr" or "3yr" to years
	if term == "3yr" || term == "3" {
		return 3
	}
	return 1
}
