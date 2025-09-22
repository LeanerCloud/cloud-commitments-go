package common

import (
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// RegionNameToCode maps AWS human-readable region names to region codes
var RegionNameToCode = map[string]string{
	"US East (N. Virginia)":     "us-east-1",
	"US East (Ohio)":            "us-east-2",
	"US West (N. California)":   "us-west-1",
	"US West (Oregon)":          "us-west-2",
	"Africa (Cape Town)":        "af-south-1",
	"Asia Pacific (Hong Kong)":  "ap-east-1",
	"Asia Pacific (Hyderabad)":  "ap-south-2",
	"Asia Pacific (Jakarta)":    "ap-southeast-3",
	"Asia Pacific (Melbourne)":  "ap-southeast-4",
	"Asia Pacific (Mumbai)":     "ap-south-1",
	"Asia Pacific (Osaka)":      "ap-northeast-3",
	"Asia Pacific (Seoul)":      "ap-northeast-2",
	"Asia Pacific (Singapore)":  "ap-southeast-1",
	"Asia Pacific (Sydney)":     "ap-southeast-2",
	"Asia Pacific (Tokyo)":      "ap-northeast-1",
	"Canada (Central)":          "ca-central-1",
	"Canada (West)":             "ca-west-1",
	"Europe (Frankfurt)":        "eu-central-1",
	"Europe (Ireland)":          "eu-west-1",
	"Europe (London)":           "eu-west-2",
	"Europe (Milan)":            "eu-south-1",
	"Europe (Paris)":            "eu-west-3",
	"Europe (Spain)":            "eu-south-2",
	"Europe (Stockholm)":        "eu-north-1",
	"Europe (Zurich)":           "eu-central-2",
	"Israel (Tel Aviv)":         "il-central-1",
	"Middle East (Bahrain)":     "me-south-1",
	"Middle East (UAE)":         "me-central-1",
	"South America (São Paulo)": "sa-east-1",
	"AWS GovCloud (US-East)":    "us-gov-east-1",
	"AWS GovCloud (US-West)":    "us-gov-west-1",
}

// NormalizeRegionName converts human-readable region names to AWS region codes
func NormalizeRegionName(regionName string) string {
	if regionName == "" {
		return ""
	}

	// First try exact match
	if code, exists := RegionNameToCode[regionName]; exists {
		return code
	}

	// If it's already a region code (lowercase with dashes), return as-is
	if IsRegionCode(regionName) {
		return regionName
	}

	// Try case-insensitive match
	for name, code := range RegionNameToCode {
		if strings.EqualFold(name, regionName) {
			return code
		}
	}

	// Try partial matching for common variations
	regionLower := strings.ToLower(regionName)

	// Handle common abbreviations and variations
	switch {
	case strings.Contains(regionLower, "virginia") || strings.Contains(regionLower, "n. virginia"):
		return "us-east-1"
	case strings.Contains(regionLower, "ohio"):
		return "us-east-2"
	case strings.Contains(regionLower, "california") || strings.Contains(regionLower, "n. california"):
		return "us-west-1"
	case strings.Contains(regionLower, "oregon"):
		return "us-west-2"
	case strings.Contains(regionLower, "ireland"):
		return "eu-west-1"
	case strings.Contains(regionLower, "frankfurt"):
		return "eu-central-1"
	case strings.Contains(regionLower, "london"):
		return "eu-west-2"
	case strings.Contains(regionLower, "paris"):
		return "eu-west-3"
	case strings.Contains(regionLower, "tokyo"):
		return "ap-northeast-1"
	case strings.Contains(regionLower, "singapore"):
		return "ap-southeast-1"
	case strings.Contains(regionLower, "sydney"):
		return "ap-southeast-2"
	case strings.Contains(regionLower, "mumbai"):
		return "ap-south-1"
	case strings.Contains(regionLower, "seoul"):
		return "ap-northeast-2"
	case strings.Contains(regionLower, "são paulo") || strings.Contains(regionLower, "sao paulo"):
		return "sa-east-1"
	}

	// If no match found, return the original
	return regionName
}

// IsRegionCode checks if a string looks like an AWS region code
func IsRegionCode(s string) bool {
	// AWS region codes are typically lowercase, contain dashes, and follow patterns like:
	// us-east-1, eu-west-1, ap-southeast-2, etc.
	return strings.Contains(s, "-") &&
		strings.ToLower(s) == s &&
		!strings.Contains(s, " ") &&
		!strings.Contains(s, "(") &&
		!strings.Contains(s, ")")
}

// ConvertPaymentOption converts string payment option to AWS SDK type
func ConvertPaymentOption(option string) types.PaymentOption {
	switch option {
	case "all-upfront":
		return types.PaymentOptionAllUpfront
	case "partial-upfront":
		return types.PaymentOptionPartialUpfront
	case "no-upfront":
		return types.PaymentOptionNoUpfront
	default:
		return types.PaymentOptionPartialUpfront
	}
}

// ConvertPaymentOptionToString converts payment option for API calls
func ConvertPaymentOptionToString(option string) string {
	switch option {
	case "all-upfront":
		return "All Upfront"
	case "partial-upfront":
		return "Partial Upfront"
	case "no-upfront":
		return "No Upfront"
	default:
		return "Partial Upfront"
	}
}

// ConvertTermInYears converts years to AWS SDK type
func ConvertTermInYears(years int) types.TermInYears {
	switch years {
	case 1:
		return types.TermInYearsOneYear
	case 3:
		return types.TermInYearsThreeYears
	default:
		return types.TermInYearsThreeYears
	}
}

// ConvertLookbackPeriod converts days to AWS SDK type
func ConvertLookbackPeriod(days int) types.LookbackPeriodInDays {
	switch days {
	case 7:
		return types.LookbackPeriodInDaysSevenDays
	case 30:
		return types.LookbackPeriodInDaysThirtyDays
	case 60:
		return types.LookbackPeriodInDaysSixtyDays
	default:
		return types.LookbackPeriodInDaysSevenDays
	}
}

// GetServiceStringForCostExplorer returns the service name string for Cost Explorer API
func GetServiceStringForCostExplorer(service ServiceType) string {
	switch service {
	case ServiceRDS:
		return "Amazon Relational Database Service"
	case ServiceElastiCache:
		return "Amazon ElastiCache"
	case ServiceEC2:
		return "Amazon Elastic Compute Cloud - Compute"
	case ServiceOpenSearch:
		return "Amazon OpenSearch Service"
	case ServiceElasticsearch:
		return "Amazon Elasticsearch Service"
	case ServiceRedshift:
		return "Amazon Redshift"
	case ServiceMemoryDB:
		return "Amazon MemoryDB Service"
	default:
		return string(service)
	}
}

// ApplyCoverage applies a coverage percentage to recommendations
func ApplyCoverage(recs []Recommendation, coverage float64) []Recommendation {
	if coverage >= 100.0 {
		return recs
	}

	filtered := make([]Recommendation, 0, len(recs))
	for _, rec := range recs {
		adjustedCount := int32(float64(rec.Count) * (coverage / 100.0))
		if adjustedCount > 0 {
			recCopy := rec
			recCopy.Count = adjustedCount
			filtered = append(filtered, recCopy)
		}
	}
	return filtered
}

// CalculateTotalSavings calculates the total estimated savings from recommendations
func CalculateTotalSavings(recs []Recommendation) float64 {
	total := 0.0
	for _, rec := range recs {
		// Calculate savings from cost and savings percent
		savings := rec.EstimatedCost * (rec.SavingsPercent / 100.0)
		total += savings
	}
	return total
}

// CalculateTotalInstances calculates the total number of instances in recommendations
func CalculateTotalInstances(recs []Recommendation) int32 {
	var total int32
	for _, rec := range recs {
		total += rec.Count
	}
	return total
}

// GroupRecommendationsByRegion groups recommendations by region
func GroupRecommendationsByRegion(recs []Recommendation) map[string][]Recommendation {
	grouped := make(map[string][]Recommendation)
	for _, rec := range recs {
		grouped[rec.Region] = append(grouped[rec.Region], rec)
	}
	return grouped
}

// GroupRecommendationsByService groups recommendations by service type
func GroupRecommendationsByService(recs []Recommendation) map[ServiceType][]Recommendation {
	grouped := make(map[ServiceType][]Recommendation)
	for _, rec := range recs {
		grouped[rec.Service] = append(grouped[rec.Service], rec)
	}
	return grouped
}

// FilterRecommendationsByThreshold filters recommendations by minimum savings threshold
func FilterRecommendationsByThreshold(recs []Recommendation, threshold float64) []Recommendation {
	filtered := make([]Recommendation, 0)
	for _, rec := range recs {
		// Calculate savings from cost and savings percent
		savings := rec.EstimatedCost * (rec.SavingsPercent / 100.0)
		if savings >= threshold {
			filtered = append(filtered, rec)
		}
	}
	return filtered
}

// SortRecommendationsBySavings sorts recommendations by estimated savings (descending)
func SortRecommendationsBySavings(recs []Recommendation) []Recommendation {
	// Create a copy to avoid modifying the original slice
	sorted := make([]Recommendation, len(recs))
	copy(sorted, recs)

	// Sort by savings in descending order
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			savingsI := sorted[i].EstimatedCost * (sorted[i].SavingsPercent / 100.0)
			savingsJ := sorted[j].EstimatedCost * (sorted[j].SavingsPercent / 100.0)
			if savingsJ > savingsI {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	return sorted
}

// MergeRecommendations merges two slices of recommendations
func MergeRecommendations(recsA, recsB []Recommendation) []Recommendation {
	merged := make([]Recommendation, 0, len(recsA)+len(recsB))
	merged = append(merged, recsA...)
	merged = append(merged, recsB...)
	return merged
}

// ValidateRecommendation checks if a recommendation has all required fields
func ValidateRecommendation(rec Recommendation) bool {
	if rec.Region == "" {
		return false
	}
	if rec.InstanceType == "" {
		return false
	}
	if rec.Count <= 0 {
		return false
	}
	return true
}