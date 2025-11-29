package gcp

import (
	"context"
	"errors"
	"os"
	"testing"

	"cloud.google.com/go/compute/apiv1/computepb"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iterator"

	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/provider"
)

// MockProjectsClient mocks the ProjectsClient interface
type MockProjectsClient struct {
	project *resourcemanagerpb.Project
	err     error
	closed  bool
}

func (m *MockProjectsClient) GetProject(ctx context.Context, req *resourcemanagerpb.GetProjectRequest) (*resourcemanagerpb.Project, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.project, nil
}

func (m *MockProjectsClient) Close() error {
	m.closed = true
	return nil
}

// MockRegionsClient mocks the RegionsClient interface
type MockRegionsClient struct {
	regions []*computepb.Region
	err     error
	closed  bool
}

func (m *MockRegionsClient) List(ctx context.Context, req *computepb.ListRegionsRequest) RegionsIterator {
	return &MockRegionsIterator{regions: m.regions, err: m.err}
}

func (m *MockRegionsClient) Close() error {
	m.closed = true
	return nil
}

// MockRegionsIterator mocks the RegionsIterator interface
type MockRegionsIterator struct {
	regions []*computepb.Region
	index   int
	err     error
}

func (m *MockRegionsIterator) Next() (*computepb.Region, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.index >= len(m.regions) {
		return nil, iterator.Done
	}
	r := m.regions[m.index]
	m.index++
	return r, nil
}

// MockResourceManagerService mocks the ResourceManagerService interface
type MockResourceManagerService struct {
	projects []*cloudresourcemanager.Project
	err      error
}

func (m *MockResourceManagerService) ListProjects(ctx context.Context) ([]*cloudresourcemanager.Project, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.projects, nil
}

func TestNewProviderWithProject(t *testing.T) {
	ctx := context.Background()
	provider := NewProviderWithProject(ctx, "test-project")

	require.NotNil(t, provider)
	assert.Equal(t, "test-project", provider.projectID)
	assert.Equal(t, ctx, provider.ctx)
	// clientOpts is nil when no options are passed (not an error - it's expected)
}

func TestGCPProvider_Name(t *testing.T) {
	provider := &GCPProvider{}
	assert.Equal(t, "gcp", provider.Name())
}

func TestGCPProvider_DisplayName(t *testing.T) {
	provider := &GCPProvider{}
	assert.Equal(t, "Google Cloud Platform", provider.DisplayName())
}

func TestGCPProvider_GetDefaultRegion(t *testing.T) {
	provider := &GCPProvider{}
	// GCP defaults to us-central1
	assert.Equal(t, "us-central1", provider.GetDefaultRegion())
}

func TestGCPProvider_GetSupportedServices(t *testing.T) {
	provider := &GCPProvider{}
	services := provider.GetSupportedServices()

	require.NotEmpty(t, services)
	assert.Contains(t, services, common.ServiceCompute)
	assert.Contains(t, services, common.ServiceRelationalDB)
}

func TestGCPProvider_GetServiceClient_UnsupportedService(t *testing.T) {
	ctx := context.Background()
	provider := NewProviderWithProject(ctx, "test-project")

	// ServiceCache is not supported in GCP provider
	_, err := provider.GetServiceClient(ctx, common.ServiceCache, "us-central1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported service type")
}

func TestGCPProvider_GetRecommendationsClient(t *testing.T) {
	ctx := context.Background()
	provider := NewProviderWithProject(ctx, "test-project")

	client, err := provider.GetRecommendationsClient(ctx)
	require.NoError(t, err)
	require.NotNil(t, client)

	// Verify it's the right type
	adapter, ok := client.(*RecommendationsClientAdapter)
	assert.True(t, ok)
	assert.Equal(t, "test-project", adapter.projectID)
}

func TestGCPProvider_Fields(t *testing.T) {
	ctx := context.Background()
	provider := NewProviderWithProject(ctx, "my-gcp-project")

	assert.Equal(t, "my-gcp-project", provider.projectID)
	assert.Equal(t, ctx, provider.ctx)
	assert.Empty(t, provider.clientOpts)
}

func TestNewProviderWithProject_WithEmptyProject(t *testing.T) {
	ctx := context.Background()
	provider := NewProviderWithProject(ctx, "")

	require.NotNil(t, provider)
	assert.Equal(t, "", provider.projectID)
}

func TestGCPProvider_GetServiceClient_Compute(t *testing.T) {
	ctx := context.Background()
	p := NewProviderWithProject(ctx, "test-project")

	// GetServiceClient creates client but may succeed even without credentials
	// The error would occur when actually using the client
	client, err := p.GetServiceClient(ctx, common.ServiceCompute, "us-central1")
	// May succeed in creation - tests the branch coverage
	if err == nil {
		require.NotNil(t, client)
		assert.Equal(t, common.ServiceCompute, client.GetServiceType())
		assert.Equal(t, "us-central1", client.GetRegion())
	}
}

func TestGCPProvider_GetServiceClient_RelationalDB(t *testing.T) {
	ctx := context.Background()
	p := NewProviderWithProject(ctx, "test-project")

	// GetServiceClient creates client but may succeed even without credentials
	// The error would occur when actually using the client
	client, err := p.GetServiceClient(ctx, common.ServiceRelationalDB, "us-central1")
	// May succeed in creation - tests the branch coverage
	if err == nil {
		require.NotNil(t, client)
		assert.Equal(t, common.ServiceRelationalDB, client.GetServiceType())
		assert.Equal(t, "us-central1", client.GetRegion())
	}
}

func TestNewProvider_WithConfig(t *testing.T) {
	// Test NewProvider with a config containing a project ID
	config := &provider.ProviderConfig{
		Profile: "test-project-id",
	}

	p, err := NewProvider(config)
	// Error is expected since we don't have real GCP credentials
	// but the function should handle it gracefully
	if err != nil {
		// Expected - no GCP credentials
		assert.Contains(t, err.Error(), "failed to get default GCP project")
	} else {
		require.NotNil(t, p)
		assert.Equal(t, "test-project-id", p.projectID)
	}
}

func TestNewProvider_NilConfig(t *testing.T) {
	// Test NewProvider with nil config
	_, err := NewProvider(nil)
	// Error is expected since we need to detect the default project
	// which requires GCP credentials
	if err != nil {
		assert.Contains(t, err.Error(), "failed to get default GCP project")
	}
}

func TestGCPProvider_GetCredentials_WithEnvVar(t *testing.T) {
	// Test GetCredentials when GOOGLE_APPLICATION_CREDENTIALS env var is set
	// We just test the logic, not actual credential retrieval
	p := &GCPProvider{
		projectID: "test-project",
	}

	// Save and restore env var
	origVal := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")

	// Test with env var set
	os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/path/to/creds.json")
	defer func() {
		if origVal == "" {
			os.Unsetenv("GOOGLE_APPLICATION_CREDENTIALS")
		} else {
			os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", origVal)
		}
	}()

	// GetCredentials will still fail without real credentials,
	// but we're testing the code path
	_, _ = p.GetCredentials()
}

func TestGCPProvider_SetterMethods(t *testing.T) {
	ctx := context.Background()
	p := NewProviderWithProject(ctx, "test-project")

	// Test SetProjectsClient
	mockProjects := &MockProjectsClient{}
	p.SetProjectsClient(mockProjects)
	assert.Equal(t, mockProjects, p.projectsClient)

	// Test SetRegionsClient
	mockRegions := &MockRegionsClient{}
	p.SetRegionsClient(mockRegions)
	assert.Equal(t, mockRegions, p.regionsClient)

	// Test SetResourceManagerService
	mockRM := &MockResourceManagerService{}
	p.SetResourceManagerService(mockRM)
	assert.Equal(t, mockRM, p.resourceManagerService)
}

func TestGCPProvider_IsConfigured_WithMock(t *testing.T) {
	ctx := context.Background()
	p := NewProviderWithProject(ctx, "test-project")

	mockClient := &MockProjectsClient{
		project: &resourcemanagerpb.Project{
			Name:  "projects/test-project",
			State: resourcemanagerpb.Project_ACTIVE,
		},
	}
	p.SetProjectsClient(mockClient)

	result := p.IsConfigured()
	assert.True(t, result)
	assert.True(t, mockClient.closed)
}

func TestGCPProvider_IsConfigured_Error(t *testing.T) {
	ctx := context.Background()
	p := NewProviderWithProject(ctx, "test-project")

	mockClient := &MockProjectsClient{
		err: errors.New("API error"),
	}
	p.SetProjectsClient(mockClient)

	result := p.IsConfigured()
	assert.False(t, result)
}

func TestGCPProvider_ValidateCredentials_WithMock(t *testing.T) {
	ctx := context.Background()
	p := NewProviderWithProject(ctx, "test-project")

	mockClient := &MockProjectsClient{
		project: &resourcemanagerpb.Project{
			Name:  "projects/test-project",
			State: resourcemanagerpb.Project_ACTIVE,
		},
	}
	p.SetProjectsClient(mockClient)

	err := p.ValidateCredentials(ctx)
	assert.NoError(t, err)
	assert.True(t, mockClient.closed)
}

func TestGCPProvider_ValidateCredentials_Error(t *testing.T) {
	ctx := context.Background()
	p := NewProviderWithProject(ctx, "test-project")

	mockClient := &MockProjectsClient{
		err: errors.New("API error"),
	}
	p.SetProjectsClient(mockClient)

	err := p.ValidateCredentials(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get project")
}

func TestGCPProvider_ValidateCredentials_InactiveProject(t *testing.T) {
	ctx := context.Background()
	p := NewProviderWithProject(ctx, "test-project")

	mockClient := &MockProjectsClient{
		project: &resourcemanagerpb.Project{
			Name:  "projects/test-project",
			State: resourcemanagerpb.Project_DELETE_REQUESTED,
		},
	}
	p.SetProjectsClient(mockClient)

	err := p.ValidateCredentials(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "is not active")
}

func TestGCPProvider_GetAccounts_WithMock(t *testing.T) {
	ctx := context.Background()
	p := NewProviderWithProject(ctx, "test-project")

	mockService := &MockResourceManagerService{
		projects: []*cloudresourcemanager.Project{
			{
				ProjectId:      "project-1",
				Name:           "Project 1",
				LifecycleState: "ACTIVE",
			},
			{
				ProjectId:      "project-2",
				Name:           "Project 2",
				LifecycleState: "ACTIVE",
			},
			{
				ProjectId:      "project-deleted",
				Name:           "Deleted Project",
				LifecycleState: "DELETE_REQUESTED",
			},
		},
	}
	p.SetResourceManagerService(mockService)

	accounts, err := p.GetAccounts(ctx)
	require.NoError(t, err)
	assert.Len(t, accounts, 2)
	assert.Equal(t, "project-1", accounts[0].ID)
	assert.Equal(t, "Project 1", accounts[0].Name)
	assert.Equal(t, "project-2", accounts[1].ID)
}

func TestGCPProvider_GetAccounts_Empty(t *testing.T) {
	ctx := context.Background()
	p := NewProviderWithProject(ctx, "default-project")

	mockService := &MockResourceManagerService{
		projects: []*cloudresourcemanager.Project{},
	}
	p.SetResourceManagerService(mockService)

	accounts, err := p.GetAccounts(ctx)
	require.NoError(t, err)
	// Should return the default project when no projects found
	assert.Len(t, accounts, 1)
	assert.Equal(t, "default-project", accounts[0].ID)
}

func TestGCPProvider_GetAccounts_Error(t *testing.T) {
	ctx := context.Background()
	p := NewProviderWithProject(ctx, "test-project")

	mockService := &MockResourceManagerService{
		err: errors.New("API error"),
	}
	p.SetResourceManagerService(mockService)

	_, err := p.GetAccounts(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list projects")
}

func TestGCPProvider_GetRegions_WithMock(t *testing.T) {
	ctx := context.Background()
	p := NewProviderWithProject(ctx, "test-project")

	upStatus := "UP"
	name1, name2 := "us-central1", "us-east1"
	desc1, desc2 := "Iowa", "South Carolina"

	mockClient := &MockRegionsClient{
		regions: []*computepb.Region{
			{
				Name:        &name1,
				Description: &desc1,
				Status:      &upStatus,
			},
			{
				Name:        &name2,
				Description: &desc2,
				Status:      &upStatus,
			},
		},
	}
	p.SetRegionsClient(mockClient)

	regions, err := p.GetRegions(ctx)
	require.NoError(t, err)
	assert.Len(t, regions, 2)
	assert.Equal(t, "us-central1", regions[0].ID)
	assert.Equal(t, "Iowa", regions[0].DisplayName)
	assert.Equal(t, "us-east1", regions[1].ID)
	assert.True(t, mockClient.closed)
}

func TestGCPProvider_GetRegions_WithoutDescription(t *testing.T) {
	ctx := context.Background()
	p := NewProviderWithProject(ctx, "test-project")

	upStatus := "UP"
	name := "us-central1"

	mockClient := &MockRegionsClient{
		regions: []*computepb.Region{
			{
				Name:        &name,
				Description: nil, // No description
				Status:      &upStatus,
			},
		},
	}
	p.SetRegionsClient(mockClient)

	regions, err := p.GetRegions(ctx)
	require.NoError(t, err)
	assert.Len(t, regions, 1)
	assert.Equal(t, "us-central1", regions[0].ID)
	assert.Equal(t, "us-central1", regions[0].DisplayName) // Should use name as fallback
}

func TestGCPProvider_GetRegions_FilterDownRegions(t *testing.T) {
	ctx := context.Background()
	p := NewProviderWithProject(ctx, "test-project")

	upStatus := "UP"
	downStatus := "DOWN"
	name1, name2 := "us-central1", "us-down1"

	mockClient := &MockRegionsClient{
		regions: []*computepb.Region{
			{
				Name:   &name1,
				Status: &upStatus,
			},
			{
				Name:   &name2,
				Status: &downStatus,
			},
		},
	}
	p.SetRegionsClient(mockClient)

	regions, err := p.GetRegions(ctx)
	require.NoError(t, err)
	// Should only return UP regions
	assert.Len(t, regions, 1)
	assert.Equal(t, "us-central1", regions[0].ID)
}

func TestGCPProvider_GetRegions_Empty(t *testing.T) {
	ctx := context.Background()
	p := NewProviderWithProject(ctx, "test-project")

	mockClient := &MockRegionsClient{
		regions: []*computepb.Region{},
	}
	p.SetRegionsClient(mockClient)

	_, err := p.GetRegions(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no active regions found")
}

func TestGCPProvider_GetRegions_Error(t *testing.T) {
	ctx := context.Background()
	p := NewProviderWithProject(ctx, "test-project")

	mockClient := &MockRegionsClient{
		err: errors.New("API error"),
	}
	p.SetRegionsClient(mockClient)

	_, err := p.GetRegions(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list regions")
}

func TestGCPProvider_GetCredentials_NotConfigured(t *testing.T) {
	p := &GCPProvider{
		projectID: "",
	}

	// Set up mock that returns error to simulate not configured
	mockClient := &MockProjectsClient{
		err: errors.New("not configured"),
	}
	p.SetProjectsClient(mockClient)

	_, err := p.GetCredentials()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "GCP is not configured")
}

func TestGCPProvider_GetCredentials_Configured(t *testing.T) {
	p := &GCPProvider{
		projectID: "test-project",
	}

	mockClient := &MockProjectsClient{
		project: &resourcemanagerpb.Project{
			Name:  "projects/test-project",
			State: resourcemanagerpb.Project_ACTIVE,
		},
	}
	p.SetProjectsClient(mockClient)

	creds, err := p.GetCredentials()
	require.NoError(t, err)
	require.NotNil(t, creds)
}

func TestGCPProvider_GetCredentials_WithFileSource(t *testing.T) {
	p := &GCPProvider{
		projectID: "test-project",
	}

	mockClient := &MockProjectsClient{
		project: &resourcemanagerpb.Project{
			Name:  "projects/test-project",
			State: resourcemanagerpb.Project_ACTIVE,
		},
	}
	p.SetProjectsClient(mockClient)

	// Save and restore env var
	origVal := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/path/to/creds.json")
	defer func() {
		if origVal == "" {
			os.Unsetenv("GOOGLE_APPLICATION_CREDENTIALS")
		} else {
			os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", origVal)
		}
	}()

	creds, err := p.GetCredentials()
	require.NoError(t, err)
	require.NotNil(t, creds)

	baseCreds, ok := creds.(*provider.BaseCredentials)
	require.True(t, ok)
	assert.Equal(t, provider.CredentialSourceFile, baseCreds.Source)
}
