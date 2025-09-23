package common

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// ProcessorConfig contains configuration for the multi-service processor
type ProcessorConfig struct {
	Services       []ServiceType
	Regions        []string
	Coverage       float64
	IsDryRun       bool
	OutputPath     string
}

// ServiceProcessor handles processing of multiple services
type ServiceProcessor struct {
	config    ProcessorConfig
	awsConfig aws.Config
	recClient RecommendationsClientInterface
}

// NewServiceProcessor creates a new service processor
func NewServiceProcessor(cfg aws.Config, config ProcessorConfig) *ServiceProcessor {
	return &ServiceProcessor{
		config:    config,
		awsConfig: cfg,
		recClient: NewRecommendationsClient(cfg),
	}
}

// ProcessAllServices processes recommendations and purchases for all configured services
func (p *ServiceProcessor) ProcessAllServices(ctx context.Context) ([]Recommendation, []PurchaseResult, map[ServiceType]ServiceStats) {
	allRecommendations := make([]Recommendation, 0)
	allResults := make([]PurchaseResult, 0)
	serviceStats := make(map[ServiceType]ServiceStats)

	for _, service := range p.config.Services {
		fmt.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		fmt.Printf("🎯 Processing %s\n", GetServiceDisplayName(service))
		fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		serviceRecs, serviceResults := p.processService(ctx, service)
		allRecommendations = append(allRecommendations, serviceRecs...)
		allResults = append(allResults, serviceResults...)

		stats := p.calculateServiceStats(service, serviceRecs, serviceResults)
		serviceStats[service] = stats
		p.printServiceSummary(service, stats)
	}

	return allRecommendations, allResults, serviceStats
}

// processService processes a single service across all regions
func (p *ServiceProcessor) processService(ctx context.Context, service ServiceType) ([]Recommendation, []PurchaseResult) {
	// Auto-discover regions if none specified
	regionsToProcess := p.config.Regions
	if len(regionsToProcess) == 0 {
		fmt.Printf("🔍 Auto-discovering regions for %s...\n", GetServiceDisplayName(service))
		discoveredRegions, err := p.discoverRegionsForService(ctx, service)
		if err != nil {
			log.Printf("❌ Failed to discover regions: %v", err)
			return nil, nil
		}

		if len(discoveredRegions) == 0 {
			fmt.Printf("ℹ️  No regions with %s RI recommendations found\n", GetServiceDisplayName(service))
			return nil, nil
		}

		regionsToProcess = discoveredRegions
		fmt.Printf("✅ Found %d region(s) with recommendations: %s\n",
			len(regionsToProcess), strings.Join(regionsToProcess, ", "))
	}

	serviceRecs := make([]Recommendation, 0)
	serviceResults := make([]PurchaseResult, 0)

	for i, region := range regionsToProcess {
		fmt.Printf("\n  📍 [%d/%d] Region: %s\n", i+1, len(regionsToProcess), region)

		// Fetch recommendations
		params := RecommendationParams{
			Service:            service,
			Region:             region,
			PaymentOption:      "partial-upfront",
			TermInYears:        3,
			LookbackPeriodDays: 7,
		}

		recs, err := p.recClient.GetRecommendations(ctx, params)
		if err != nil {
			log.Printf("  ❌ Failed to fetch recommendations: %v", err)
			continue
		}

		if len(recs) == 0 {
			fmt.Printf("  ℹ️  No recommendations found\n")
			continue
		}

		fmt.Printf("  ✅ Found %d recommendations\n", len(recs))

		// Apply coverage
		filteredRecs := p.applyCoverage(recs)
		fmt.Printf("  📈 Applying %.1f%% coverage: %d recommendations selected\n", p.config.Coverage, len(filteredRecs))

		serviceRecs = append(serviceRecs, filteredRecs...)

		// Get purchase client
		regionalCfg := p.awsConfig.Copy()
		regionalCfg.Region = region
		purchaseClient := p.createPurchaseClient(service, regionalCfg)

		if purchaseClient == nil {
			fmt.Printf("  ⚠️  Purchase client not yet implemented for %s\n", GetServiceDisplayName(service))
			continue
		}

		// Process purchases
		for j, rec := range filteredRecs {
			fmt.Printf("    [%d/%d] Processing: %s\n", j+1, len(filteredRecs), rec.Description)

			var result PurchaseResult
			if p.config.IsDryRun {
				result = PurchaseResult{
					Config:     rec,
					Success:    true,
					PurchaseID: p.generatePurchaseID(rec, region, j+1),
					Message:    "Dry run - no actual purchase",
					Timestamp:  time.Now(),
				}
			} else {
				result = purchaseClient.PurchaseRI(ctx, rec)
				if result.PurchaseID == "" {
					result.PurchaseID = p.generatePurchaseID(rec, region, j+1)
				}
				if j < len(filteredRecs)-1 {
					time.Sleep(2 * time.Second)
				}
			}

			serviceResults = append(serviceResults, result)

			if result.Success {
				fmt.Printf("    ✅ Success: %s\n", result.Message)
			} else {
				fmt.Printf("    ❌ Failed: %s\n", result.Message)
			}
		}
	}

	return serviceRecs, serviceResults
}

// discoverRegionsForService discovers regions with recommendations for a service
func (p *ServiceProcessor) discoverRegionsForService(ctx context.Context, service ServiceType) ([]string, error) {
	recs, err := p.recClient.GetRecommendationsForDiscovery(ctx, service)
	if err != nil {
		return nil, err
	}

	regionSet := make(map[string]bool)
	for _, rec := range recs {
		if rec.Region != "" {
			regionSet[rec.Region] = true
		}
	}

	regions := make([]string, 0, len(regionSet))
	for region := range regionSet {
		regions = append(regions, region)
	}

	sort.Strings(regions)
	return regions, nil
}

// applyCoverage applies the coverage percentage to recommendations
func (p *ServiceProcessor) applyCoverage(recs []Recommendation) []Recommendation {
	return ApplyCoverage(recs, p.config.Coverage)
}

// generatePurchaseID generates a unique purchase ID
func (p *ServiceProcessor) generatePurchaseID(rec Recommendation, region string, index int) string {
	timestamp := time.Now().Format("20060102-150405")
	prefix := "ri"
	if p.config.IsDryRun {
		prefix = "dryrun"
	}

	service := strings.ToLower(rec.GetServiceName())
	instanceType := strings.ReplaceAll(rec.InstanceType, ".", "-")

	return fmt.Sprintf("%s-%s-%s-%s-%dx-%s-%03d",
		prefix, service, region, instanceType, rec.Count, timestamp, index)
}

// createPurchaseClient creates the appropriate purchase client for a service
func (p *ServiceProcessor) createPurchaseClient(service ServiceType, cfg aws.Config) PurchaseClient {
	// This will be implemented by the main package to avoid circular dependencies
	// The main package will set up a factory function
	if purchaseClientFactory != nil {
		return purchaseClientFactory(service, cfg)
	}
	return nil
}

// PurchaseClientFactory is a function type for creating purchase clients
type PurchaseClientFactory func(service ServiceType, cfg aws.Config) PurchaseClient

// purchaseClientFactory is set by the main package
var purchaseClientFactory PurchaseClientFactory

// SetPurchaseClientFactory sets the factory function for creating purchase clients
func SetPurchaseClientFactory(factory PurchaseClientFactory) {
	purchaseClientFactory = factory
}

// ServiceStats holds statistics for a service
type ServiceStats struct {
	Service                 ServiceType
	RegionsProcessed        int
	RecommendationsFound    int
	RecommendationsSelected int
	InstancesProcessed      int32
	SuccessfulPurchases     int
	FailedPurchases         int
	TotalEstimatedSavings   float64
}

// calculateServiceStats calculates statistics for a service
func (p *ServiceProcessor) calculateServiceStats(service ServiceType, recs []Recommendation, results []PurchaseResult) ServiceStats {
	stats := ServiceStats{
		Service:                 service,
		RecommendationsFound:    len(recs),
		RecommendationsSelected: len(recs),
	}

	regionSet := make(map[string]bool)
	for _, rec := range recs {
		regionSet[rec.Region] = true
		stats.InstancesProcessed += rec.Count
		stats.TotalEstimatedSavings += rec.EstimatedCost
	}
	stats.RegionsProcessed = len(regionSet)

	for _, result := range results {
		if result.Success {
			stats.SuccessfulPurchases++
		} else {
			stats.FailedPurchases++
		}
	}

	return stats
}

// printServiceSummary prints a summary for a service
func (p *ServiceProcessor) printServiceSummary(service ServiceType, stats ServiceStats) {
	fmt.Printf("\n📊 %s Summary:\n", GetServiceDisplayName(service))
	fmt.Printf("  Regions processed: %d\n", stats.RegionsProcessed)
	fmt.Printf("  Recommendations: %d\n", stats.RecommendationsSelected)
	fmt.Printf("  Instances: %d\n", stats.InstancesProcessed)
	fmt.Printf("  Successful: %d, Failed: %d\n", stats.SuccessfulPurchases, stats.FailedPurchases)
	if stats.TotalEstimatedSavings > 0 {
		fmt.Printf("  Estimated monthly savings: $%.2f\n", stats.TotalEstimatedSavings)
	}
}

// GetServiceDisplayName returns a human-readable name for a service
func GetServiceDisplayName(service ServiceType) string {
	switch service {
	case ServiceRDS:
		return "RDS"
	case ServiceElastiCache:
		return "ElastiCache"
	case ServiceEC2:
		return "EC2"
	case ServiceOpenSearch, ServiceElasticsearch:
		return "OpenSearch"
	case ServiceRedshift:
		return "Redshift"
	case ServiceMemoryDB:
		return "MemoryDB"
	default:
		return string(service)
	}
}

// PrintFinalSummary prints the final summary of all operations
func PrintFinalSummary(allRecommendations []Recommendation, allResults []PurchaseResult, serviceStats map[ServiceType]ServiceStats, isDryRun bool) {
	fmt.Println("\n🎯 Final Summary:")
	fmt.Println("==========================================")

	if isDryRun {
		fmt.Println("Mode: DRY RUN")
	} else {
		fmt.Println("Mode: ACTUAL PURCHASE")
	}

	// Overall statistics
	totalRecommendations := len(allRecommendations)
	totalSuccessful := 0
	totalFailed := 0
	totalInstances := int32(0)
	totalSavings := float64(0)

	for _, result := range allResults {
		if result.Success {
			totalSuccessful++
			totalInstances += result.Config.Count
		} else {
			totalFailed++
		}
	}

	for _, stats := range serviceStats {
		totalSavings += stats.TotalEstimatedSavings
	}

	fmt.Printf("Total services processed: %d\n", len(serviceStats))
	fmt.Printf("Total recommendations: %d\n", totalRecommendations)
	fmt.Printf("Successful operations: %d\n", totalSuccessful)
	fmt.Printf("Failed operations: %d\n", totalFailed)
	fmt.Printf("Total instances: %d\n", totalInstances)
	if totalSavings > 0 {
		fmt.Printf("Total estimated monthly savings: $%.2f\n", totalSavings)
	}

	// Service breakdown
	if len(serviceStats) > 0 {
		fmt.Println("\n📊 By Service:")
		fmt.Println("--------------------------------------------------")
		for service, stats := range serviceStats {
			fmt.Printf("%-15s | Recs: %3d | Instances: %3d | Success: %3d | Failed: %3d\n",
				GetServiceDisplayName(service),
				stats.RecommendationsSelected,
				stats.InstancesProcessed,
				stats.SuccessfulPurchases,
				stats.FailedPurchases)
		}
	}

	// Success rate
	if len(allResults) > 0 {
		successRate := (float64(totalSuccessful) / float64(len(allResults))) * 100
		fmt.Printf("\nOverall success rate: %.1f%%\n", successRate)
	}

	if isDryRun {
		fmt.Println("\n💡 To actually purchase these RIs, run with --purchase flag")
	} else if totalSuccessful > 0 {
		fmt.Println("\n🎉 Purchase operations completed!")
		fmt.Println("⏰ Allow up to 15 minutes for RIs to appear in your account")
	}
}