package common

import (
	"fmt"
	"time"
)

// ServiceType represents the AWS service type for RI recommendations
type ServiceType string

const (
	ServiceRDS         ServiceType = "Amazon Relational Database Service"
	ServiceElastiCache ServiceType = "Amazon ElastiCache"
	ServiceEC2         ServiceType = "Amazon Elastic Compute Cloud"
	ServiceOpenSearch  ServiceType = "Amazon OpenSearch Service"
	ServiceElasticsearch ServiceType = "Amazon Elasticsearch Service" // Legacy name
	ServiceRedshift    ServiceType = "Amazon Redshift"
	ServiceMemoryDB    ServiceType = "Amazon MemoryDB"
)

// ServiceDetails is an interface that all service-specific details must implement
type ServiceDetails interface {
	// GetServiceType returns the service type for this detail
	GetServiceType() ServiceType
	// GetDetailDescription returns a service-specific description
	GetDetailDescription() string
}

// Recommendation represents a generic Reserved Instance recommendation
type Recommendation struct {
	Service        ServiceType
	Region         string
	InstanceType   string
	Count          int32
	PaymentOption  string
	Term           int // in months
	EstimatedCost  float64
	SavingsPercent float64
	Timestamp      time.Time
	Description    string

	// Service-specific details
	ServiceDetails ServiceDetails
}

// RDSDetails contains RDS-specific recommendation details
type RDSDetails struct {
	Engine   string // aurora-mysql, postgres, mysql, mariadb, oracle, sqlserver
	AZConfig string // single-az or multi-az
}

// GetServiceType returns the service type
func (r *RDSDetails) GetServiceType() ServiceType {
	return ServiceRDS
}

// GetDetailDescription returns a service-specific description
func (r *RDSDetails) GetDetailDescription() string {
	return fmt.Sprintf("%s %s", r.Engine, r.AZConfig)
}

// ElastiCacheDetails contains ElastiCache-specific recommendation details
type ElastiCacheDetails struct {
	Engine   string // redis or memcached
	NodeType string
}

// GetServiceType returns the service type
func (e *ElastiCacheDetails) GetServiceType() ServiceType {
	return ServiceElastiCache
}

// GetDetailDescription returns a service-specific description
func (e *ElastiCacheDetails) GetDetailDescription() string {
	return fmt.Sprintf("%s", e.Engine)
}

// EC2Details contains EC2-specific recommendation details
type EC2Details struct {
	Platform string // Linux/UNIX, Windows, RHEL, SUSE, etc.
	Tenancy  string // shared, dedicated, host
	Scope    string // region or availability-zone
}

// GetServiceType returns the service type
func (e *EC2Details) GetServiceType() ServiceType {
	return ServiceEC2
}

// GetDetailDescription returns a service-specific description
func (e *EC2Details) GetDetailDescription() string {
	return fmt.Sprintf("%s %s %s", e.Platform, e.Tenancy, e.Scope)
}

// OpenSearchDetails contains OpenSearch-specific recommendation details
type OpenSearchDetails struct {
	InstanceType   string
	InstanceCount  int32
	MasterEnabled  bool
	MasterType     string
	MasterCount    int32
	DataNodeStorage int32 // in GB
}

// GetServiceType returns the service type
func (o *OpenSearchDetails) GetServiceType() ServiceType {
	return ServiceOpenSearch
}

// GetDetailDescription returns a service-specific description
func (o *OpenSearchDetails) GetDetailDescription() string {
	desc := fmt.Sprintf("%s x%d", o.InstanceType, o.InstanceCount)
	if o.MasterEnabled {
		desc += fmt.Sprintf(" (Master: %s x%d)", o.MasterType, o.MasterCount)
	}
	return desc
}

// RedshiftDetails contains Redshift-specific recommendation details
type RedshiftDetails struct {
	NodeType   string // dc2.large, ra3.4xlarge, etc.
	NumberOfNodes int32
	ClusterType string // single-node or multi-node
}

// GetServiceType returns the service type
func (r *RedshiftDetails) GetServiceType() ServiceType {
	return ServiceRedshift
}

// GetDetailDescription returns a service-specific description
func (r *RedshiftDetails) GetDetailDescription() string {
	return fmt.Sprintf("%s %d-node %s", r.NodeType, r.NumberOfNodes, r.ClusterType)
}

// MemoryDBDetails contains MemoryDB-specific recommendation details
type MemoryDBDetails struct {
	NodeType      string
	NumberOfNodes int32
	ShardCount    int32
}

// GetServiceType returns the service type
func (m *MemoryDBDetails) GetServiceType() ServiceType {
	return ServiceMemoryDB
}

// GetDetailDescription returns a service-specific description
func (m *MemoryDBDetails) GetDetailDescription() string {
	return fmt.Sprintf("%s %d-node %d-shard", m.NodeType, m.NumberOfNodes, m.ShardCount)
}

// GetDescription returns a human-readable description of the recommendation
func (r *Recommendation) GetDescription() string {
	switch details := r.ServiceDetails.(type) {
	case *RDSDetails:
		return fmt.Sprintf("%s %s %s %dx", details.Engine, r.InstanceType, details.AZConfig, r.Count)
	case *ElastiCacheDetails:
		return fmt.Sprintf("%s %s %dx", details.Engine, r.InstanceType, r.Count)
	case *EC2Details:
		return fmt.Sprintf("%s %s %s %dx", details.Platform, r.InstanceType, details.Tenancy, r.Count)
	case *OpenSearchDetails:
		desc := fmt.Sprintf("OpenSearch %s %dx", details.InstanceType, details.InstanceCount)
		if details.MasterEnabled {
			desc += fmt.Sprintf(" (Master: %s %dx)", details.MasterType, details.MasterCount)
		}
		return desc
	case *RedshiftDetails:
		return fmt.Sprintf("Redshift %s %d-node %s", details.NodeType, details.NumberOfNodes, details.ClusterType)
	case *MemoryDBDetails:
		return fmt.Sprintf("MemoryDB %s %d-node %d-shard", details.NodeType, details.NumberOfNodes, details.ShardCount)
	default:
		return fmt.Sprintf("%s %dx", r.InstanceType, r.Count)
	}
}

// GetServiceName returns the short name of the service
func (r *Recommendation) GetServiceName() string {
	switch r.Service {
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
		return "Unknown"
	}
}

// GetMultiAZ returns whether this is a multi-AZ configuration (RDS specific)
func (r *Recommendation) GetMultiAZ() bool {
	if details, ok := r.ServiceDetails.(*RDSDetails); ok {
		return details.AZConfig == "multi-az"
	}
	return false
}

// GetDurationString converts term months to a duration string (for RDS API)
func (r *Recommendation) GetDurationString() string {
	years := r.Term / 12
	if years == 1 {
		return "31536000" // 1 year in seconds
	}
	return "94608000" // 3 years in seconds
}

// PurchaseResult represents the result of a RI purchase attempt
type PurchaseResult struct {
	Config        Recommendation
	Success       bool
	PurchaseID    string
	ReservationID string
	Message       string
	ActualCost    float64
	Timestamp     time.Time
}

// RecommendationParams contains parameters for fetching recommendations
type RecommendationParams struct {
	Service            ServiceType
	Region             string
	AccountID          string
	PaymentOption      string
	TermInYears        int
	LookbackPeriodDays int
}

// RegionProcessingStats holds statistics for each region processed
type RegionProcessingStats struct {
	Region                  string
	Service                 ServiceType
	Success                 bool
	ErrorMessage            string
	RecommendationsFound    int
	RecommendationsSelected int
	InstancesProcessed      int32
	SuccessfulPurchases     int
	FailedPurchases         int
}

// CostEstimate represents the cost estimate for a recommendation
type CostEstimate struct {
	Recommendation   Recommendation
	TotalFixedCost   float64
	MonthlyUsageCost float64
	TotalTermCost    float64
	Error            string
}

// OfferingDetails contains details about a Reserved Instance offering
type OfferingDetails struct {
	OfferingID    string
	InstanceType  string
	Engine        string  // For RDS/ElastiCache/MemoryDB
	Platform      string  // For EC2
	NodeType      string  // For Redshift
	Duration      string
	PaymentOption string
	MultiAZ       bool    // For RDS
	FixedPrice    float64
	UsagePrice    float64
	CurrencyCode  string
	OfferingType  string
}