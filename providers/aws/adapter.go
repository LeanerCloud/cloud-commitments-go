// Package aws provides adapters between old internal types and new pkg types
package aws

import (
	"github.com/LeanerCloud/CUDly/pkg/common"
	internalCommon "github.com/LeanerCloud/CUDly/internal/common"
)

// ConvertRecommendationToInternal converts new common.Recommendation to internal Recommendation
func ConvertRecommendationToInternal(rec common.Recommendation) internalCommon.Recommendation {
	// Convert term string to months
	termMonths := 12 // default 1 year
	if rec.Term == "3yr" || rec.Term == "3" {
		termMonths = 36
	}

	internal := internalCommon.Recommendation{
		Service:       convertServiceTypeToInternal(rec.Service),
		Region:        rec.Region,
		AccountID:     rec.Account,
		AccountName:   rec.AccountName,
		InstanceType:  rec.ResourceType,
		Count:         int32(rec.Count),
		Term:          termMonths,
		PaymentType:   rec.PaymentOption,
		PaymentOption: rec.PaymentOption,
	}

	// Convert service-specific details
	if rec.Details != nil {
		internal.ServiceDetails = convertDetailsToInternal(rec.Details)
	}

	return internal
}

// ConvertRecommendationFromInternal converts internal Recommendation to new common.Recommendation
func ConvertRecommendationFromInternal(internal internalCommon.Recommendation) common.Recommendation {
	// Convert term from months to string
	termStr := "1yr"
	if internal.Term >= 36 {
		termStr = "3yr"
	}

	rec := common.Recommendation{
		Provider:          common.ProviderAWS,
		Service:           convertServiceTypeFromInternal(internal.Service),
		Region:            internal.Region,
		Account:           internal.AccountID,
		AccountName:       internal.AccountName,
		ResourceType:      internal.InstanceType,
		Count:             int(internal.Count),
		Term:              termStr,
		PaymentOption:     internal.PaymentType,
		CommitmentType:    common.CommitmentReservedInstance,
		OnDemandCost:      internal.CurrentCost,
		CommitmentCost:    internal.EstimatedCost,
		EstimatedSavings:  internal.EstimatedSavings,
		SavingsPercentage: internal.SavingsPercentage,
	}

	// Convert service-specific details
	if internal.ServiceDetails != nil {
		rec.Details = convertDetailsFromInternal(internal.ServiceDetails)
	}

	return rec
}

// ConvertPurchaseResultFromInternal converts internal PurchaseResult to new common.PurchaseResult
func ConvertPurchaseResultFromInternal(internal internalCommon.PurchaseResult) common.PurchaseResult {
	return common.PurchaseResult{
		Recommendation: ConvertRecommendationFromInternal(internal.Config),
		Success:        internal.Success,
		CommitmentID:   internal.ReservationID,
		Error:          nil, // Error is in Message field in internal
		Cost:           internal.Cost,
		DryRun:         false,
		Timestamp:      internal.Timestamp,
	}
}

// ConvertCommitmentFromInternal converts internal ExistingRI to new common.Commitment
func ConvertCommitmentFromInternal(internal internalCommon.ExistingRI) common.Commitment {
	return common.Commitment{
		Provider:       common.ProviderAWS,
		Account:        "",
		CommitmentID:   internal.ReservationID,
		CommitmentType: common.CommitmentReservedInstance,
		Service:        convertServiceTypeFromInternal(internal.Service),
		Region:         internal.Region,
		ResourceType:   internal.InstanceType,
		Count:          int(internal.Count),
		StartDate:      internal.StartDate,
		EndDate:        internal.EndDate,
		State:          internal.State,
		Cost:           0,
	}
}

// ConvertOfferingDetailsFromInternal converts internal OfferingDetails to new common.OfferingDetails
func ConvertOfferingDetailsFromInternal(internal *internalCommon.OfferingDetails) *common.OfferingDetails {
	if internal == nil {
		return nil
	}

	return &common.OfferingDetails{
		OfferingID:          internal.OfferingID,
		ResourceType:        internal.InstanceType,
		Term:                internal.Term,
		PaymentOption:       internal.PaymentOption,
		UpfrontCost:         internal.UpfrontCost,
		RecurringCost:       internal.RecurringCost,
		TotalCost:           internal.TotalCost,
		EffectiveHourlyRate: internal.EffectiveHourlyRate,
		Currency:            internal.Currency,
	}
}

// convertServiceTypeToInternal converts new ServiceType to internal ServiceType
func convertServiceTypeToInternal(service common.ServiceType) internalCommon.ServiceType {
	switch service {
	case common.ServiceCompute, common.ServiceEC2:
		return internalCommon.ServiceEC2
	case common.ServiceRelationalDB, common.ServiceRDS:
		return internalCommon.ServiceRDS
	case common.ServiceCache, common.ServiceElastiCache:
		return internalCommon.ServiceElastiCache
	case common.ServiceSearch, common.ServiceOpenSearch:
		return internalCommon.ServiceOpenSearch
	case common.ServiceDataWarehouse, common.ServiceRedshift:
		return internalCommon.ServiceRedshift
	case common.ServiceMemoryDB:
		return internalCommon.ServiceMemoryDB
	default:
		return internalCommon.ServiceEC2
	}
}

// convertServiceTypeFromInternal converts internal ServiceType to new ServiceType
func convertServiceTypeFromInternal(service internalCommon.ServiceType) common.ServiceType {
	switch service {
	case internalCommon.ServiceEC2:
		return common.ServiceEC2
	case internalCommon.ServiceRDS:
		return common.ServiceRDS
	case internalCommon.ServiceElastiCache:
		return common.ServiceElastiCache
	case internalCommon.ServiceOpenSearch:
		return common.ServiceOpenSearch
	case internalCommon.ServiceRedshift:
		return common.ServiceRedshift
	case internalCommon.ServiceMemoryDB:
		return common.ServiceMemoryDB
	default:
		return common.ServiceEC2
	}
}

// convertDetailsToInternal converts new ServiceDetails to internal ServiceDetails
func convertDetailsToInternal(details common.ServiceDetails) internalCommon.ServiceDetails {
	switch d := details.(type) {
	case common.ComputeDetails:
		return &internalCommon.EC2Details{
			Platform: d.Platform,
			Tenancy:  d.Tenancy,
			Scope:    d.Scope,
		}
	case common.DatabaseDetails:
		return &internalCommon.RDSDetails{
			Engine:   d.Engine,
			AZConfig: d.AZConfig,
		}
	case common.CacheDetails:
		return &internalCommon.ElastiCacheDetails{
			Engine:   d.Engine,
			NodeType: d.NodeType,
		}
	case common.SearchDetails:
		return &internalCommon.OpenSearchDetails{
			InstanceType:  d.InstanceType,
			InstanceCount: 0, // Not available in pkg/common SearchDetails
			MasterEnabled: d.MasterNodeCount > 0,
			MasterType:    d.MasterNodeType,
			MasterCount:   int32(d.MasterNodeCount),
		}
	case common.DataWarehouseDetails:
		return &internalCommon.RedshiftDetails{
			NodeType:      d.NodeType,
			NumberOfNodes: int32(d.NumberOfNodes),
			ClusterType:   d.ClusterType,
		}
	default:
		return nil
	}
}

// convertDetailsFromInternal converts internal ServiceDetails to new ServiceDetails
func convertDetailsFromInternal(details internalCommon.ServiceDetails) common.ServiceDetails {
	switch d := details.(type) {
	case *internalCommon.EC2Details:
		return common.ComputeDetails{
			InstanceType: "",
			Platform:     d.Platform,
			Tenancy:      d.Tenancy,
			Scope:        d.Scope,
		}
	case *internalCommon.RDSDetails:
		return common.DatabaseDetails{
			Engine:   d.Engine,
			AZConfig: d.AZConfig,
		}
	case *internalCommon.ElastiCacheDetails:
		return common.CacheDetails{
			Engine:   d.Engine,
			NodeType: d.NodeType,
		}
	case *internalCommon.OpenSearchDetails:
		return common.SearchDetails{
			InstanceType:    d.InstanceType,
			MasterNodeCount: int(d.MasterCount),
			MasterNodeType:  d.MasterType,
		}
	case *internalCommon.RedshiftDetails:
		return common.DataWarehouseDetails{
			NodeType:      d.NodeType,
			NumberOfNodes: int(d.NumberOfNodes),
			ClusterType:   d.ClusterType,
		}
	default:
		return nil
	}
}
