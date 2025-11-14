package common

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// CostExplorerAPI defines the interface for Cost Explorer operations
type CostExplorerAPI interface {
	GetReservationPurchaseRecommendation(ctx context.Context, params *costexplorer.GetReservationPurchaseRecommendationInput, optFns ...func(*costexplorer.Options)) (*costexplorer.GetReservationPurchaseRecommendationOutput, error)
	GetSavingsPlansPurchaseRecommendation(ctx context.Context, params *costexplorer.GetSavingsPlansPurchaseRecommendationInput, optFns ...func(*costexplorer.Options)) (*costexplorer.GetSavingsPlansPurchaseRecommendationOutput, error)
}

// RecommendationsClientInterface defines the interface for recommendations client operations
type RecommendationsClientInterface interface {
	GetRecommendations(ctx context.Context, params RecommendationParams) ([]Recommendation, error)
	GetRecommendationsForDiscovery(ctx context.Context, service ServiceType) ([]Recommendation, error)
}

// RecommendationsClient wraps the AWS Cost Explorer client for RI recommendations
type RecommendationsClient struct {
	costExplorerClient CostExplorerAPI
	region             string
	rateLimiter        *RateLimiter
}

// NewRecommendationsClient creates a new recommendations client
func NewRecommendationsClient(cfg aws.Config) *RecommendationsClient {
	// Force Cost Explorer to use us-east-1 with explicit endpoint
	ceConfig := cfg.Copy()
	ceConfig.Region = "us-east-1"
	ceConfig.BaseEndpoint = aws.String("https://ce.us-east-1.amazonaws.com")

	return &RecommendationsClient{
		costExplorerClient: costexplorer.NewFromConfig(ceConfig),
		region:             cfg.Region,
		rateLimiter:        NewRateLimiter(),
	}
}

// NewRecommendationsClientWithAPI creates a new recommendations client with a custom Cost Explorer API
// This is primarily used for testing with mocked clients
func NewRecommendationsClientWithAPI(api CostExplorerAPI, region string) *RecommendationsClient {
	return &RecommendationsClient{
		costExplorerClient: api,
		region:             region,
		rateLimiter:        NewRateLimiter(),
	}
}

// NewRecommendationsClientWithAPIAndRateLimiter creates a new client with a provided API client and rate limiter (for testing)
func NewRecommendationsClientWithAPIAndRateLimiter(api CostExplorerAPI, region string, rateLimiter *RateLimiter) *RecommendationsClient {
	return &RecommendationsClient{
		costExplorerClient: api,
		region:             region,
		rateLimiter:        rateLimiter,
	}
}

// GetRecommendations fetches Reserved Instance recommendations for any service
func (c *RecommendationsClient) GetRecommendations(ctx context.Context, params RecommendationParams) ([]Recommendation, error) {
	// Handle Savings Plans separately as they use a different API
	if params.Service == ServiceSavingsPlans {
		return c.getSavingsPlansRecommendations(ctx, params)
	}

	input := &costexplorer.GetReservationPurchaseRecommendationInput{
		Service:              aws.String(GetServiceStringForCostExplorer(params.Service)),
		PaymentOption:        ConvertPaymentOption(params.PaymentOption),
		TermInYears:          ConvertTermInYears(params.TermInYears),
		LookbackPeriodInDays: ConvertLookbackPeriod(params.LookbackPeriodDays),
		AccountScope:         types.AccountScopeLinked, // Get recommendations broken down by linked account
	}

	// Add account ID filter if specified
	if params.AccountID != "" {
		input.AccountId = aws.String(params.AccountID)
	}

	// Implement rate limiting with exponential backoff
	var result *costexplorer.GetReservationPurchaseRecommendationOutput
	var err error

	c.rateLimiter.Reset()
	for {
		// Wait if this is a retry
		if waitErr := c.rateLimiter.Wait(ctx); waitErr != nil {
			return nil, fmt.Errorf("rate limiter wait failed: %w", waitErr)
		}

		result, err = c.costExplorerClient.GetReservationPurchaseRecommendation(ctx, input)
		if !c.rateLimiter.ShouldRetry(err) {
			break
		}
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get RI recommendations after %d retries: %w", c.rateLimiter.GetRetryCount(), err)
	}

	return c.parseRecommendations(result.Recommendations, params)
}

// parseRecommendations converts AWS recommendations to our internal format
func (c *RecommendationsClient) parseRecommendations(awsRecs []types.ReservationPurchaseRecommendation, params RecommendationParams) ([]Recommendation, error) {
	var recommendations []Recommendation

	for _, awsRec := range awsRecs {
		// Process ALL recommendation details
		for i, details := range awsRec.RecommendationDetails {
			rec, err := c.parseRecommendationDetail(awsRec, &details, params)
			if err != nil {
				// Log error but continue processing other recommendations
				fmt.Printf("Warning: Failed to parse recommendation detail %d: %v\n", i, err)
				continue
			}

			if rec != nil {
				recommendations = append(recommendations, *rec)
			}
		}
	}

	return recommendations, nil
}

// parseRecommendationDetail converts a single AWS recommendation detail to our format
func (c *RecommendationsClient) parseRecommendationDetail(awsRec types.ReservationPurchaseRecommendation, details *types.ReservationPurchaseRecommendationDetail, params RecommendationParams) (*Recommendation, error) {
	// awsRec parameter reserved for future use (metadata, summary info, etc.)
	_ = awsRec

	var rec Recommendation
	rec.Service = params.Service
	rec.PaymentOption = params.PaymentOption
	rec.Term = params.TermInYears * 12
	rec.Timestamp = time.Now()

	// Parse recommended quantity
	count, err := c.parseRecommendedQuantity(details)
	if err != nil {
		return nil, fmt.Errorf("failed to parse recommended quantity: %w", err)
	}
	rec.Count = count

	// Parse cost information
	rec.EstimatedCost, rec.SavingsPercent, err = c.parseCostInformation(details)
	if err != nil {
		return nil, fmt.Errorf("failed to parse cost information: %w", err)
	}

	// Parse AWS-provided cost details
	if details.UpfrontCost != nil {
		if upfront, err := strconv.ParseFloat(*details.UpfrontCost, 64); err == nil {
			rec.UpfrontCost = upfront
		}
	}
	if details.RecurringStandardMonthlyCost != nil {
		if monthly, err := strconv.ParseFloat(*details.RecurringStandardMonthlyCost, 64); err == nil {
			rec.RecurringMonthlyCost = monthly
		}
	}
	if details.EstimatedMonthlyOnDemandCost != nil {
		if onDemand, err := strconv.ParseFloat(*details.EstimatedMonthlyOnDemandCost, 64); err == nil {
			rec.EstimatedMonthlyOnDemand = onDemand
		}
	}

	// Extract account ID if available (from linked account recommendations)
	if details.AccountId != nil {
		rec.AccountID = aws.ToString(details.AccountId)
	}

	// Parse service-specific details
	switch params.Service {
	case ServiceRDS:
		if err := c.parseRDSDetails(&rec, details); err != nil {
			return nil, err
		}
	case ServiceElastiCache:
		if err := c.parseElastiCacheDetails(&rec, details); err != nil {
			return nil, err
		}
	case ServiceEC2:
		if err := c.parseEC2Details(&rec, details); err != nil {
			return nil, err
		}
	case ServiceOpenSearch, ServiceElasticsearch:
		if err := c.parseOpenSearchDetails(&rec, details); err != nil {
			return nil, err
		}
	case ServiceRedshift:
		if err := c.parseRedshiftDetails(&rec, details); err != nil {
			return nil, err
		}
	case ServiceMemoryDB:
		if err := c.parseMemoryDBDetails(&rec, details); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported service: %s", params.Service)
	}

	// Filter by region if specified
	if params.Region != "" && rec.Region != params.Region {
		return nil, nil // Skip this recommendation
	}

	// Generate description
	rec.Description = rec.GetDescription()

	return &rec, nil
}

// parseRDSDetails extracts RDS-specific details
func (c *RecommendationsClient) parseRDSDetails(rec *Recommendation, details *types.ReservationPurchaseRecommendationDetail) error {
	if details.InstanceDetails == nil || details.InstanceDetails.RDSInstanceDetails == nil {
		return fmt.Errorf("RDS instance details not found")
	}

	rdsDetails := details.InstanceDetails.RDSInstanceDetails

	rdsInfo := &RDSDetails{}

	if rdsDetails.InstanceType != nil {
		rec.InstanceType = *rdsDetails.InstanceType
	}
	if rdsDetails.DatabaseEngine != nil {
		rdsInfo.Engine = *rdsDetails.DatabaseEngine
	}
	if rdsDetails.Region != nil {
		rec.Region = NormalizeRegionName(*rdsDetails.Region)
	}
	if rdsDetails.DeploymentOption != nil {
		if *rdsDetails.DeploymentOption == "Multi-AZ" {
			rdsInfo.AZConfig = "multi-az"
		} else {
			rdsInfo.AZConfig = "single-az"
		}
	} else {
		rdsInfo.AZConfig = "single-az"
	}

	rec.ServiceDetails = rdsInfo
	return nil
}

// parseElastiCacheDetails extracts ElastiCache-specific details
func (c *RecommendationsClient) parseElastiCacheDetails(rec *Recommendation, details *types.ReservationPurchaseRecommendationDetail) error {
	if details.InstanceDetails == nil || details.InstanceDetails.ElastiCacheInstanceDetails == nil {
		return fmt.Errorf("ElastiCache instance details not found")
	}

	cacheDetails := details.InstanceDetails.ElastiCacheInstanceDetails

	cacheInfo := &ElastiCacheDetails{}

	if cacheDetails.NodeType != nil {
		rec.InstanceType = *cacheDetails.NodeType
		cacheInfo.NodeType = *cacheDetails.NodeType
	}
	if cacheDetails.ProductDescription != nil {
		cacheInfo.Engine = *cacheDetails.ProductDescription
	}
	if cacheDetails.Region != nil {
		rec.Region = NormalizeRegionName(*cacheDetails.Region)
	}

	rec.ServiceDetails = cacheInfo
	return nil
}

// parseEC2Details extracts EC2-specific details
func (c *RecommendationsClient) parseEC2Details(rec *Recommendation, details *types.ReservationPurchaseRecommendationDetail) error {
	if details.InstanceDetails == nil || details.InstanceDetails.EC2InstanceDetails == nil {
		return fmt.Errorf("EC2 instance details not found")
	}

	ec2Details := details.InstanceDetails.EC2InstanceDetails

	ec2Info := &EC2Details{}

	if ec2Details.InstanceType != nil {
		rec.InstanceType = *ec2Details.InstanceType
	}
	if ec2Details.Platform != nil {
		ec2Info.Platform = *ec2Details.Platform
	}
	if ec2Details.Region != nil {
		rec.Region = NormalizeRegionName(*ec2Details.Region)
	}
	if ec2Details.Tenancy != nil {
		ec2Info.Tenancy = *ec2Details.Tenancy
	} else {
		ec2Info.Tenancy = "shared"
	}

	// Determine scope from availability zone info
	if ec2Details.AvailabilityZone != nil && *ec2Details.AvailabilityZone != "" {
		ec2Info.Scope = "availability-zone"
	} else {
		ec2Info.Scope = "region"
	}

	rec.ServiceDetails = ec2Info
	return nil
}

// parseOpenSearchDetails extracts OpenSearch-specific details
func (c *RecommendationsClient) parseOpenSearchDetails(rec *Recommendation, details *types.ReservationPurchaseRecommendationDetail) error {
	if details.InstanceDetails == nil || details.InstanceDetails.ESInstanceDetails == nil {
		return fmt.Errorf("OpenSearch/Elasticsearch instance details not found")
	}

	esDetails := details.InstanceDetails.ESInstanceDetails

	osInfo := &OpenSearchDetails{}

	// ESInstanceDetails has InstanceClass and InstanceSize, not InstanceType
	// Build instance type from class and size
	if esDetails.InstanceClass != nil && esDetails.InstanceSize != nil {
		rec.InstanceType = fmt.Sprintf("%s.%s", *esDetails.InstanceClass, *esDetails.InstanceSize)
		osInfo.InstanceType = rec.InstanceType
	}
	if esDetails.InstanceSize != nil {
		// Parse instance count from size if available
		osInfo.InstanceCount = 1 // Default
	}
	if esDetails.Region != nil {
		rec.Region = NormalizeRegionName(*esDetails.Region)
	}

	// Note: Master node details are not typically in Cost Explorer recommendations
	osInfo.MasterEnabled = false

	rec.ServiceDetails = osInfo
	return nil
}

// parseRedshiftDetails extracts Redshift-specific details
func (c *RecommendationsClient) parseRedshiftDetails(rec *Recommendation, details *types.ReservationPurchaseRecommendationDetail) error {
	if details.InstanceDetails == nil || details.InstanceDetails.RedshiftInstanceDetails == nil {
		return fmt.Errorf("Redshift instance details not found")
	}

	rsDetails := details.InstanceDetails.RedshiftInstanceDetails

	rsInfo := &RedshiftDetails{}

	if rsDetails.NodeType != nil {
		rec.InstanceType = *rsDetails.NodeType
		rsInfo.NodeType = *rsDetails.NodeType
	}
	if rsDetails.Region != nil {
		rec.Region = NormalizeRegionName(*rsDetails.Region)
	}

	// Parse number of nodes from recommendation quantity
	rsInfo.NumberOfNodes = rec.Count
	if rsInfo.NumberOfNodes == 1 {
		rsInfo.ClusterType = "single-node"
	} else {
		rsInfo.ClusterType = "multi-node"
	}

	rec.ServiceDetails = rsInfo
	return nil
}

// parseMemoryDBDetails extracts MemoryDB-specific details
func (c *RecommendationsClient) parseMemoryDBDetails(rec *Recommendation, details *types.ReservationPurchaseRecommendationDetail) error {
	// MemoryDB might not have specific details in Cost Explorer yet
	// Parse from generic instance details

	memInfo := &MemoryDBDetails{}

	// Try to get instance type from generic details
	if details.InstanceDetails != nil {
		// MemoryDB details might be in a generic field
		// This will need adjustment based on actual AWS API response
		rec.InstanceType = "db.r6gd.xlarge" // Default for now
		memInfo.NodeType = rec.InstanceType
	}

	memInfo.NumberOfNodes = rec.Count
	memInfo.ShardCount = 1 // Default

	rec.ServiceDetails = memInfo
	return nil
}

// parseRecommendedQuantity extracts the recommended quantity from details
func (c *RecommendationsClient) parseRecommendedQuantity(details *types.ReservationPurchaseRecommendationDetail) (int32, error) {
	if details.RecommendedNumberOfInstancesToPurchase == nil {
		return 0, fmt.Errorf("recommended quantity not found")
	}

	// AWS returns this as a string, we need to parse it
	qty := *details.RecommendedNumberOfInstancesToPurchase

	// Parse the quantity string (e.g., "5.0" -> 5)
	var count float64
	_, err := fmt.Sscanf(qty, "%f", &count)
	if err != nil {
		// Try parsing as integer
		if intCount, err := strconv.Atoi(qty); err == nil {
			return int32(intCount), nil
		}
		return 0, fmt.Errorf("failed to parse quantity '%s': %w", qty, err)
	}

	return int32(count), nil
}

// parseCostInformation extracts cost and savings information
func (c *RecommendationsClient) parseCostInformation(details *types.ReservationPurchaseRecommendationDetail) (float64, float64, error) {
	var estimatedCost, savingsPercent float64

	// Parse monthly savings amount
	if details.EstimatedMonthlySavingsAmount != nil {
		fmt.Sscanf(*details.EstimatedMonthlySavingsAmount, "%f", &estimatedCost)
	}

	// Parse savings percentage
	if details.EstimatedMonthlySavingsPercentage != nil {
		fmt.Sscanf(*details.EstimatedMonthlySavingsPercentage, "%f", &savingsPercent)
	}

	return estimatedCost, savingsPercent, nil
}

// GetRecommendationsForDiscovery fetches recommendations without region filtering for auto-discovery
func (c *RecommendationsClient) GetRecommendationsForDiscovery(ctx context.Context, service ServiceType) ([]Recommendation, error) {
	params := RecommendationParams{
		Service:            service,
		PaymentOption:      "partial-upfront",
		TermInYears:        3,
		LookbackPeriodDays: 7,
		Region:             "", // Don't filter by region for discovery
	}

	return c.GetRecommendations(ctx, params)
}

// getSavingsPlansRecommendations fetches Savings Plans recommendations from Cost Explorer
func (c *RecommendationsClient) getSavingsPlansRecommendations(ctx context.Context, params RecommendationParams) ([]Recommendation, error) {
	// For Savings Plans, we need to query all three types: Compute, EC2Instance, SageMaker
	planTypes := []types.SupportedSavingsPlansType{
		types.SupportedSavingsPlansTypeComputeSp,
		types.SupportedSavingsPlansTypeEc2InstanceSp,
		types.SupportedSavingsPlansTypeSagemakerSp, // Note: lowercase 'm' in 'maker'
	}

	var allRecommendations []Recommendation

	for _, planType := range planTypes {
		input := &costexplorer.GetSavingsPlansPurchaseRecommendationInput{
			SavingsPlansType:     planType,
			PaymentOption:        ConvertSavingsPlansPaymentOption(params.PaymentOption),
			TermInYears:          ConvertSavingsPlansTermInYears(params.TermInYears),
			LookbackPeriodInDays: ConvertSavingsPlansLookbackPeriod(params.LookbackPeriodDays),
			AccountScope:         types.AccountScopeLinked,
		}

		// Note: Savings Plans API doesn't support AccountId filter

		// Implement rate limiting with exponential backoff
		var result *costexplorer.GetSavingsPlansPurchaseRecommendationOutput
		var err error

		c.rateLimiter.Reset()
		for {
			if waitErr := c.rateLimiter.Wait(ctx); waitErr != nil {
				return nil, fmt.Errorf("rate limiter wait failed: %w", waitErr)
			}

			result, err = c.costExplorerClient.GetSavingsPlansPurchaseRecommendation(ctx, input)
			if !c.rateLimiter.ShouldRetry(err) {
				break
			}
		}

		if err != nil {
			// Log error but continue with other plan types
			fmt.Printf("Warning: Failed to get %s recommendations: %v\n", planType, err)
			continue
		}

		// Parse recommendations for this plan type
		if result.SavingsPlansPurchaseRecommendation != nil {
			recs, err := c.parseSavingsPlansRecommendations(result.SavingsPlansPurchaseRecommendation, params, planType)
			if err != nil {
				fmt.Printf("Warning: Failed to parse %s recommendations: %v\n", planType, err)
				continue
			}
			allRecommendations = append(allRecommendations, recs...)
		}
	}

	return allRecommendations, nil
}

// parseSavingsPlansRecommendations converts Savings Plans recommendations to our internal format
func (c *RecommendationsClient) parseSavingsPlansRecommendations(
	spRec *types.SavingsPlansPurchaseRecommendation,
	params RecommendationParams,
	planType types.SupportedSavingsPlansType,
) ([]Recommendation, error) {
	var recommendations []Recommendation

	// Parse each recommendation detail
	for i, detail := range spRec.SavingsPlansPurchaseRecommendationDetails {
		rec, err := c.parseSavingsPlanDetail(&detail, params, planType)
		if err != nil {
			fmt.Printf("Warning: Failed to parse Savings Plan detail %d: %v\n", i, err)
			continue
		}

		if rec != nil {
			recommendations = append(recommendations, *rec)
		}
	}

	return recommendations, nil
}

// parseSavingsPlanDetail converts a single Savings Plan recommendation detail
func (c *RecommendationsClient) parseSavingsPlanDetail(
	detail *types.SavingsPlansPurchaseRecommendationDetail,
	params RecommendationParams,
	planType types.SupportedSavingsPlansType,
) (*Recommendation, error) {
	// Extract hourly commitment
	var hourlyCommitment float64
	if detail.HourlyCommitmentToPurchase != nil {
		if parsed, err := strconv.ParseFloat(*detail.HourlyCommitmentToPurchase, 64); err == nil {
			hourlyCommitment = parsed
		}
	}

	// Extract monthly savings
	var monthlySavings float64
	if detail.EstimatedMonthlySavingsAmount != nil {
		if parsed, err := strconv.ParseFloat(*detail.EstimatedMonthlySavingsAmount, 64); err == nil {
			monthlySavings = parsed
		}
	}

	// Extract savings percentage
	var savingsPercent float64
	if detail.EstimatedSavingsPercentage != nil {
		if parsed, err := strconv.ParseFloat(*detail.EstimatedSavingsPercentage, 64); err == nil {
			savingsPercent = parsed
		}
	}

	// Extract upfront cost
	var upfrontCost float64
	if detail.UpfrontCost != nil {
		if parsed, err := strconv.ParseFloat(*detail.UpfrontCost, 64); err == nil {
			upfrontCost = parsed
		}
	}

	// Extract estimated monthly SP cost
	var estimatedSPCost float64
	if detail.EstimatedSPCost != nil {
		if parsed, err := strconv.ParseFloat(*detail.EstimatedSPCost, 64); err == nil {
			estimatedSPCost = parsed
		}
	}

	// Convert plan type to string
	planTypeStr := string(planType)
	switch planType {
	case types.SupportedSavingsPlansTypeComputeSp:
		planTypeStr = "Compute"
	case types.SupportedSavingsPlansTypeEc2InstanceSp:
		planTypeStr = "EC2Instance"
	case types.SupportedSavingsPlansTypeSagemakerSp:
		planTypeStr = "SageMaker"
	}

	// Extract account ID if available
	accountID := ""
	if detail.AccountId != nil {
		accountID = aws.ToString(detail.AccountId)
	}

	// Create recommendation
	rec := &Recommendation{
		Service:       ServiceSavingsPlans,
		Region:        "",   // Savings Plans are global (region-flexible) or regional depending on type
		InstanceType:  "",   // Not applicable for Savings Plans
		PaymentOption: params.PaymentOption,
		Term:          params.TermInYears * 12,
		Count:         1, // Savings Plans don't have a count - they have hourly commitment
		EstimatedCost: monthlySavings,
		SavingsPercent: savingsPercent,
		UpfrontCost:    upfrontCost,
		RecurringMonthlyCost: estimatedSPCost,
		Timestamp:      time.Now(),
		AccountID:      accountID,
		ServiceDetails: &SavingsPlanDetails{
			PlanType:         planTypeStr,
			HourlyCommitment: hourlyCommitment,
			Coverage:         fmt.Sprintf("%.1f%%", savingsPercent),
		},
	}

	rec.Description = rec.GetDescription()
	return rec, nil
}

// ConvertSavingsPlansPaymentOption converts payment option string to Savings Plans type
func ConvertSavingsPlansPaymentOption(option string) types.PaymentOption {
	switch option {
	case "all-upfront":
		return types.PaymentOptionAllUpfront
	case "partial-upfront":
		return types.PaymentOptionPartialUpfront
	case "no-upfront":
		return types.PaymentOptionNoUpfront
	default:
		return types.PaymentOptionNoUpfront
	}
}

// ConvertSavingsPlansTermInYears converts term in years to Savings Plans type
func ConvertSavingsPlansTermInYears(years int) types.TermInYears {
	switch years {
	case 1:
		return types.TermInYearsOneYear
	case 3:
		return types.TermInYearsThreeYears
	default:
		return types.TermInYearsThreeYears
	}
}

// ConvertSavingsPlansLookbackPeriod converts lookback days to Savings Plans type
func ConvertSavingsPlansLookbackPeriod(days int) types.LookbackPeriodInDays {
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