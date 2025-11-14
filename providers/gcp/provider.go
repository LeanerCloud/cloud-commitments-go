// Package gcp provides Google Cloud Platform provider implementation
package gcp

import (
	"context"
	"fmt"
	"os"

	"cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/provider"
	"github.com/LeanerCloud/CUDly/providers/gcp/services/cloudsql"
	"github.com/LeanerCloud/CUDly/providers/gcp/services/computeengine"
)

// GCPProvider implements the Provider interface for Google Cloud Platform
type GCPProvider struct {
	ctx        context.Context
	projectID  string
	clientOpts []option.ClientOption
}

// NewProvider creates a new GCP provider
func NewProvider(config *provider.ProviderConfig) (*GCPProvider, error) {
	ctx := context.Background()

	var projectID string
	var err error

	// Use project from config if provided, otherwise detect default
	if config != nil && config.Profile != "" {
		// In GCP, we use Profile field to pass project ID
		projectID = config.Profile
	} else {
		// Try to get default project from Application Default Credentials
		projectID, err = getDefaultProject(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get default GCP project: %w", err)
		}
	}

	return &GCPProvider{
		ctx:        ctx,
		projectID:  projectID,
		clientOpts: []option.ClientOption{},
	}, nil
}

// NewProviderWithProject creates a new GCP provider with a specific project
func NewProviderWithProject(ctx context.Context, projectID string, opts ...option.ClientOption) *GCPProvider {
	return &GCPProvider{
		ctx:        ctx,
		projectID:  projectID,
		clientOpts: opts,
	}
}

// Name returns the provider name
func (p *GCPProvider) Name() string {
	return string(common.ProviderGCP)
}

// DisplayName returns the provider display name
func (p *GCPProvider) DisplayName() string {
	return "Google Cloud Platform"
}

// IsConfigured checks if GCP credentials are configured
func (p *GCPProvider) IsConfigured() bool {
	// Try to create a simple client to test credentials
	ctx := context.Background()
	client, err := resourcemanager.NewProjectsClient(ctx, p.clientOpts...)
	if err != nil {
		return false
	}
	defer client.Close()

	// Try to get the project to verify credentials work
	_, err = client.GetProject(ctx, &resourcemanagerpb.GetProjectRequest{
		Name: fmt.Sprintf("projects/%s", p.projectID),
	})

	return err == nil
}

// ValidateCredentials validates that GCP credentials are valid
func (p *GCPProvider) ValidateCredentials(ctx context.Context) error {
	client, err := resourcemanager.NewProjectsClient(ctx, p.clientOpts...)
	if err != nil {
		return fmt.Errorf("failed to create resource manager client: %w", err)
	}
	defer client.Close()

	// Verify we can access the project
	project, err := client.GetProject(ctx, &resourcemanagerpb.GetProjectRequest{
		Name: fmt.Sprintf("projects/%s", p.projectID),
	})
	if err != nil {
		return fmt.Errorf("failed to get project %s: %w", p.projectID, err)
	}

	if project.State != resourcemanagerpb.Project_ACTIVE {
		return fmt.Errorf("project %s is not active (state: %v)", p.projectID, project.State)
	}

	return nil
}

// GetCredentials returns the current GCP credentials information
func (p *GCPProvider) GetCredentials() (provider.Credentials, error) {
	if !p.IsConfigured() {
		return nil, fmt.Errorf("GCP is not configured")
	}

	// GCP uses Application Default Credentials (ADC)
	// The actual credentials could come from:
	// - GOOGLE_APPLICATION_CREDENTIALS env var (service account JSON file)
	// - gcloud CLI configuration
	// - Compute Engine/GKE metadata service
	// - Cloud Shell

	credType := provider.CredentialSourceADC // Application Default Credentials

	// Try to determine the source more specifically
	if _, ok := os.LookupEnv("GOOGLE_APPLICATION_CREDENTIALS"); ok {
		credType = provider.CredentialSourceFile
	}

	return &provider.BaseCredentials{
		Source: credType,
		Valid:  true,
	}, nil
}

// GetDefaultRegion returns the default GCP region
func (p *GCPProvider) GetDefaultRegion() string {
	// GCP doesn't have a concept of "default region" like AWS
	// Common defaults are us-central1 (Iowa) or us-east1 (South Carolina)
	return "us-central1"
}

// GetAccounts returns all accessible GCP projects
func (p *GCPProvider) GetAccounts(ctx context.Context) ([]common.Account, error) {
	// For GCP, accounts are projects
	service, err := cloudresourcemanager.NewService(ctx, p.clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource manager service: %w", err)
	}

	accounts := make([]common.Account, 0)

	// List all projects the credentials have access to
	req := service.Projects.List()
	if err := req.Pages(ctx, func(page *cloudresourcemanager.ListProjectsResponse) error {
		for _, project := range page.Projects {
			if project.LifecycleState == "ACTIVE" {
				accounts = append(accounts, common.Account{
					ID:   project.ProjectId,
					Name: project.Name,
				})
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}

	// If no projects found, return at least the default project
	if len(accounts) == 0 {
		accounts = append(accounts, common.Account{
			ID:   p.projectID,
			Name: p.projectID,
		})
	}

	return accounts, nil
}

// GetRegions returns all available GCP regions using Compute Engine API
func (p *GCPProvider) GetRegions(ctx context.Context) ([]common.Region, error) {
	client, err := compute.NewRegionsRESTClient(ctx, p.clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create compute client: %w", err)
	}
	defer client.Close()

	req := &computepb.ListRegionsRequest{
		Project: p.projectID,
	}

	regions := make([]common.Region, 0)
	it := client.List(ctx, req)

	for {
		region, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to list regions: %w", err)
		}

		if region.Name != nil && region.Status != nil && *region.Status == "UP" {
			displayName := *region.Name
			if region.Description != nil {
				displayName = *region.Description
			}

			regions = append(regions, common.Region{
				ID:          *region.Name,
				DisplayName: displayName,
			})
		}
	}

	if len(regions) == 0 {
		return nil, fmt.Errorf("no active regions found for project %s", p.projectID)
	}

	return regions, nil
}

// GetSupportedServices returns the list of supported GCP services
func (p *GCPProvider) GetSupportedServices() []common.ServiceType {
	return []common.ServiceType{
		common.ServiceCompute,
		common.ServiceRelationalDB,
	}
}

// GetServiceClient creates a service client for the specified service and region
func (p *GCPProvider) GetServiceClient(ctx context.Context, service common.ServiceType, region string) (provider.ServiceClient, error) {
	switch service {
	case common.ServiceCompute:
		return computeengine.NewClient(ctx, p.projectID, region, p.clientOpts...)
	case common.ServiceRelationalDB:
		return cloudsql.NewClient(ctx, p.projectID, region, p.clientOpts...)
	default:
		return nil, fmt.Errorf("unsupported service type for GCP: %s", service)
	}
}

// GetRecommendationsClient creates a recommendations client
func (p *GCPProvider) GetRecommendationsClient(ctx context.Context) (provider.RecommendationsClient, error) {
	return &RecommendationsClientAdapter{
		ctx:        ctx,
		projectID:  p.projectID,
		clientOpts: p.clientOpts,
	}, nil
}

// getDefaultProject attempts to get the default GCP project from environment or ADC
func getDefaultProject(ctx context.Context) (string, error) {
	// Try to use the Cloud Resource Manager API to get the default project
	service, err := cloudresourcemanager.NewService(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to create resource manager service: %w", err)
	}

	// List projects and use the first active one as default
	req := service.Projects.List()
	resp, err := req.Do()
	if err != nil {
		return "", fmt.Errorf("failed to list projects: %w", err)
	}

	for _, project := range resp.Projects {
		if project.LifecycleState == "ACTIVE" {
			return project.ProjectId, nil
		}
	}

	return "", fmt.Errorf("no active GCP projects found")
}

func init() {
	// Register GCP provider in the global registry
	provider.RegisterProvider("gcp", func(config *provider.ProviderConfig) (provider.Provider, error) {
		return NewProvider(config)
	})
}
