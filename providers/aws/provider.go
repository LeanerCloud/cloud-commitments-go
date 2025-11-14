// Package aws provides AWS cloud provider implementation
package aws

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/provider"
)

// AWSProvider implements the Provider interface for AWS
type AWSProvider struct {
	cfg     aws.Config
	profile string
	region  string
}

// NewAWSProvider creates a new AWS provider instance
func NewAWSProvider(config *provider.ProviderConfig) (*AWSProvider, error) {
	p := &AWSProvider{}

	if config != nil {
		p.profile = config.Profile
		p.region = config.Region
	}

	return p, nil
}

// Name returns the provider name
func (p *AWSProvider) Name() string {
	return "aws"
}

// DisplayName returns the human-readable provider name
func (p *AWSProvider) DisplayName() string {
	return "Amazon Web Services"
}

// IsConfigured checks if AWS credentials are available
func (p *AWSProvider) IsConfigured() bool {
	ctx := context.Background()

	// Try to load AWS config
	var opts []func(*config.LoadOptions) error

	if p.profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(p.profile))
	}

	if p.region != "" {
		opts = append(opts, config.WithRegion(p.region))
	}

	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return false
	}

	p.cfg = cfg
	return true
}

// GetCredentials returns AWS credentials
func (p *AWSProvider) GetCredentials() (provider.Credentials, error) {
	if !p.IsConfigured() {
		return nil, fmt.Errorf("AWS is not configured")
	}

	creds, err := p.cfg.Credentials.Retrieve(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve AWS credentials: %w", err)
	}

	credType := provider.CredentialSourceEnvironment
	if creds.Source != "" {
		switch creds.Source {
		case "SharedConfigCredentials":
			credType = provider.CredentialSourceFile
		case "AssumeRoleProvider":
			credType = provider.CredentialSourceIAMRole
		}
	}

	return &provider.BaseCredentials{
		Source: credType,
		Valid:  true,
	}, nil
}

// ValidateCredentials validates that AWS credentials are working
func (p *AWSProvider) ValidateCredentials(ctx context.Context) error {
	if !p.IsConfigured() {
		return fmt.Errorf("AWS is not configured")
	}

	// Use STS GetCallerIdentity to validate credentials
	stsClient := sts.NewFromConfig(p.cfg)
	_, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return fmt.Errorf("AWS credentials validation failed: %w", err)
	}

	return nil
}

// GetAccounts returns all accessible AWS accounts
func (p *AWSProvider) GetAccounts(ctx context.Context) ([]common.Account, error) {
	// Try to get organization accounts
	orgClient := organizations.NewFromConfig(p.cfg)

	accounts := make([]common.Account, 0)

	// Get current account
	stsClient := sts.NewFromConfig(p.cfg)
	identity, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to get current account: %w", err)
	}

	// Add current account
	accounts = append(accounts, common.Account{
		Provider:    common.ProviderAWS,
		ID:          *identity.Account,
		Name:        *identity.Account,
		DisplayName: *identity.Account,
		IsDefault:   true,
	})

	// Try to list organization accounts
	paginator := organizations.NewListAccountsPaginator(orgClient, &organizations.ListAccountsInput{})
	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			// Not in an organization or no permissions, return just current account
			return accounts, nil
		}

		for _, acc := range output.Accounts {
			// Skip the current account as we already added it
			if *acc.Id == *identity.Account {
				continue
			}

			accounts = append(accounts, common.Account{
				Provider:    common.ProviderAWS,
				ID:          *acc.Id,
				Name:        *acc.Name,
				DisplayName: *acc.Name,
				IsDefault:   false,
			})
		}
	}

	return accounts, nil
}

// GetRegions returns all available AWS regions using EC2 DescribeRegions API
func (p *AWSProvider) GetRegions(ctx context.Context) ([]common.Region, error) {
	// Use EC2 DescribeRegions to get dynamic list of regions
	ec2Client := ec2.NewFromConfig(p.cfg)

	result, err := ec2Client.DescribeRegions(ctx, &ec2.DescribeRegionsInput{
		AllRegions: aws.Bool(false), // Only return enabled regions
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe AWS regions: %w", err)
	}

	regions := make([]common.Region, 0, len(result.Regions))
	for _, region := range result.Regions {
		if region.RegionName == nil {
			continue
		}

		displayName := *region.RegionName
		if region.OptInStatus != nil {
			displayName = fmt.Sprintf("%s (%s)", *region.RegionName, *region.OptInStatus)
		}

		regions = append(regions, common.Region{
			Provider:    common.ProviderAWS,
			ID:          *region.RegionName,
			Name:        *region.RegionName,
			DisplayName: displayName,
		})
	}

	return regions, nil
}

// GetDefaultRegion returns the default AWS region
func (p *AWSProvider) GetDefaultRegion() string {
	if p.region != "" {
		return p.region
	}
	if p.cfg.Region != "" {
		return p.cfg.Region
	}
	return "us-east-1"
}

// GetSupportedServices returns the list of services supported by AWS provider
func (p *AWSProvider) GetSupportedServices() []common.ServiceType {
	return []common.ServiceType{
		common.ServiceCompute,
		common.ServiceRelationalDB,
		common.ServiceCache,
		common.ServiceSearch,
		common.ServiceDataWarehouse,
		common.ServiceSavingsPlans,
		// Legacy service types for backward compatibility
		common.ServiceEC2,
		common.ServiceRDS,
		common.ServiceElastiCache,
		common.ServiceOpenSearch,
		common.ServiceRedshift,
		common.ServiceMemoryDB,
	}
}

// GetServiceClient returns a service client for the specified service and region
func (p *AWSProvider) GetServiceClient(ctx context.Context, service common.ServiceType, region string) (provider.ServiceClient, error) {
	if !p.IsConfigured() {
		return nil, fmt.Errorf("AWS is not configured")
	}

	// Create a regional config
	regionalCfg := p.cfg.Copy()
	regionalCfg.Region = region

	switch service {
	case common.ServiceCompute, common.ServiceEC2:
		return NewEC2Client(regionalCfg), nil
	case common.ServiceRelationalDB, common.ServiceRDS:
		return NewRDSClient(regionalCfg), nil
	case common.ServiceCache, common.ServiceElastiCache:
		return NewElastiCacheClient(regionalCfg), nil
	case common.ServiceSearch, common.ServiceOpenSearch:
		return NewOpenSearchClient(regionalCfg), nil
	case common.ServiceDataWarehouse, common.ServiceRedshift:
		return NewRedshiftClient(regionalCfg), nil
	case common.ServiceMemoryDB:
		return NewMemoryDBClient(regionalCfg), nil
	case common.ServiceSavingsPlans:
		return NewSavingsPlansClient(regionalCfg), nil
	default:
		return nil, fmt.Errorf("unsupported service: %s", service)
	}
}

// GetRecommendationsClient returns a recommendations client
func (p *AWSProvider) GetRecommendationsClient(ctx context.Context) (provider.RecommendationsClient, error) {
	if !p.IsConfigured() {
		return nil, fmt.Errorf("AWS is not configured")
	}

	return NewRecommendationsClient(p.cfg), nil
}

// Register the AWS provider with the global registry
func init() {
	provider.RegisterProvider("aws", func(config *provider.ProviderConfig) (provider.Provider, error) {
		return NewAWSProvider(config)
	})
}
