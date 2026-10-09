package ec2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockEC2Client implements API for testing.
type MockEC2Client struct {
	mock.Mock
}

func (m *MockEC2Client) PurchaseReservedInstancesOffering(ctx context.Context, params *ec2.PurchaseReservedInstancesOfferingInput, optFns ...func(*ec2.Options)) (*ec2.PurchaseReservedInstancesOfferingOutput, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ec2.PurchaseReservedInstancesOfferingOutput), args.Error(1)
}

func (m *MockEC2Client) DescribeReservedInstancesOfferings(ctx context.Context, params *ec2.DescribeReservedInstancesOfferingsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeReservedInstancesOfferingsOutput, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ec2.DescribeReservedInstancesOfferingsOutput), args.Error(1)
}

// lastDescribeOfferingsInput captures the most recent
// DescribeReservedInstancesOfferings params for assertion in integration tests.
// Only used in tests that set this field explicitly; nil means uncaptured.
type capturingMockEC2Client struct {
	MockEC2Client
	LastDescribeOfferingsInput *ec2.DescribeReservedInstancesOfferingsInput
}

func (m *capturingMockEC2Client) DescribeReservedInstancesOfferings(ctx context.Context, params *ec2.DescribeReservedInstancesOfferingsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeReservedInstancesOfferingsOutput, error) {
	m.LastDescribeOfferingsInput = params
	return m.MockEC2Client.DescribeReservedInstancesOfferings(ctx, params, optFns...)
}

func (m *MockEC2Client) DescribeReservedInstances(ctx context.Context, params *ec2.DescribeReservedInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeReservedInstancesOutput, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ec2.DescribeReservedInstancesOutput), args.Error(1)
}

func (m *MockEC2Client) DescribeInstanceTypeOfferings(ctx context.Context, params *ec2.DescribeInstanceTypeOfferingsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstanceTypeOfferingsOutput, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ec2.DescribeInstanceTypeOfferingsOutput), args.Error(1)
}

func (m *MockEC2Client) GetReservedInstancesExchangeQuote(ctx context.Context, params *ec2.GetReservedInstancesExchangeQuoteInput, optFns ...func(*ec2.Options)) (*ec2.GetReservedInstancesExchangeQuoteOutput, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ec2.GetReservedInstancesExchangeQuoteOutput), args.Error(1)
}

func (m *MockEC2Client) AcceptReservedInstancesExchangeQuote(ctx context.Context, params *ec2.AcceptReservedInstancesExchangeQuoteInput, optFns ...func(*ec2.Options)) (*ec2.AcceptReservedInstancesExchangeQuoteOutput, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ec2.AcceptReservedInstancesExchangeQuoteOutput), args.Error(1)
}

func (m *MockEC2Client) CreateTags(ctx context.Context, params *ec2.CreateTagsInput, optFns ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ec2.CreateTagsOutput), args.Error(1)
}

func (m *MockEC2Client) CreateReservedInstancesListing(ctx context.Context, params *ec2.CreateReservedInstancesListingInput, optFns ...func(*ec2.Options)) (*ec2.CreateReservedInstancesListingOutput, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ec2.CreateReservedInstancesListingOutput), args.Error(1)
}

func (m *MockEC2Client) DescribeReservedInstancesListings(ctx context.Context, params *ec2.DescribeReservedInstancesListingsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeReservedInstancesListingsOutput, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ec2.DescribeReservedInstancesListingsOutput), args.Error(1)
}

func (m *MockEC2Client) CancelReservedInstancesListing(ctx context.Context, params *ec2.CancelReservedInstancesListingInput, optFns ...func(*ec2.Options)) (*ec2.CancelReservedInstancesListingOutput, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ec2.CancelReservedInstancesListingOutput), args.Error(1)
}

func TestNewClient(t *testing.T) {
	t.Parallel()
	cfg := aws.Config{
		Region: "us-east-1",
	}

	client := NewClient(cfg)

	assert.NotNil(t, client)
	assert.NotNil(t, client.client)
	assert.Equal(t, "us-east-1", client.region)
}

func TestClient_GetServiceType(t *testing.T) {
	t.Parallel()
	client := &Client{region: "us-east-1"}
	assert.Equal(t, common.ServiceCompute, client.GetServiceType())
}

func TestClient_GetRegion(t *testing.T) {
	t.Parallel()
	client := &Client{region: "eu-west-1"}
	assert.Equal(t, "eu-west-1", client.GetRegion())
}

func TestClient_GetRecommendations(t *testing.T) {
	t.Parallel()
	client := &Client{region: "us-east-1"}
	recs, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{})
	assert.NoError(t, err)
	assert.Empty(t, recs)
}

func TestClient_GetExistingCommitments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		setupMocks  func(*MockEC2Client)
		expectedLen int
		expectError bool
	}{
		{
			name: "successful retrieval with active instances",
			setupMocks: func(m *MockEC2Client) {
				m.On("DescribeReservedInstances", mock.Anything, mock.Anything).
					Return(&ec2.DescribeReservedInstancesOutput{
						ReservedInstances: []types.ReservedInstances{
							{
								ReservedInstancesId: aws.String("ri-123"),
								InstanceType:        types.InstanceTypeT3Micro,
								InstanceCount:       aws.Int32(2),
								ProductDescription:  types.RIProductDescriptionLinuxUnix,
								State:               types.ReservedInstanceStateActive,
								Duration:            aws.Int64(31536000),
								Start:               aws.Time(time.Now()),
								End:                 aws.Time(time.Now().AddDate(1, 0, 0)),
								OfferingType:        types.OfferingTypeValuesPartialUpfront,
							},
							{
								ReservedInstancesId: aws.String("ri-456"),
								InstanceType:        types.InstanceTypeM5Large,
								InstanceCount:       aws.Int32(1),
								ProductDescription:  types.RIProductDescriptionLinuxUnix,
								State:               types.ReservedInstanceStatePaymentPending,
								Duration:            aws.Int64(94608000),
								Start:               aws.Time(time.Now()),
								End:                 aws.Time(time.Now().AddDate(3, 0, 0)),
								OfferingType:        types.OfferingTypeValuesAllUpfront,
							},
						},
					}, nil).Once()
			},
			expectedLen: 2,
			expectError: false,
		},
		{
			name: "API filter returns only active and payment-pending instances",
			setupMocks: func(m *MockEC2Client) {
				// Mock simulates API behavior - filter is applied server-side
				// So we only return instances that match the filter
				m.On("DescribeReservedInstances", mock.Anything, mock.Anything).
					Return(&ec2.DescribeReservedInstancesOutput{
						ReservedInstances: []types.ReservedInstances{
							{
								ReservedInstancesId: aws.String("ri-123"),
								InstanceType:        types.InstanceTypeT3Micro,
								InstanceCount:       aws.Int32(2),
								State:               types.ReservedInstanceStateActive,
								Duration:            aws.Int64(31536000),
								Start:               aws.Time(time.Now()),
							},
						},
					}, nil).Once()
			},
			expectedLen: 1,
			expectError: false,
		},
		{
			// Queued RIs are owned purchases; the state filter must request
			// them or the duplicate check never sees them.
			name: "state filter includes queued instances",
			setupMocks: func(m *MockEC2Client) {
				m.On("DescribeReservedInstances", mock.Anything, mock.MatchedBy(func(in *ec2.DescribeReservedInstancesInput) bool {
					return len(in.Filters) == 1 && aws.ToString(in.Filters[0].Name) == "state" &&
						assert.ObjectsAreEqual([]string{
							string(types.ReservedInstanceStateActive),
							string(types.ReservedInstanceStatePaymentPending),
							string(types.ReservedInstanceStateQueued),
						}, in.Filters[0].Values)
				})).
					Return(&ec2.DescribeReservedInstancesOutput{
						ReservedInstances: []types.ReservedInstances{
							{
								ReservedInstancesId: aws.String("ri-q"),
								InstanceType:        types.InstanceTypeM5Large,
								InstanceCount:       aws.Int32(1),
								State:               types.ReservedInstanceStateQueued,
								Start:               aws.Time(time.Now().Add(24 * time.Hour)),
							},
						},
					}, nil).Once()
			},
			expectedLen: 1,
			expectError: false,
		},
		{
			name: "API error",
			setupMocks: func(m *MockEC2Client) {
				m.On("DescribeReservedInstances", mock.Anything, mock.Anything).
					Return(nil, fmt.Errorf("API error")).Once()
			},
			expectedLen: 0,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockEC2Client{}
			tt.setupMocks(mockClient)

			client := &Client{
				client: mockClient,
				region: "us-east-1",
			}

			result, err := client.GetExistingCommitments(context.Background())

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, result, tt.expectedLen)
			}

			mockClient.AssertExpectations(t)
		})
	}
}

func TestClient_GetValidResourceTypes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		setupMocks    func(*MockEC2Client)
		expectedTypes []string
		expectError   bool
	}{
		{
			name: "successful retrieval single page",
			setupMocks: func(m *MockEC2Client) {
				m.On("DescribeInstanceTypeOfferings", mock.Anything, mock.Anything).
					Return(&ec2.DescribeInstanceTypeOfferingsOutput{
						InstanceTypeOfferings: []types.InstanceTypeOffering{
							{InstanceType: types.InstanceTypeT3Micro},
							{InstanceType: types.InstanceTypeT3Small},
							{InstanceType: types.InstanceTypeM5Large},
						},
						NextToken: nil,
					}, nil).Once()
			},
			expectedTypes: []string{"m5.large", "t3.micro", "t3.small"},
			expectError:   false,
		},
		{
			name: "API error",
			setupMocks: func(m *MockEC2Client) {
				m.On("DescribeInstanceTypeOfferings", mock.Anything, mock.Anything).
					Return(nil, fmt.Errorf("API error")).Once()
			},
			expectedTypes: nil,
			expectError:   true,
		},
		{
			name: "deduplicates instance types",
			setupMocks: func(m *MockEC2Client) {
				m.On("DescribeInstanceTypeOfferings", mock.Anything, mock.Anything).
					Return(&ec2.DescribeInstanceTypeOfferingsOutput{
						InstanceTypeOfferings: []types.InstanceTypeOffering{
							{InstanceType: types.InstanceTypeT3Micro},
							{InstanceType: types.InstanceTypeT3Micro},
							{InstanceType: types.InstanceTypeM5Large},
						},
						NextToken: nil,
					}, nil).Once()
			},
			expectedTypes: []string{"m5.large", "t3.micro"},
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockEC2Client{}
			tt.setupMocks(mockClient)

			client := &Client{
				client: mockClient,
				region: "us-east-1",
			}

			result, err := client.GetValidResourceTypes(context.Background())

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedTypes, result)
			}

			mockClient.AssertExpectations(t)
		})
	}
}

func TestClient_ValidateOffering(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	client := &Client{
		client: mockEC2,
		region: "us-east-1",
	}

	rec := common.Recommendation{
		Service:       common.ServiceCompute,
		ResourceType:  "t3.micro",
		PaymentOption: "partial-upfront",
		Term:          "3yr",
		Details: &common.ComputeDetails{
			Platform: "Linux/UNIX",
			Tenancy:  "default",
			Scope:    "Region",
		},
	}

	mockEC2.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{
			ReservedInstancesOfferings: []types.ReservedInstancesOffering{
				{
					ReservedInstancesOfferingId: aws.String("offering-123"),
					InstanceType:                types.InstanceTypeT3Micro,
					Duration:                    aws.Int64(94608000),
					OfferingType:                types.OfferingTypeValuesPartialUpfront,
					ProductDescription:          types.RIProductDescriptionLinuxUnix,
					InstanceTenancy:             types.TenancyDefault,
				},
			},
		}, nil)

	err := client.ValidateOffering(context.Background(), rec)
	assert.NoError(t, err)
	mockEC2.AssertExpectations(t)
}

func TestClient_PurchaseCommitment(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	client := &Client{
		client: mockEC2,
		region: "us-east-1",
	}

	rec := common.Recommendation{
		Service:       common.ServiceCompute,
		ResourceType:  "t3.micro",
		Count:         2,
		PaymentOption: "partial-upfront",
		Term:          "3yr",
		Details: &common.ComputeDetails{
			Platform: "Linux/UNIX",
			Tenancy:  "default",
			Scope:    "Region",
		},
	}

	mockEC2.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{
			ReservedInstancesOfferings: []types.ReservedInstancesOffering{
				{
					ReservedInstancesOfferingId: aws.String("offering-123"),
					InstanceType:                types.InstanceTypeT3Micro,
					Duration:                    aws.Int64(94608000),
					OfferingType:                types.OfferingTypeValuesPartialUpfront,
					ProductDescription:          types.RIProductDescriptionLinuxUnix,
					InstanceTenancy:             types.TenancyDefault,
					FixedPrice:                  aws.Float32(100.0),
				},
			},
		}, nil)

	mockEC2.On("PurchaseReservedInstancesOffering", mock.Anything, mock.Anything).
		Return(&ec2.PurchaseReservedInstancesOfferingOutput{
			ReservedInstancesId: aws.String("ri-12345678"),
		}, nil)

	// Post-purchase tagging call (EC2 RIs don't accept tags at purchase time).
	mockEC2.On("CreateTags", mock.Anything, mock.Anything).
		Return(&ec2.CreateTagsOutput{}, nil)

	result, err := client.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})

	assert.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "ri-12345678", result.CommitmentID)
	require.NotNil(t, result.Cost, "EC2 RI purchase must record its upfront cost, not leave it unset")
	assert.Equal(t, 200.0, *result.Cost, "total upfront is the offering FixedPrice x purchased count")
	mockEC2.AssertExpectations(t)
}

func TestClient_PurchaseCommitment_UpfrontCost(t *testing.T) {
	for _, tt := range []struct {
		name  string
		price *float32
		count int
		want  *float64
	}{
		{"unknown", nil, 3, nil},
		{"zero", aws.Float32(0), 3, aws.Float64(0)},
		{"cents", aws.Float32(12.34), 3, aws.Float64(37.02)},
		{"large quantity", aws.Float32(12.34), 20000, aws.Float64(246800)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := &MockEC2Client{}
			client := &Client{client: m, region: "us-east-1"}
			rec := common.Recommendation{ResourceType: "t3.micro", Count: tt.count, PaymentOption: "partial-upfront", Term: "3yr",
				Details: &common.ComputeDetails{Platform: "Linux/UNIX", Tenancy: "default", Scope: "Region"}}
			m.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).Return(&ec2.DescribeReservedInstancesOfferingsOutput{
				ReservedInstancesOfferings: []types.ReservedInstancesOffering{{ReservedInstancesOfferingId: aws.String("offer"),
					InstanceType: types.InstanceTypeT3Micro, Duration: aws.Int64(94608000), OfferingType: types.OfferingTypeValuesPartialUpfront,
					ProductDescription: types.RIProductDescriptionLinuxUnix, InstanceTenancy: types.TenancyDefault, FixedPrice: tt.price}},
			}, nil).Once()
			m.On("PurchaseReservedInstancesOffering", mock.Anything, mock.MatchedBy(func(in *ec2.PurchaseReservedInstancesOfferingInput) bool {
				return int(aws.ToInt32(in.InstanceCount)) == tt.count
			})).Return(&ec2.PurchaseReservedInstancesOfferingOutput{ReservedInstancesId: aws.String("ri-bought")}, nil).Once()
			m.On("CreateTags", mock.Anything, mock.Anything).Return(&ec2.CreateTagsOutput{}, nil).Once()
			result, err := client.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})
			require.NoError(t, err)
			assert.True(t, result.Success)
			assert.Equal(t, "ri-bought", result.CommitmentID)
			assert.Equal(t, tt.want, result.Cost)
			payload, err := json.Marshal(result)
			require.NoError(t, err)
			var decoded struct {
				Cost *float64 `json:"cost"`
			}
			require.NoError(t, json.Unmarshal(payload, &decoded))
			assert.Equal(t, tt.want, decoded.Cost)
			m.AssertExpectations(t)
		})
	}
}

func TestClient_PurchaseCommitment_StampsPurchaseAutomationTag(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	client := &Client{client: mockEC2, region: "us-east-1"}

	rec := common.Recommendation{
		Service:       common.ServiceCompute,
		ResourceType:  "t3.micro",
		Count:         1,
		PaymentOption: "all-upfront",
		Term:          "1yr",
		Region:        "us-east-1",
		Details:       &common.ComputeDetails{Platform: "Linux/UNIX", Tenancy: "default", Scope: "Region"},
	}

	mockEC2.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{
			ReservedInstancesOfferings: []types.ReservedInstancesOffering{{
				ReservedInstancesOfferingId: aws.String("off-tag"),
				InstanceType:                types.InstanceTypeT3Micro,
				Duration:                    aws.Int64(31536000),
				OfferingType:                types.OfferingTypeValuesAllUpfront,
				ProductDescription:          types.RIProductDescriptionLinuxUnix,
				InstanceTenancy:             types.TenancyDefault,
			}},
		}, nil)

	mockEC2.On("PurchaseReservedInstancesOffering", mock.Anything, mock.Anything).
		Return(&ec2.PurchaseReservedInstancesOfferingOutput{
			ReservedInstancesId: aws.String("ri-tag-test"),
		}, nil)

	mockEC2.On("CreateTags", mock.Anything, mock.MatchedBy(func(in *ec2.CreateTagsInput) bool {
		if len(in.Resources) != 1 || in.Resources[0] != "ri-tag-test" {
			return false
		}
		for _, tag := range in.Tags {
			if aws.ToString(tag.Key) == common.PurchaseTagKey && aws.ToString(tag.Value) == common.PurchaseSourceWeb {
				return true
			}
		}
		return false
	})).Return(&ec2.CreateTagsOutput{}, nil)

	result, err := client.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{Source: common.PurchaseSourceWeb})
	assert.NoError(t, err)
	assert.True(t, result.Success)
	mockEC2.AssertExpectations(t)
}

func TestClient_GetOfferingDetails(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	client := &Client{
		client: mockEC2,
		region: "us-east-1",
	}

	rec := common.Recommendation{
		Service:       common.ServiceCompute,
		ResourceType:  "t3.micro",
		PaymentOption: "partial-upfront",
		Term:          "3yr",
		Count:         1,
		Details: &common.ComputeDetails{
			Platform: "Linux/UNIX",
			Tenancy:  "default",
			Scope:    "Region",
		},
	}

	mockEC2.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{
			ReservedInstancesOfferings: []types.ReservedInstancesOffering{
				{
					ReservedInstancesOfferingId: aws.String("offering-123"),
					InstanceType:                types.InstanceTypeT3Micro,
					ProductDescription:          types.RIProductDescriptionLinuxUnix,
					InstanceTenancy:             types.TenancyDefault,
					OfferingType:                types.OfferingTypeValuesPartialUpfront,
					Duration:                    aws.Int64(94608000),
					UsagePrice:                  aws.Float32(0.05),
					FixedPrice:                  aws.Float32(100.0),
					CurrencyCode:                types.CurrencyCodeValuesUsd,
				},
			},
		}, nil).Twice()

	details, err := client.GetOfferingDetails(context.Background(), rec)

	assert.NoError(t, err)
	assert.NotNil(t, details)
	assert.Equal(t, "offering-123", details.OfferingID)
	assert.Equal(t, "t3.micro", details.ResourceType)
	mockEC2.AssertExpectations(t)
}

func TestClient_GetDurationValue(t *testing.T) {
	t.Parallel()
	client := &Client{}

	tests := []struct {
		name      string
		term      string
		expected  int64
		expectErr bool
	}{
		{"1 year", "1yr", 31536000, false},
		{"1 numeric", "1", 31536000, false},
		{"3 years", "3yr", 94608000, false},
		{"3 numeric", "3", 94608000, false},
		// Regression for ARCH-04 follow-up (issue #1207): unrecognized or
		// empty terms must error instead of silently mapping to a 1-year
		// purchase. An empty term is what a 0/NULL Term DB row produces on
		// the scheduler purchase path.
		{"invalid term errors", "invalid", 0, true},
		{"empty term errors", "", 0, true},
		{"zero term errors", "0", 0, true},
		{"2yr term errors", "2yr", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := client.getDurationValue(tt.term)
			if tt.expectErr {
				if assert.Error(t, err) {
					assert.Contains(t, err.Error(), "unsupported EC2 reservation term")
				}
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestParseEC2Tenancy verifies that legacy values written by pre-fix/598 parser
// versions still map to the EC2 enum, and that empty or unsupported values error.
func TestParseEC2Tenancy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected types.Tenancy
		wantErr  bool
	}{
		{"legacy shared -> default", "shared", types.TenancyDefault, false},
		{"canonical default", "default", types.TenancyDefault, false},
		{"canonical dedicated", "dedicated", types.TenancyDedicated, false},
		{"uppercase SHARED -> default", "SHARED", types.TenancyDefault, false},
		{"uppercase DEFAULT -> default", "DEFAULT", types.TenancyDefault, false},
		{"uppercase DEDICATED -> dedicated", "DEDICATED", types.TenancyDedicated, false},
		{"empty errors", "", "", true},
		{"host has no RI product", "host", "", true},
		{"unknown errors", "unknown-tenancy", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseEC2Tenancy(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "unsupported EC2 RI tenancy")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestParseEC2Scope verifies that legacy values written by pre-fix/598 parser
// versions still map to the EC2 enum, and that empty or unknown values error.
func TestParseEC2Scope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected types.Scope
		wantErr  bool
	}{
		{"legacy region -> Region", "region", types.ScopeRegional, false},
		{"legacy availability-zone", "availability-zone", types.ScopeAvailabilityZone, false},
		{"canonical Region", "Region", types.ScopeRegional, false},
		{"canonical Availability Zone", "Availability Zone", types.ScopeAvailabilityZone, false},
		{"lowercase availability zone", "availability zone", types.ScopeAvailabilityZone, false},
		{"empty errors", "", "", true},
		{"unknown errors", "unknown-scope", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseEC2Scope(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "unsupported EC2 RI scope")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestFindOfferingID_PaginationCapFires asserts that findOffering returns a
// "pagination cap reached" error after maxOfferingPages empty pages and does NOT
// make a (maxOfferingPages+1)th call (issue #688).
func TestFindOfferingID_PaginationCapFires(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	t.Cleanup(func() { mockEC2.AssertExpectations(t) })
	client := &Client{client: mockEC2, region: "us-east-1"}

	rec := common.Recommendation{
		ResourceType:  "t4g.nano",
		PaymentOption: "no-upfront",
		Term:          "1yr",
		Details: &common.ComputeDetails{
			Platform: "Linux/UNIX",
			Tenancy:  "default",
			Scope:    "Region",
		},
	}

	for i := range maxOfferingPages {
		mockEC2.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
			Return(&ec2.DescribeReservedInstancesOfferingsOutput{
				ReservedInstancesOfferings: []types.ReservedInstancesOffering{},
				NextToken:                  aws.String(fmt.Sprintf("tok-%d", i+1)),
			}, nil).Once()
	}

	_, err := client.findOffering(context.Background(), rec, "", "")

	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "pagination cap reached")
	}
	mockEC2.AssertNumberOfCalls(t, "DescribeReservedInstancesOfferings", maxOfferingPages)
}

// TestFindOfferingID_WrongVariantRejected asserts that findOffering rejects an
// offering whose OfferingType does not match the requested payment option
// is soft-skipped (logged, not returned). With the typed OfferingType field
// on the request this should never fire in production; the test pins the
// defense-in-depth behavior for the rare API anomaly. After skipping the
// only mismatched offering on the only page, findOffering returns the
// "no offerings found" diagnostic (issue #688).
func TestFindOfferingID_WrongVariantRejected(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	t.Cleanup(func() { mockEC2.AssertExpectations(t) })
	client := &Client{client: mockEC2, region: "us-east-1"}

	rec := common.Recommendation{
		ResourceType:  "t4g.nano",
		PaymentOption: "no-upfront",
		Term:          "1yr",
		Details: &common.ComputeDetails{
			Platform: "Linux/UNIX",
			Tenancy:  "default",
			Scope:    "Region",
		},
	}

	mockEC2.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{
			ReservedInstancesOfferings: []types.ReservedInstancesOffering{
				{
					ReservedInstancesOfferingId: aws.String("wrong-offering"),
					InstanceType:                types.InstanceTypeT4gNano,
					OfferingType:                types.OfferingTypeValuesAllUpfront, // mismatch
				},
			},
		}, nil).Once()

	_, err := client.findOffering(context.Background(), rec, "", "")

	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "no offerings found")
		assert.Contains(t, err.Error(), "t4g.nano")
	}
}

// TestFindOfferingID_HappyPath asserts that findOffering returns the correct
// offering ID on the first page when a matching offering is present (issue #688).
func TestFindOfferingID_HappyPath(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	t.Cleanup(func() { mockEC2.AssertExpectations(t) })
	client := &Client{client: mockEC2, region: "us-east-1"}

	rec := common.Recommendation{
		ResourceType:  "t4g.nano",
		PaymentOption: "no-upfront",
		Term:          "1yr",
		Details: &common.ComputeDetails{
			Platform: "Linux/UNIX",
			Tenancy:  "default",
			Scope:    "Region",
		},
	}

	mockEC2.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{
			ReservedInstancesOfferings: []types.ReservedInstancesOffering{
				{
					ReservedInstancesOfferingId: aws.String("offering-ok"),
					InstanceType:                types.InstanceTypeT4gNano,
					OfferingType:                types.OfferingTypeValuesNoUpfront,
				},
			},
		}, nil).Once()

	offering, err := client.findOffering(context.Background(), rec, "", "")

	require.NoError(t, err)
	assert.Equal(t, "offering-ok", aws.ToString(offering.ReservedInstancesOfferingId))
}

// TestFindOfferingID_InvalidTerm_ErrorsBeforeAPICall is the ARCH-04
// follow-up (issue #1207) call-path regression test: an unrecognized or
// empty term must abort the offering lookup before any
// DescribeReservedInstancesOfferings call, rather than silently matching
// (and buying) a 1-year offering. A "0" term is what a 0/NULL Term DB row
// produces on the scheduler purchase path.
func TestFindOfferingID_InvalidTerm_ErrorsBeforeAPICall(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	t.Cleanup(func() { mockEC2.AssertExpectations(t) })
	client := &Client{client: mockEC2, region: "us-east-1"}

	rec := common.Recommendation{
		ResourceType:  "t4g.nano",
		PaymentOption: "no-upfront",
		Term:          "0",
		Details: &common.ComputeDetails{
			Platform: "Linux/UNIX",
			Tenancy:  "default",
			Scope:    "Region",
		},
	}

	_, err := client.findOffering(context.Background(), rec, "", "")

	require.Error(t, err, "findOffering must error on an unrecognized term (ARCH-04)")
	assert.Contains(t, err.Error(), "unsupported EC2 reservation term")
	mockEC2.AssertNotCalled(t, "DescribeReservedInstancesOfferings", mock.Anything, mock.Anything)
}

// TestClient_tagReservedInstance_NameTagPresent asserts that a purchase on the
// no-token CLI path (issue #687) stamps a self-describing Name tag on the EC2
// RI. EC2 PurchaseReservedInstancesOfferingInput has no customer-supplied name
// field, so the Name tag is the only way to identify the reservation in the AWS
// console without cross-referencing CUDly's purchase audit log.
func TestClient_tagReservedInstance_NameTagPresent(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	client := &Client{client: mockEC2, region: "us-west-2"}

	rec := common.Recommendation{
		Service:       common.ServiceCompute,
		ResourceType:  "m5.xlarge",
		Region:        "us-west-2",
		Count:         3,
		PaymentOption: "all-upfront",
		Term:          "1yr",
		Details:       &common.ComputeDetails{Platform: "Linux/UNIX", Tenancy: "default", Scope: "Region"},
	}

	var capturedTags []types.Tag
	mockEC2.On("CreateTags", mock.Anything, mock.MatchedBy(func(in *ec2.CreateTagsInput) bool {
		capturedTags = in.Tags
		return len(in.Resources) == 1 && in.Resources[0] == "ri-name-test"
	})).Return(&ec2.CreateTagsOutput{}, nil)

	err := client.tagReservedInstance(context.Background(), "ri-name-test", rec, "", "")
	assert.NoError(t, err)

	tagMap := make(map[string]string, len(capturedTags))
	for _, tag := range capturedTags {
		tagMap[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}

	name, ok := tagMap["Name"]
	assert.True(t, ok, "Name tag must be present in CreateTags call")
	assert.True(t, len(name) > 0, "Name tag must be non-empty")
	assert.LessOrEqual(t, len(name), 60, "Name must fit the 60-char AWS reservation-name cap")
	// Key segments that make the RI self-describing without CUDly:
	assert.Contains(t, name, "ec2", "Name must start with the service code")
	assert.Contains(t, name, "us-west-2", "Name must embed the region")
	assert.Contains(t, name, "m5-xlarge", "Name must embed the SKU (dots->hyphens)")
	assert.Contains(t, name, "3x", "Name must embed the count")
	assert.Contains(t, name, "1yr", "Name must embed the term")

	mockEC2.AssertExpectations(t)
}

// TestClient_PurchaseCommitment_NameTagInCreateTagsRequest asserts that an
// end-to-end purchase on the no-token CLI path (issue #687) produces a
// CreateTags call that includes a self-describing Name tag.
func TestClient_PurchaseCommitment_NameTagInCreateTagsRequest(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	client := &Client{client: mockEC2, region: "ap-southeast-1"}

	rec := common.Recommendation{
		Service:       common.ServiceCompute,
		ResourceType:  "r6g.large",
		Region:        "ap-southeast-1",
		Count:         2,
		PaymentOption: "no-upfront",
		Term:          "3yr",
		Details:       &common.ComputeDetails{Platform: "Linux/UNIX", Tenancy: "default", Scope: "Region"},
	}

	// No idempotency token -> skip DescribeReservedInstances guard
	mockEC2.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{
			ReservedInstancesOfferings: []types.ReservedInstancesOffering{{
				ReservedInstancesOfferingId: aws.String("off-name-e2e"),
				InstanceType:                types.InstanceTypeR6gLarge,
				Duration:                    aws.Int64(94608000),
				OfferingType:                types.OfferingTypeValuesNoUpfront,
				ProductDescription:          types.RIProductDescriptionLinuxUnix,
				InstanceTenancy:             types.TenancyDefault,
			}},
		}, nil)

	mockEC2.On("PurchaseReservedInstancesOffering", mock.Anything, mock.Anything).
		Return(&ec2.PurchaseReservedInstancesOfferingOutput{
			ReservedInstancesId: aws.String("ri-name-e2e"),
		}, nil)

	var capturedName string
	mockEC2.On("CreateTags", mock.Anything, mock.MatchedBy(func(in *ec2.CreateTagsInput) bool {
		for _, tag := range in.Tags {
			if aws.ToString(tag.Key) == "Name" {
				capturedName = aws.ToString(tag.Value)
				return true
			}
		}
		return false
	})).Return(&ec2.CreateTagsOutput{}, nil)

	result, err := client.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})
	assert.NoError(t, err)
	assert.True(t, result.Success)

	assert.True(t, len(capturedName) > 0, "Name tag must be set on the CreateTags call")
	assert.Contains(t, capturedName, "ec2", "service code must appear in Name: %q", capturedName)
	assert.Contains(t, capturedName, "ap-southeast-1", "region must appear in Name: %q", capturedName)

	mockEC2.AssertExpectations(t)
}

// TestBuildEC2OfferingQuery_EmptyPlatformErrors is the M2/M3 regression test:
// buildEC2OfferingQuery must return an error when Platform is empty rather than
// silently substituting "Linux/UNIX". On the purchase path the CE parser always
// populates Platform from the recommendation payload; an empty value signals a
// malformed rec, not a value to be fabricated.
func TestBuildEC2OfferingQuery_EmptyPlatformErrors(t *testing.T) {
	rec := common.Recommendation{
		ResourceType:  "m5.large",
		PaymentOption: "all-upfront",
		Term:          "1yr",
		Details: &common.ComputeDetails{
			InstanceType: "m5.large",
			Platform:     "", // intentionally empty
			Tenancy:      "default",
			Scope:        "Region",
		},
	}
	details := rec.Details.(*common.ComputeDetails)

	_, err := buildEC2OfferingQuery(rec, details, OneYearSeconds)
	assert.Error(t, err, "buildEC2OfferingQuery must error when Platform is empty (M2/M3 fix)")
	assert.Contains(t, err.Error(), "Platform")
}

// TestBuildEC2OfferingQuery_ValidPlatform asserts the happy path still works.
func TestBuildEC2OfferingQuery_ValidPlatform(t *testing.T) {
	rec := common.Recommendation{
		ResourceType:  "m5.large",
		PaymentOption: "all-upfront",
		Term:          "1yr",
		Details: &common.ComputeDetails{
			InstanceType: "m5.large",
			Platform:     "Linux/UNIX",
			Tenancy:      "default",
			Scope:        "Region",
		},
	}
	details := rec.Details.(*common.ComputeDetails)

	q, err := buildEC2OfferingQuery(rec, details, OneYearSeconds)
	assert.NoError(t, err)
	assert.Equal(t, types.RIProductDescription("Linux/UNIX"), q.productDesc)
	assert.Equal(t, types.Tenancy("default"), q.tenancy)
}

// TestPurchaseCommitment_IdempotencySkipLogMasked asserts that the re-drive
// skip-log line emits a masked token (first 8 chars + "..."), not the raw
// 64-char idempotency token (issue #656).
//
// This is a real §4 regression test: it captures the bytes written to the
// standard logger and asserts both that the raw token is absent AND that
// the masked form is present.  Reverting line 137 of client.go to log
// opts.IdempotencyToken raw causes the NotContains assertion to fail.
func TestPurchaseCommitment_IdempotencySkipLogMasked(t *testing.T) {
	// Not parallel: we swap the global log writer and must restore it before
	// any other test that also captures the logger races with us.
	mockEC2 := &MockEC2Client{}
	t.Cleanup(func() { mockEC2.AssertExpectations(t) })
	client := &Client{client: mockEC2, region: "us-east-1"}

	token := common.DeriveIdempotencyToken("exec-idem-656", 0)

	// Capture the standard logger so we can assert what is actually emitted.
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	// Simulate that an RI tagged with this token already exists (re-drive path).
	mockEC2.On("DescribeReservedInstances", mock.Anything, mock.MatchedBy(func(in *ec2.DescribeReservedInstancesInput) bool {
		for _, f := range in.Filters {
			if aws.ToString(f.Name) == "tag:"+common.IdempotencyTagKey {
				return len(f.Values) == 1 && f.Values[0] == token
			}
		}
		return false
	})).Return(&ec2.DescribeReservedInstancesOutput{
		ReservedInstances: []types.ReservedInstances{
			{ReservedInstancesId: aws.String("ri-existing-656")},
		},
	}, nil).Once()

	rec := common.Recommendation{
		ResourceType:  "t3.micro",
		Count:         1,
		PaymentOption: "all-upfront",
		Term:          "1yr",
		Details:       &common.ComputeDetails{Platform: "Linux/UNIX", Tenancy: "default", Scope: "Region"},
	}

	result, err := client.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{IdempotencyToken: token})

	assert.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "ri-existing-656", result.CommitmentID)

	// Core regression assertions: the re-drive log line must contain the masked
	// form and must NOT contain the full raw token.
	logOutput := logBuf.String()
	masked := common.MaskToken(token)
	assert.Contains(t, logOutput, masked, "re-drive log must emit the masked token")
	assert.NotContains(t, logOutput, token, "re-drive log must NOT emit the raw idempotency token")

	// Sanity-check that MaskToken itself has the expected shape (first 8 chars + "...").
	assert.Equal(t, token[:8]+"...", masked, "MaskToken shape: first 8 chars + ellipsis")
	assert.NotEqual(t, token, masked, "masked token must not equal raw token")
}

// --- RI Marketplace listing tests (issue #292) ---

func TestClient_CreateMarketplaceListing_HappyPathMultiCount(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	client := &Client{client: mockEC2, region: "us-east-1"}

	// Capture the input so we can assert InstanceCount is the row's count, not 1.
	mockEC2.On("CreateReservedInstancesListing", mock.Anything,
		mock.MatchedBy(func(in *ec2.CreateReservedInstancesListingInput) bool {
			return aws.ToInt32(in.InstanceCount) == 3 &&
				aws.ToString(in.ReservedInstancesId) == "ri-multi" &&
				len(in.PriceSchedules) == 1
		})).
		Return(&ec2.CreateReservedInstancesListingOutput{
			ReservedInstancesListings: []types.ReservedInstancesListing{
				{
					ReservedInstancesListingId: aws.String("ril-abc"),
					Status:                     types.ListingStatusActive,
				},
			},
		}, nil)

	res, err := client.CreateMarketplaceListing(context.Background(), MarketplaceListingRequest{
		ReservedInstancesID: "ri-multi",
		ClientToken:         "tok-1",
		InstanceCount:       3,
		PriceSchedule:       []MarketplacePriceTier{{Term: 12, Price: 100}},
	})

	assert.NoError(t, err)
	assert.Equal(t, "ril-abc", res.ListingID)
	assert.Equal(t, "active", res.State)
	mockEC2.AssertExpectations(t)
}

func TestClient_CreateMarketplaceListing_RejectsNonPositiveCount(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	client := &Client{client: mockEC2, region: "us-east-1"}

	_, err := client.CreateMarketplaceListing(context.Background(), MarketplaceListingRequest{
		ReservedInstancesID: "ri-1",
		ClientToken:         "tok",
		InstanceCount:       0,
		PriceSchedule:       []MarketplacePriceTier{{Term: 12, Price: 100}},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "instance count must be a positive integer")
	// No outbound call must have been made.
	mockEC2.AssertNotCalled(t, "CreateReservedInstancesListing", mock.Anything, mock.Anything)
	mockEC2.AssertExpectations(t)
}

func TestClient_CreateMarketplaceListing_EmptyScheduleRejected(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	client := &Client{client: mockEC2, region: "us-east-1"}

	_, err := client.CreateMarketplaceListing(context.Background(), MarketplaceListingRequest{
		ReservedInstancesID: "ri-1",
		InstanceCount:       1,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "price schedule must have at least one tier")
	mockEC2.AssertExpectations(t)
}

func TestClient_CreateMarketplaceListing_EmptyListingIDRejected(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	client := &Client{client: mockEC2, region: "us-east-1"}

	mockEC2.On("CreateReservedInstancesListing", mock.Anything, mock.Anything).
		Return(&ec2.CreateReservedInstancesListingOutput{
			ReservedInstancesListings: []types.ReservedInstancesListing{
				{ReservedInstancesListingId: aws.String(""), Status: types.ListingStatusActive},
			},
		}, nil)

	_, err := client.CreateMarketplaceListing(context.Background(), MarketplaceListingRequest{
		ReservedInstancesID: "ri-1",
		InstanceCount:       1,
		PriceSchedule:       []MarketplacePriceTier{{Term: 12, Price: 100}},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty ID")
	mockEC2.AssertExpectations(t)
}

func TestClient_DescribeMarketplaceListing(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	client := &Client{client: mockEC2, region: "us-east-1"}

	mockEC2.On("DescribeReservedInstancesListings", mock.Anything,
		mock.MatchedBy(func(in *ec2.DescribeReservedInstancesListingsInput) bool {
			return aws.ToString(in.ReservedInstancesListingId) == "ril-xyz"
		})).
		Return(&ec2.DescribeReservedInstancesListingsOutput{
			ReservedInstancesListings: []types.ReservedInstancesListing{
				{ReservedInstancesListingId: aws.String("ril-xyz"), Status: types.ListingStatusClosed},
			},
		}, nil)

	res, err := client.DescribeMarketplaceListing(context.Background(), "ril-xyz")
	assert.NoError(t, err)
	assert.Equal(t, "ril-xyz", res.ListingID)
	assert.Equal(t, "closed", res.State)
	mockEC2.AssertExpectations(t)
}

func TestClient_DescribeMarketplaceListing_NotFound(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	client := &Client{client: mockEC2, region: "us-east-1"}

	mockEC2.On("DescribeReservedInstancesListings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesListingsOutput{}, nil)

	_, err := client.DescribeMarketplaceListing(context.Background(), "ril-missing")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	mockEC2.AssertExpectations(t)
}

func TestClient_CancelMarketplaceListing(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	client := &Client{client: mockEC2, region: "us-east-1"}

	mockEC2.On("CancelReservedInstancesListing", mock.Anything,
		mock.MatchedBy(func(in *ec2.CancelReservedInstancesListingInput) bool {
			return aws.ToString(in.ReservedInstancesListingId) == "ril-cancel"
		})).
		Return(&ec2.CancelReservedInstancesListingOutput{
			ReservedInstancesListings: []types.ReservedInstancesListing{
				{ReservedInstancesListingId: aws.String("ril-cancel"), Status: types.ListingStatusCancelled},
			},
		}, nil)

	res, err := client.CancelMarketplaceListing(context.Background(), "ril-cancel")
	assert.NoError(t, err)
	assert.Equal(t, "ril-cancel", res.ListingID)
	assert.Equal(t, string(types.ListingStatusCancelled), res.State)
	mockEC2.AssertExpectations(t)
}

func TestClient_CancelMarketplaceListing_APIError(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	client := &Client{client: mockEC2, region: "us-east-1"}

	mockEC2.On("CancelReservedInstancesListing", mock.Anything, mock.Anything).
		Return(nil, fmt.Errorf("boom"))

	_, err := client.CancelMarketplaceListing(context.Background(), "ril-cancel")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "CancelReservedInstancesListing failed")
	mockEC2.AssertExpectations(t)
}

func TestResolveOfferingClassType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input   string
		want    types.OfferingClassType
		wantErr bool
	}{
		{"convertible", types.OfferingClassTypeConvertible, false},
		{"", types.OfferingClassTypeConvertible, false}, // empty = default = convertible
		{"standard", types.OfferingClassTypeStandard, false},
		{"STANDARD", "", true},    // case-sensitive
		{"unknown", "", true},     // unknown value must error
		{"Convertible", "", true}, // wrong case must error
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got, err := resolveOfferingClassType(tc.input)
			if tc.wantErr {
				assert.Error(t, err, "expected error for input %q", tc.input)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestDescribeInputFromQuery_OfferingClass(t *testing.T) {
	t.Parallel()
	base := ec2OfferingQuery{
		instanceType:     types.InstanceTypeT3Micro,
		productDesc:      types.RIProductDescriptionLinuxUnix,
		tenancy:          types.TenancyDefault,
		scope:            types.ScopeRegional,
		duration:         94608000,
		wantOfferingType: types.OfferingTypeValuesNoUpfront,
	}

	t.Run("convertible explicit", func(t *testing.T) {
		t.Parallel()
		q := base
		q.offeringClass = types.OfferingClassTypeConvertible
		inp := describeInputFromQuery(q, nil)
		assert.Equal(t, types.OfferingClassTypeConvertible, inp.OfferingClass)
	})

	t.Run("standard explicit", func(t *testing.T) {
		t.Parallel()
		q := base
		q.offeringClass = types.OfferingClassTypeStandard
		inp := describeInputFromQuery(q, nil)
		assert.Equal(t, types.OfferingClassTypeStandard, inp.OfferingClass)
	})
}

// TestFindOfferingID_OfferingClassReachesSDKCall is the integration test for
// issue #694: it verifies that the offeringClassStr argument passed to
// findOffering is wired all the way through to the OfferingClass field on
// the outbound DescribeReservedInstancesOfferings SDK call. The test fails if
// the wiring regresses (e.g. the field is dropped or hardcoded).
func TestFindOfferingID_OfferingClassReachesSDKCall(t *testing.T) {
	t.Parallel()

	rec := common.Recommendation{
		ResourceType:  "m5.large",
		PaymentOption: "all-upfront",
		Term:          "1yr",
		Details: &common.ComputeDetails{
			Platform: "Linux/UNIX",
			Tenancy:  "default",
			Scope:    "Region",
		},
	}

	offeringOutput := &ec2.DescribeReservedInstancesOfferingsOutput{
		ReservedInstancesOfferings: []types.ReservedInstancesOffering{
			{
				ReservedInstancesOfferingId: aws.String("offering-std-123"),
				InstanceType:                types.InstanceTypeM5Large,
				OfferingType:                types.OfferingTypeValuesAllUpfront,
			},
		},
	}

	tests := []struct {
		name              string
		offeringClassStr  string
		wantOfferingClass types.OfferingClassType
	}{
		{
			name:              "empty string defaults to convertible",
			offeringClassStr:  "",
			wantOfferingClass: types.OfferingClassTypeConvertible,
		},
		{
			name:              "convertible explicit reaches SDK as convertible",
			offeringClassStr:  "convertible",
			wantOfferingClass: types.OfferingClassTypeConvertible,
		},
		{
			name:              "standard reaches SDK as standard",
			offeringClassStr:  "standard",
			wantOfferingClass: types.OfferingClassTypeStandard,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cap := &capturingMockEC2Client{}
			cap.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
				Return(offeringOutput, nil).Once()

			client := &Client{client: cap, region: "us-east-1"}

			offering, err := client.findOffering(context.Background(), rec, "", tc.offeringClassStr)
			require.NoError(t, err)
			assert.Equal(t, "offering-std-123", aws.ToString(offering.ReservedInstancesOfferingId))

			if assert.NotNil(t, cap.LastDescribeOfferingsInput, "DescribeReservedInstancesOfferings must have been called") {
				assert.Equal(t, tc.wantOfferingClass, cap.LastDescribeOfferingsInput.OfferingClass,
					"OfferingClass on the SDK call must match the configured value")
			}
			cap.AssertExpectations(t)
		})
	}
}

// TestFindOfferingID_CtxCancelledBeforePage asserts that findOffering returns
// context.Canceled immediately when the context is already canceled at the top
// of the first pagination iteration, without calling the AWS API (issue #515).
func TestFindOfferingID_CtxCancelledBeforePage(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	t.Cleanup(func() { mockEC2.AssertExpectations(t) })
	client := &Client{client: mockEC2, region: "us-east-1"}

	rec := common.Recommendation{
		ResourceType:  "t4g.nano",
		PaymentOption: "no-upfront",
		Term:          "1yr",
		Details: &common.ComputeDetails{
			Platform: "Linux/UNIX",
			Tenancy:  "default",
			Scope:    "Region",
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the first iteration

	// The mock must not be called: ctx.Err() fires at the top of the loop.
	_, err := client.findOffering(ctx, rec, "", "")

	assert.ErrorIs(t, err, context.Canceled)
	mockEC2.AssertNumberOfCalls(t, "DescribeReservedInstancesOfferings", 0)
}

// TestFindOfferingID_EmptyStringTokenEndsPagination asserts that a page whose
// NextToken is a pointer to an empty string (rather than nil) is treated as the
// terminal page and does not cause an extra API call (issue #515).
func TestFindOfferingID_EmptyStringTokenEndsPagination(t *testing.T) {
	t.Parallel()
	mockEC2 := &MockEC2Client{}
	t.Cleanup(func() { mockEC2.AssertExpectations(t) })
	client := &Client{client: mockEC2, region: "us-east-1"}

	rec := common.Recommendation{
		ResourceType:  "t4g.nano",
		PaymentOption: "no-upfront",
		Term:          "1yr",
		Details: &common.ComputeDetails{
			Platform: "Linux/UNIX",
			Tenancy:  "default",
			Scope:    "Region",
		},
	}

	// Single page with zero results and NextToken = ""; must not loop again.
	mockEC2.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{
			ReservedInstancesOfferings: []types.ReservedInstancesOffering{},
			NextToken:                  aws.String(""),
		}, nil).Once()

	_, err := client.findOffering(context.Background(), rec, "", "")

	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "no offerings found")
	}
	mockEC2.AssertNumberOfCalls(t, "DescribeReservedInstancesOfferings", 1)
}

// invalidTenancyScopeCases are recommendation tenancy/scope pairs that must
// abort an EC2 RI lookup instead of defaulting to shared tenancy or regional scope.
var invalidTenancyScopeCases = []struct {
	name, tenancy, scope string
}{
	{"empty tenancy", "", "Region"},
	{"host tenancy", "host", "Region"},
	{"unknown tenancy", "unknown-tenancy", "Region"},
	{"empty scope", "dedicated", ""},
	{"unknown scope", "dedicated", "unknown-scope"},
}

// matchingOfferingMock answers any offering search with a default-tenancy
// offering, so a silent default would carry through to a purchase.
func matchingOfferingMock() *MockEC2Client {
	mockEC2 := &MockEC2Client{}
	mockEC2.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{
			ReservedInstancesOfferings: []types.ReservedInstancesOffering{{
				ReservedInstancesOfferingId: aws.String("offering-default-tenancy"),
				InstanceType:                types.InstanceTypeM5Large,
				Duration:                    aws.Int64(OneYearSeconds),
				OfferingType:                types.OfferingTypeValuesNoUpfront,
				ProductDescription:          types.RIProductDescriptionLinuxUnix,
				InstanceTenancy:             types.TenancyDefault,
			}},
		}, nil).Maybe()
	mockEC2.On("PurchaseReservedInstancesOffering", mock.Anything, mock.Anything).
		Return(&ec2.PurchaseReservedInstancesOfferingOutput{ReservedInstancesId: aws.String("ri-wrong")}, nil).Maybe()
	mockEC2.On("CreateTags", mock.Anything, mock.Anything).Return(&ec2.CreateTagsOutput{}, nil).Maybe()
	return mockEC2
}

// Issue #23: an empty or unsupported tenancy/scope must fail before any AWS
// call instead of buying a default-tenancy or regional RI.
func TestPurchaseCommitment_InvalidTenancyOrScope_ErrorsBeforeAPICall(t *testing.T) {
	t.Parallel()
	for _, tc := range invalidTenancyScopeCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mockEC2 := matchingOfferingMock()
			client := &Client{client: mockEC2, region: "us-east-1"}
			rec := common.Recommendation{
				ResourceType:  "m5.large",
				Count:         1,
				PaymentOption: "no-upfront",
				Term:          "1yr",
				Details: &common.ComputeDetails{
					Platform: "Linux/UNIX",
					Tenancy:  tc.tenancy,
					Scope:    tc.scope,
				},
			}

			result, err := client.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})

			require.Error(t, err)
			assert.Contains(t, err.Error(), "unsupported EC2 RI")
			assert.False(t, result.Success)
			mockEC2.AssertNotCalled(t, "DescribeReservedInstancesOfferings", mock.Anything, mock.Anything)
			mockEC2.AssertNotCalled(t, "PurchaseReservedInstancesOffering", mock.Anything, mock.Anything)
		})
	}
}

// TestFindConvertibleOffering_InvalidTenancyOrScope_ErrorsBeforeAPICall covers
// the exchange-target lookup, whose offering ID is then bought by an exchange.
func TestFindConvertibleOffering_InvalidTenancyOrScope_ErrorsBeforeAPICall(t *testing.T) {
	t.Parallel()
	for _, tc := range invalidTenancyScopeCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mockEC2 := matchingOfferingMock()
			client := &Client{client: mockEC2, region: "us-east-1"}

			_, err := client.FindConvertibleOffering(context.Background(), FindConvertibleOfferingParams{
				InstanceType:       "m5.large",
				ProductDescription: "Linux/UNIX",
				Tenancy:            tc.tenancy,
				Scope:              tc.scope,
				Duration:           OneYearSeconds,
			})

			require.Error(t, err)
			assert.Contains(t, err.Error(), "unsupported EC2 RI")
			mockEC2.AssertNotCalled(t, "DescribeReservedInstancesOfferings", mock.Anything, mock.Anything)
		})
	}
}

// The exchange-target picker must not list shared-tenancy or regional targets
// for a source RI whose tenancy or scope is missing.
func TestListTargetOfferings_InvalidTenancyOrScope_ErrorsBeforeAPICall(t *testing.T) {
	t.Parallel()
	for _, tc := range invalidTenancyScopeCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mockEC2 := matchingOfferingMock()
			client := &Client{client: mockEC2, region: "us-east-1"}

			_, err := client.ListTargetOfferings(context.Background(), ListTargetOfferingsParams{
				ProductDescription: "Linux/UNIX",
				Tenancy:            tc.tenancy,
				Scope:              tc.scope,
				Duration:           OneYearSeconds,
			})

			require.Error(t, err)
			assert.Contains(t, err.Error(), "unsupported EC2 RI")
			mockEC2.AssertNotCalled(t, "DescribeReservedInstancesOfferings", mock.Anything, mock.Anything)
		})
	}
}

// validTenancyScopeCases are the accepted tenancy/scope spellings with the
// values the exchange lookups must send to AWS.
var validTenancyScopeCases = []struct {
	name        string
	tenancy     string
	scope       string
	wantTenancy types.Tenancy
	wantScope   string
}{
	{"default_region", "default", "Region", types.TenancyDefault, "Region"},
	{"dedicated_az", "dedicated", "Availability Zone", types.TenancyDedicated, "Availability Zone"},
	{"legacy_shared_region", "shared", "region", types.TenancyDefault, "Region"},
	{"legacy_az_hyphenated", "dedicated", "availability-zone", types.TenancyDedicated, "Availability Zone"},
}

// captureOfferingsInput returns a mock that records the request it receives
// and answers with a single offering.
func captureOfferingsInput(offeringID string, got **ec2.DescribeReservedInstancesOfferingsInput) *MockEC2Client {
	mockEC2 := &MockEC2Client{}
	mockEC2.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			*got = args.Get(1).(*ec2.DescribeReservedInstancesOfferingsInput)
		}).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{
			ReservedInstancesOfferings: []types.ReservedInstancesOffering{{
				ReservedInstancesOfferingId: aws.String(offeringID),
				InstanceType:                types.InstanceTypeM5Large,
			}},
		}, nil).Once()
	return mockEC2
}

func filterValues(filters []types.Filter, name string) []string {
	for _, f := range filters {
		if aws.ToString(f.Name) == name {
			return f.Values
		}
	}
	return nil
}

func TestFindConvertibleOffering_TenancyScopeVariants_SendEnumValues(t *testing.T) {
	t.Parallel()
	for _, tc := range validTenancyScopeCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got *ec2.DescribeReservedInstancesOfferingsInput
			client := &Client{client: captureOfferingsInput("offering-"+tc.name, &got), region: "us-east-1"}

			id, err := client.FindConvertibleOffering(context.Background(), FindConvertibleOfferingParams{
				InstanceType:       "m5.large",
				ProductDescription: "Linux/UNIX",
				Tenancy:            tc.tenancy,
				Scope:              tc.scope,
				Duration:           OneYearSeconds,
			})

			require.NoError(t, err)
			assert.Equal(t, "offering-"+tc.name, id)
			require.NotNil(t, got)
			assert.Equal(t, tc.wantTenancy, got.InstanceTenancy)
			assert.Nil(t, filterValues(got.Filters, "instance-tenancy"), "instance-tenancy is not a documented filter")
			assert.Equal(t, []string{tc.wantScope}, filterValues(got.Filters, "scope"))
			assert.Equal(t, []string{"m5.large"}, filterValues(got.Filters, "instance-type"))
			assert.Equal(t, types.OfferingClassTypeConvertible, got.OfferingClass)
			assert.Nil(t, filterValues(got.Filters, "offering-class"), "offering-class is not a documented filter")
		})
	}
}

func TestListTargetOfferings_TenancyScopeVariants_SendEnumValues(t *testing.T) {
	t.Parallel()
	for _, tc := range validTenancyScopeCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got *ec2.DescribeReservedInstancesOfferingsInput
			client := &Client{client: captureOfferingsInput("offering-"+tc.name, &got), region: "us-east-1"}

			offerings, err := client.ListTargetOfferings(context.Background(), ListTargetOfferingsParams{
				ProductDescription: "Linux/UNIX",
				Tenancy:            tc.tenancy,
				Scope:              tc.scope,
				Duration:           OneYearSeconds,
			})

			require.NoError(t, err)
			require.Len(t, offerings, 1)
			assert.Equal(t, "offering-"+tc.name, offerings[0].OfferingID)
			assert.Equal(t, tc.wantScope, offerings[0].Scope)
			require.NotNil(t, got)
			assert.Equal(t, tc.wantTenancy, got.InstanceTenancy)
			assert.Equal(t, []string{tc.wantScope}, filterValues(got.Filters, "scope"))
			assert.Equal(t, types.OfferingClassTypeConvertible, got.OfferingClass)
		})
	}
}

// Issue #211: a re-drive that adopts the RI an earlier attempt bought must say
// so, or the caller cannot tell it from a fresh purchase.
func TestPurchaseCommitment_ExistingCommitmentFlag(t *testing.T) {
	t.Parallel()
	token := common.DeriveIdempotencyToken("exec-211-ec2", 0)
	rec := common.Recommendation{
		ResourceType:  "t3.micro",
		Count:         1,
		PaymentOption: "all-upfront",
		Term:          "1yr",
		Details:       &common.ComputeDetails{Platform: "Linux/UNIX", Tenancy: "default", Scope: "Region"},
	}
	tokenLookup := mock.MatchedBy(func(in *ec2.DescribeReservedInstancesInput) bool {
		for _, f := range in.Filters {
			if aws.ToString(f.Name) == "tag:"+common.IdempotencyTagKey {
				return true
			}
		}
		return false
	})
	offerings := &ec2.DescribeReservedInstancesOfferingsOutput{
		ReservedInstancesOfferings: []types.ReservedInstancesOffering{{
			ReservedInstancesOfferingId: aws.String("offering-211"),
			InstanceType:                types.InstanceTypeT3Micro,
			Duration:                    aws.Int64(31536000),
			OfferingType:                types.OfferingTypeValuesAllUpfront,
			ProductDescription:          types.RIProductDescriptionLinuxUnix,
			InstanceTenancy:             types.TenancyDefault,
			FixedPrice:                  aws.Float32(100.0),
		}},
	}

	t.Run("fresh purchase is not flagged", func(t *testing.T) {
		t.Parallel()
		m := &MockEC2Client{}
		client := &Client{client: m, region: "us-east-1"}
		m.On("DescribeReservedInstances", mock.Anything, tokenLookup).
			Return(&ec2.DescribeReservedInstancesOutput{}, nil).Once()
		m.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).Return(offerings, nil)
		m.On("PurchaseReservedInstancesOffering", mock.Anything, mock.Anything).
			Return(&ec2.PurchaseReservedInstancesOfferingOutput{ReservedInstancesId: aws.String("ri-fresh-211")}, nil)
		m.On("CreateTags", mock.Anything, mock.Anything).Return(&ec2.CreateTagsOutput{}, nil)

		result, err := client.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{IdempotencyToken: token})
		require.NoError(t, err)
		assert.True(t, result.Success)
		assert.Equal(t, "ri-fresh-211", result.CommitmentID)
		assert.False(t, result.ExistingCommitment)
		m.AssertExpectations(t)
	})

	t.Run("failed purchase is not flagged", func(t *testing.T) {
		t.Parallel()
		m := &MockEC2Client{}
		client := &Client{client: m, region: "us-east-1"}
		m.On("DescribeReservedInstances", mock.Anything, tokenLookup).
			Return(&ec2.DescribeReservedInstancesOutput{}, nil).Once()
		m.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).Return(offerings, nil)
		m.On("PurchaseReservedInstancesOffering", mock.Anything, mock.Anything).
			Return((*ec2.PurchaseReservedInstancesOfferingOutput)(nil), errors.New("boom"))

		result, err := client.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{IdempotencyToken: token})
		require.Error(t, err)
		assert.False(t, result.Success)
		assert.False(t, result.ExistingCommitment)
	})

	// The wrong-class incident: the RI an earlier attempt bought is Standard but
	// the operator has since configured Convertible. The re-drive still adopts it
	// (no offering lookup, no second purchase), and the flag is what lets the
	// caller avoid recording the configured class for it.
	t.Run("re-drive with a different configured class is flagged", func(t *testing.T) {
		t.Parallel()
		m := &MockEC2Client{}
		client := &Client{client: m, region: "us-east-1"}
		m.On("DescribeReservedInstances", mock.Anything, tokenLookup).
			Return(&ec2.DescribeReservedInstancesOutput{ReservedInstances: []types.ReservedInstances{
				{ReservedInstancesId: aws.String("ri-standard-211"), OfferingClass: types.OfferingClassTypeStandard},
			}}, nil).Once()

		result, err := client.PurchaseCommitment(context.Background(), rec,
			common.PurchaseOptions{IdempotencyToken: token, OfferingClass: "convertible"})
		require.NoError(t, err)
		assert.True(t, result.Success)
		assert.Equal(t, "ri-standard-211", result.CommitmentID)
		assert.True(t, result.ExistingCommitment)
		m.AssertExpectations(t)
		m.AssertNotCalled(t, "PurchaseReservedInstancesOffering", mock.Anything, mock.Anything)
	})
}

func zonalRec(az string) common.Recommendation {
	return common.Recommendation{
		ResourceType:  "m5.large",
		PaymentOption: "all-upfront",
		Term:          "1yr",
		Details: &common.ComputeDetails{
			Platform:         "Linux/UNIX",
			Tenancy:          "default",
			Scope:            "Availability Zone",
			AvailabilityZone: az,
		},
	}
}

func azOffering(id, az string) types.ReservedInstancesOffering {
	o := types.ReservedInstancesOffering{
		ReservedInstancesOfferingId: aws.String(id),
		InstanceType:                types.InstanceTypeM5Large,
		OfferingType:                types.OfferingTypeValuesAllUpfront,
	}
	if az != "" {
		o.AvailabilityZone = aws.String(az)
	}
	return o
}

// Issue #289: the query builder must refuse zonal scope without an AZ and
// regional scope with one.
func TestBuildEC2OfferingQuery_AvailabilityZone(t *testing.T) {
	t.Parallel()

	t.Run("zonal without AZ errors", func(t *testing.T) {
		t.Parallel()
		rec := zonalRec("")
		_, err := buildEC2OfferingQuery(rec, rec.Details.(*common.ComputeDetails), OneYearSeconds)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "re-fetch")
	})
	t.Run("whitespace-only AZ errors", func(t *testing.T) {
		t.Parallel()
		rec := zonalRec("  ")
		_, err := buildEC2OfferingQuery(rec, rec.Details.(*common.ComputeDetails), OneYearSeconds)
		require.Error(t, err)
	})
	t.Run("regional with AZ errors", func(t *testing.T) {
		t.Parallel()
		rec := zonalRec("us-east-1b")
		d := rec.Details.(*common.ComputeDetails)
		d.Scope = "Region"
		_, err := buildEC2OfferingQuery(rec, d, OneYearSeconds)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "re-fetch")
	})
	t.Run("zonal with AZ carries it", func(t *testing.T) {
		t.Parallel()
		rec := zonalRec("us-east-1b")
		q, err := buildEC2OfferingQuery(rec, rec.Details.(*common.ComputeDetails), OneYearSeconds)
		require.NoError(t, err)
		assert.Equal(t, "us-east-1b", q.availabilityZone)
	})
}

// Request layer: AZ is sent for zonal queries and absent for regional ones.
func TestDescribeInputFromQuery_AvailabilityZone(t *testing.T) {
	t.Parallel()
	q := ec2OfferingQuery{scope: types.ScopeAvailabilityZone, availabilityZone: "us-east-1b", duration: OneYearSeconds}
	assert.Equal(t, "us-east-1b", aws.ToString(describeInputFromQuery(q, nil).AvailabilityZone))

	q = ec2OfferingQuery{scope: types.ScopeRegional, duration: OneYearSeconds}
	assert.Nil(t, describeInputFromQuery(q, nil).AvailabilityZone)
}

// Scan layer: wrong-AZ and nil-AZ offerings are skipped for zonal queries;
// a regional query does no AZ filtering.
func TestScanEC2OfferingPage_AvailabilityZone(t *testing.T) {
	t.Parallel()
	offerings := []types.ReservedInstancesOffering{
		azOffering("nil-az", ""),
		azOffering("in-1a", "us-east-1a"),
		azOffering("in-1b", "us-east-1b"),
	}
	zonal := ec2OfferingQuery{availabilityZone: "us-east-1b", wantOfferingType: types.OfferingTypeValuesAllUpfront}
	got := scanEC2OfferingPage(offerings, zonal)
	require.NotNil(t, got)
	assert.Equal(t, "in-1b", aws.ToString(got.ReservedInstancesOfferingId))

	assert.Nil(t, scanEC2OfferingPage(offerings[:2], zonal), "no 1b offering must yield nil, not a 1a buy")

	regional := ec2OfferingQuery{wantOfferingType: types.OfferingTypeValuesAllUpfront}
	got = scanEC2OfferingPage(offerings, regional)
	require.NotNil(t, got)
	assert.Equal(t, "nil-az", aws.ToString(got.ReservedInstancesOfferingId))
}

// End to end through findOffering: a CE rec for us-east-1b sends the AZ and
// buys the 1b offering even when the server returns 1a first.
func TestFindOffering_ZonalRecKeepsAZ(t *testing.T) {
	t.Parallel()
	cap := &capturingMockEC2Client{}
	cap.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{
			ReservedInstancesOfferings: []types.ReservedInstancesOffering{
				azOffering("in-1a", "us-east-1a"),
				azOffering("in-1b", "us-east-1b"),
			},
		}, nil).Once()
	client := &Client{client: cap, region: "us-east-1"}

	offering, err := client.findOffering(context.Background(), zonalRec("us-east-1b"), "", "")
	require.NoError(t, err)
	assert.Equal(t, "in-1b", aws.ToString(offering.ReservedInstancesOfferingId))
	require.NotNil(t, cap.LastDescribeOfferingsInput)
	assert.Equal(t, "us-east-1b", aws.ToString(cap.LastDescribeOfferingsInput.AvailabilityZone))
}

func TestFindOffering_ZonalRecWithoutAZRefusedBeforeAPICall(t *testing.T) {
	t.Parallel()
	cap := &capturingMockEC2Client{}
	client := &Client{client: cap, region: "us-east-1"}
	_, err := client.findOffering(context.Background(), zonalRec(""), "", "")
	require.Error(t, err)
	assert.Nil(t, cap.LastDescribeOfferingsInput)
}
