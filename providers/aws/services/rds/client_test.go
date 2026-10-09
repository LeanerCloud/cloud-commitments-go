package rds

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockRDSClient implements API for testing.
type MockRDSClient struct {
	mock.Mock
}

func (m *MockRDSClient) DescribeReservedDBInstancesOfferings(ctx context.Context, params *rds.DescribeReservedDBInstancesOfferingsInput, optFns ...func(*rds.Options)) (*rds.DescribeReservedDBInstancesOfferingsOutput, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*rds.DescribeReservedDBInstancesOfferingsOutput), args.Error(1)
}

func (m *MockRDSClient) PurchaseReservedDBInstancesOffering(ctx context.Context, params *rds.PurchaseReservedDBInstancesOfferingInput, optFns ...func(*rds.Options)) (*rds.PurchaseReservedDBInstancesOfferingOutput, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*rds.PurchaseReservedDBInstancesOfferingOutput), args.Error(1)
}

func (m *MockRDSClient) DescribeReservedDBInstances(ctx context.Context, params *rds.DescribeReservedDBInstancesInput, optFns ...func(*rds.Options)) (*rds.DescribeReservedDBInstancesOutput, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*rds.DescribeReservedDBInstancesOutput), args.Error(1)
}

func TestClient_ReservationStatesPreventDuplicatePurchases(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state *string
		owned bool
	}{
		{"active", aws.String("active"), true},
		{"paying", aws.String("payment-pending"), true},
		{"queued", aws.String("queued"), true},
		{"unknown", aws.String("future-provider-state"), true},
		{"missing", nil, true},
		{"empty", aws.String(""), true},
		{"payment_failed", aws.String("payment-failed"), false},
		{"retired", aws.String("retired"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := idempotencyTestRec()
			rec.Provider, rec.Region = common.ProviderAWS, "eu-west-1"

			token := common.DeriveIdempotencyToken("state-regression", 0)
			id := common.IdempotentReservationID("rds-id-", token)
			response := &rds.DescribeReservedDBInstancesOutput{ReservedDBInstances: []types.ReservedDBInstance{{
				ReservedDBInstanceId: aws.String(id), State: tc.state, StartTime: aws.Time(time.Now().Add(-time.Hour)),
				DBInstanceClass: aws.String(rec.ResourceType), DBInstanceCount: aws.Int32(1), ProductDescription: aws.String("mysql"), MultiAZ: aws.Bool(false), Duration: aws.Int32(31536000),
			}}}
			t.Run("dedupe", func(t *testing.T) {
				m := &MockRDSClient{}
				t.Cleanup(func() { m.AssertExpectations(t) })
				m.On("DescribeReservedDBInstances", mock.Anything, mock.Anything).Return(response, nil)
				client := &Client{client: m, region: rec.Region}
				for _, count := range []int{1, 2} {
					rec.Count = count
					passed, filtered, err := recfilter.NewDuplicateChecker(0).AdjustRecommendationsForExisting(context.Background(), []common.Recommendation{rec}, client)
					require.NoError(t, err)
					if tc.owned && count == 1 {
						assert.Empty(t, passed, "an owned reservation must suppress the duplicate")
						assert.Len(t, filtered, 1)
					} else {
						require.Len(t, passed, 1)
						want := count
						if tc.owned {
							want--
						}
						assert.Equal(t, want, passed[0].Count)
						assert.Empty(t, filtered)
					}
				}
				listed, err := client.GetExistingCommitments(context.Background())
				require.NoError(t, err)
				if !tc.owned {
					assert.Empty(t, listed)
					return
				}
				require.Len(t, listed, 1)
				wantState := common.CommitmentState(aws.ToString(tc.state))

				assert.Equal(t, wantState, listed[0].State)
				response.ReservedDBInstances[0].StartTime = aws.Time(time.Now().Add(-48 * time.Hour))
				passed, filtered, err := recfilter.NewDuplicateChecker(0).AdjustRecommendationsForExisting(context.Background(), []common.Recommendation{rec}, client)
				require.NoError(t, err)
				require.Len(t, passed, 1)
				assert.Equal(t, rec.Count, passed[0].Count, "old reservations stay outside the dedupe window")
				assert.Empty(t, filtered)
			})
			t.Run("purchase_retry", func(t *testing.T) {
				rec.Count = 1
				m := &MockRDSClient{}
				t.Cleanup(func() { m.AssertExpectations(t) })
				expectOffering(m)
				m.On("DescribeReservedDBInstances", mock.Anything, mock.Anything).Return(response, nil)
				sentinel := fmt.Errorf("purchase boundary reached")
				m.On("PurchaseReservedDBInstancesOffering", mock.Anything, mock.Anything).Return((*rds.PurchaseReservedDBInstancesOfferingOutput)(nil), sentinel).Maybe()
				client := &Client{client: m, region: rec.Region}
				result, err := client.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{IdempotencyToken: token})
				if tc.owned {
					assert.NoError(t, err)
					assert.True(t, result.Success)
					assert.Equal(t, id, result.CommitmentID)
					m.AssertNotCalled(t, "PurchaseReservedDBInstancesOffering", mock.Anything, mock.Anything)
				} else {
					assert.ErrorIs(t, err, sentinel)
					assert.False(t, result.Success)
					m.AssertNumberOfCalls(t, "PurchaseReservedDBInstancesOffering", 1)
				}
			})
		})
	}
}

func TestNewClient(t *testing.T) {
	cfg := aws.Config{
		Region: "us-east-1",
	}

	client := NewClient(cfg)

	assert.NotNil(t, client)
	assert.NotNil(t, client.client)
	assert.Equal(t, "us-east-1", client.region)
}

func TestClient_GetServiceType(t *testing.T) {
	client := &Client{region: "us-east-1"}
	assert.Equal(t, common.ServiceRelationalDB, client.GetServiceType())
}

func TestClient_GetRegion(t *testing.T) {
	client := &Client{region: "eu-west-1"}
	assert.Equal(t, "eu-west-1", client.GetRegion())
}

func TestClient_GetRecommendations(t *testing.T) {
	client := &Client{region: "us-east-1"}
	recs, err := client.GetRecommendations(context.Background(), &common.RecommendationParams{})
	assert.NoError(t, err)
	assert.Empty(t, recs)
}

func TestClient_GetExistingCommitments(t *testing.T) {
	tests := []struct {
		name        string
		setupMocks  func(*MockRDSClient)
		expectedLen int
		expectError bool
	}{
		{
			name: "successful retrieval with active instances",
			setupMocks: func(m *MockRDSClient) {
				m.On("DescribeReservedDBInstances", mock.Anything, mock.Anything).
					Return(&rds.DescribeReservedDBInstancesOutput{
						ReservedDBInstances: []types.ReservedDBInstance{
							{
								ReservedDBInstanceId: aws.String("ri-123"),
								DBInstanceClass:      aws.String("db.t3.micro"),
								DBInstanceCount:      aws.Int32(2),
								ProductDescription:   aws.String("mysql"),
								State:                aws.String("active"),
								Duration:             aws.Int32(31536000),
								StartTime:            aws.Time(time.Now()),
								OfferingType:         aws.String("Partial Upfront"),
							},
							{
								ReservedDBInstanceId: aws.String("ri-456"),
								DBInstanceClass:      aws.String("db.m5.large"),
								DBInstanceCount:      aws.Int32(1),
								ProductDescription:   aws.String("postgres"),
								State:                aws.String("payment-pending"),
								Duration:             aws.Int32(94608000),
								StartTime:            aws.Time(time.Now()),
								OfferingType:         aws.String("All Upfront"),
							},
						},
						Marker: nil,
					}, nil).Once()
			},
			expectedLen: 2,
			expectError: false,
		},
		{
			name: "filters out retired instances",
			setupMocks: func(m *MockRDSClient) {
				m.On("DescribeReservedDBInstances", mock.Anything, mock.Anything).
					Return(&rds.DescribeReservedDBInstancesOutput{
						ReservedDBInstances: []types.ReservedDBInstance{
							{
								ReservedDBInstanceId: aws.String("ri-123"),
								DBInstanceClass:      aws.String("db.t3.micro"),
								DBInstanceCount:      aws.Int32(2),
								State:                aws.String("active"),
								Duration:             aws.Int32(31536000),
								StartTime:            aws.Time(time.Now()),
							},
							{
								ReservedDBInstanceId: aws.String("ri-retired"),
								DBInstanceClass:      aws.String("db.m5.large"),
								DBInstanceCount:      aws.Int32(1),
								State:                aws.String("retired"),
								Duration:             aws.Int32(94608000),
								StartTime:            aws.Time(time.Now()),
							},
						},
						Marker: nil,
					}, nil).Once()
			},
			expectedLen: 1,
			expectError: false,
		},
		{
			name: "API error",
			setupMocks: func(m *MockRDSClient) {
				m.On("DescribeReservedDBInstances", mock.Anything, mock.Anything).
					Return(nil, fmt.Errorf("API error")).Once()
			},
			expectedLen: 0,
			expectError: true,
		},
		{
			name: "empty result",
			setupMocks: func(m *MockRDSClient) {
				m.On("DescribeReservedDBInstances", mock.Anything, mock.Anything).
					Return(&rds.DescribeReservedDBInstancesOutput{
						ReservedDBInstances: []types.ReservedDBInstance{},
						Marker:              nil,
					}, nil).Once()
			},
			expectedLen: 0,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockRDSClient{}
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
	tests := []struct {
		name          string
		setupMocks    func(*MockRDSClient)
		expectedTypes []string
		expectError   bool
	}{
		{
			name: "successful retrieval single page",
			setupMocks: func(m *MockRDSClient) {
				m.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
					Return(&rds.DescribeReservedDBInstancesOfferingsOutput{
						ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{
							{DBInstanceClass: aws.String("db.t3.micro")},
							{DBInstanceClass: aws.String("db.t3.small")},
							{DBInstanceClass: aws.String("db.m5.large")},
						},
						Marker: nil,
					}, nil).Once()
			},
			expectedTypes: []string{"db.m5.large", "db.t3.micro", "db.t3.small"},
			expectError:   false,
		},
		{
			name: "API error",
			setupMocks: func(m *MockRDSClient) {
				m.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
					Return(nil, fmt.Errorf("API error")).Once()
			},
			expectedTypes: nil,
			expectError:   true,
		},
		{
			name: "deduplicates instance types",
			setupMocks: func(m *MockRDSClient) {
				m.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
					Return(&rds.DescribeReservedDBInstancesOfferingsOutput{
						ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{
							{DBInstanceClass: aws.String("db.t3.micro")},
							{DBInstanceClass: aws.String("db.t3.micro")},
							{DBInstanceClass: aws.String("db.m5.large")},
						},
						Marker: nil,
					}, nil).Once()
			},
			expectedTypes: []string{"db.m5.large", "db.t3.micro"},
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockRDSClient{}
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
	mockRDS := &MockRDSClient{}
	client := &Client{
		client: mockRDS,
		region: "us-east-1",
	}

	rec := common.Recommendation{
		Service:       common.ServiceRelationalDB,
		ResourceType:  "db.t3.medium",
		PaymentOption: "no-upfront",
		Term:          "3yr",
		Details: &common.DatabaseDetails{
			Engine:   "mysql",
			AZConfig: "multi-az",
		},
	}

	mockRDS.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{
			ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{
				{
					ReservedDBInstancesOfferingId: aws.String("offering-123"),
					DBInstanceClass:               aws.String("db.t3.medium"),
					Duration:                      aws.Int32(94608000),
					OfferingType:                  aws.String("No Upfront"),
					MultiAZ:                       aws.Bool(true),
					ProductDescription:            aws.String("mysql"),
				},
			},
		}, nil)

	err := client.ValidateOffering(context.Background(), rec)
	assert.NoError(t, err)
	mockRDS.AssertExpectations(t)
}

func TestClient_ValidateOffering_NotFound(t *testing.T) {
	mockRDS := &MockRDSClient{}
	client := &Client{
		client: mockRDS,
		region: "us-east-1",
	}

	rec := common.Recommendation{
		Service:       common.ServiceRelationalDB,
		ResourceType:  "db.t3.medium",
		PaymentOption: "no-upfront",
		Term:          "3yr",
		Details: &common.DatabaseDetails{
			Engine:   "mysql",
			AZConfig: "multi-az",
		},
	}

	mockRDS.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{
			ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{},
		}, nil)

	err := client.ValidateOffering(context.Background(), rec)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no offerings found")
	mockRDS.AssertExpectations(t)
}

func TestClient_PurchaseCommitment(t *testing.T) {
	mockRDS := &MockRDSClient{}
	client := &Client{
		client: mockRDS,
		region: "eu-west-1",
	}

	rec := common.Recommendation{
		Service:       common.ServiceRelationalDB,
		ResourceType:  "db.r6g.xlarge",
		Count:         2,
		PaymentOption: "partial-upfront",
		Term:          "3yr",
		Details: &common.DatabaseDetails{
			Engine:   "aurora-mysql",
			AZConfig: "multi-az",
		},
	}

	mockRDS.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{
			ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{
				{
					ReservedDBInstancesOfferingId: aws.String("offering-456"),
					DBInstanceClass:               aws.String("db.r6g.xlarge"),
					Duration:                      aws.Int32(94608000),
					OfferingType:                  aws.String("Partial Upfront"),
					MultiAZ:                       aws.Bool(true),
					ProductDescription:            aws.String("aurora-mysql"),
					FixedPrice:                    aws.Float64(5000.0),
				},
			},
		}, nil)

	mockRDS.On("PurchaseReservedDBInstancesOffering", mock.Anything, mock.Anything).
		Return(&rds.PurchaseReservedDBInstancesOfferingOutput{
			ReservedDBInstance: &types.ReservedDBInstance{
				ReservedDBInstanceId: aws.String("ri-789"),
				DBInstanceClass:      aws.String("db.r6g.xlarge"),
				DBInstanceCount:      aws.Int32(2),
				FixedPrice:           aws.Float64(5000.0),
				StartTime:            aws.Time(time.Now()),
				State:                aws.String("payment-pending"),
			},
		}, nil)

	result, err := client.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})

	assert.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "ri-789", result.CommitmentID)
	require.NotNil(t, result.Cost, "upfront cost must be recorded")
	assert.Equal(t, 10000.0, *result.Cost, "total upfront is per-instance FixedPrice x DBInstanceCount")
	mockRDS.AssertExpectations(t)
}

func TestClient_PurchaseCommitment_EmptyResponse(t *testing.T) {
	mockRDS := &MockRDSClient{}
	client := &Client{
		client: mockRDS,
		region: "us-east-1",
	}

	rec := common.Recommendation{
		Service:       common.ServiceRelationalDB,
		ResourceType:  "db.t3.micro",
		Count:         1,
		PaymentOption: "all-upfront",
		Term:          "1yr",
		Details: &common.DatabaseDetails{
			Engine:   "mysql",
			AZConfig: "single-az",
		},
	}

	mockRDS.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{
			ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{
				{
					ReservedDBInstancesOfferingId: aws.String("offering-123"),
					DBInstanceClass:               aws.String("db.t3.micro"),
					ProductDescription:            aws.String("mysql"),
					MultiAZ:                       aws.Bool(false),
					OfferingType:                  aws.String("All Upfront"),
					Duration:                      aws.Int32(31536000),
				},
			},
		}, nil)

	mockRDS.On("PurchaseReservedDBInstancesOffering", mock.Anything, mock.Anything).
		Return(&rds.PurchaseReservedDBInstancesOfferingOutput{
			ReservedDBInstance: nil,
		}, nil)

	result, err := client.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})

	assert.Error(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error.Error(), "empty")
	mockRDS.AssertExpectations(t)
}

func TestClient_GetOfferingDetails(t *testing.T) {
	mockRDS := &MockRDSClient{}
	client := &Client{
		client: mockRDS,
		region: "us-east-2",
	}

	rec := common.Recommendation{
		Service:       common.ServiceRelationalDB,
		ResourceType:  "db.m6g.large",
		PaymentOption: "all-upfront",
		Term:          "1yr",
		Details: &common.DatabaseDetails{
			Engine:   "postgres",
			AZConfig: "multi-az",
		},
	}

	mockRDS.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{
			ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{
				{
					ReservedDBInstancesOfferingId: aws.String("offering-999"),
					DBInstanceClass:               aws.String("db.m6g.large"),
					Duration:                      aws.Int32(31536000),
					OfferingType:                  aws.String("All Upfront"),
					MultiAZ:                       aws.Bool(true),
					ProductDescription:            aws.String("postgresql"),
					FixedPrice:                    aws.Float64(3500.0),
					UsagePrice:                    aws.Float64(0.0),
					CurrencyCode:                  aws.String("USD"),
				},
			},
		}, nil).Twice()

	details, err := client.GetOfferingDetails(context.Background(), rec)

	assert.NoError(t, err)
	assert.NotNil(t, details)
	assert.Equal(t, "offering-999", details.OfferingID)
	assert.Equal(t, "db.m6g.large", details.ResourceType)
	assert.Equal(t, 3500.0, details.UpfrontCost)
	assert.Equal(t, "USD", details.Currency)
	mockRDS.AssertExpectations(t)
}

func TestClient_NormalizeEngineName(t *testing.T) {
	client := &Client{}

	tests := []struct {
		name        string
		input       string
		expected    string
		expectError bool
	}{
		{"Aurora MySQL uppercase", "Aurora-MySQL", "aurora-mysql", false},
		{"Aurora PostgreSQL mixed case", "Aurora-PostgreSQL", "aurora-postgresql", false},
		// Previously "Aurora" without a database variant silently defaulted to
		// aurora-mysql. Now it must error: aurora-mysql and aurora-postgresql have
		// different RI prices and the caller must supply the precise variant.
		{"Aurora no variant errors", "Aurora", "", true},
		{"MySQL", "MySQL", "mysql", false},
		{"PostgreSQL", "PostgreSQL", "postgresql", false},
		{"MariaDB", "MariaDB", "mariadb", false},
		// Bare family names are ambiguous; only the bare names error.
		{"Oracle bare errors", "oracle", "", true},
		{"Oracle title-case bare errors", "Oracle", "", true},
		{"SQL Server bare errors", "sqlserver", "", true},
		{"SQL Server hyphen bare errors", "sql-server", "", true},
		{"SQL Server camelcase errors", "SQLServer", "", true},
		// Edition-qualified strings are valid RDS ProductDescription values and must pass through.
		{"Oracle EE edition passes", "oracle-ee", "oracle-ee", false},
		{"Oracle SE2 edition passes", "oracle-se2", "oracle-se2", false},
		{"Oracle EE mixed case passes", "Oracle-EE", "oracle-ee", false},
		{"SQL Server SE edition passes", "sqlserver-se", "sqlserver-se", false},
		{"SQL Server web edition passes", "sqlserver-web", "sqlserver-web", false},
		{"SQL Server hyphen-ex edition passes", "sql-server-ex", "sql-server-ex", false},
		{"Already normalized aurora-mysql", "aurora-mysql", "aurora-mysql", false},
		{"Already normalized aurora-postgresql", "aurora-postgresql", "aurora-postgresql", false},
		{"Already normalized postgres", "postgres", "postgresql", false},
		{"Unknown engine passes through", "custom-db", "custom-db", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := client.normalizeEngineName(tt.input)
			if tt.expectError {
				assert.Error(t, err, "expected error for engine %q", tt.input)
				assert.Empty(t, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestClient_ConvertPaymentOption(t *testing.T) {
	client := &Client{}

	tests := []struct {
		name        string
		input       string
		expected    string
		expectError bool
	}{
		{"All Upfront", "all-upfront", "All Upfront", false},
		{"Partial Upfront", "partial-upfront", "Partial Upfront", false},
		{"No Upfront", "no-upfront", "No Upfront", false},
		{"Unknown returns error", "unknown", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := client.convertPaymentOption(tt.input)
			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, "", result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestClient_GetDurationString(t *testing.T) {
	client := &Client{}

	tests := []struct {
		name      string
		term      string
		expected  string
		expectErr bool
	}{
		{"1 year", "1yr", "31536000", false},
		{"1 numeric", "1", "31536000", false},
		{"3 years", "3yr", "94608000", false},
		{"3 numeric", "3", "94608000", false},
		// Regression for ARCH-04 (issue #1192): unrecognized or empty terms
		// must error instead of silently mapping to a 1-year purchase. An
		// empty term is what a 0/NULL Term DB row produces on the scheduler
		// purchase path.
		{"invalid term errors", "invalid", "", true},
		{"empty term errors", "", "", true},
		{"zero term errors", "0", "", true},
		{"2yr term errors", "2yr", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := client.getDurationString(tt.term)
			if tt.expectErr {
				if assert.Error(t, err) {
					assert.Contains(t, err.Error(), "unsupported RDS reservation term")
				}
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCreatePurchaseTags_IncludesPurchaseAutomation(t *testing.T) {
	c := &Client{}
	rec := common.Recommendation{ResourceType: "db.m5.large", Region: "eu-west-1"}
	tags := c.createPurchaseTags(rec, common.PurchaseSourceCLI)
	var found bool
	for _, tag := range tags {
		if aws.ToString(tag.Key) == common.PurchaseTagKey {
			assert.Equal(t, common.PurchaseSourceCLI, aws.ToString(tag.Value))
			found = true
		}
	}
	assert.True(t, found, "expected purchase-automation tag to be present when source is set")
}

func TestCreatePurchaseTags_OmitsPurchaseAutomationWhenSourceEmpty(t *testing.T) {
	c := &Client{}
	rec := common.Recommendation{ResourceType: "db.m5.large", Region: "eu-west-1"}
	tags := c.createPurchaseTags(rec, "")
	for _, tag := range tags {
		assert.NotEqual(t, common.PurchaseTagKey, aws.ToString(tag.Key), "tag must be skipped when source is empty")
	}
}

// idempotencyTestRec is a minimal RDS recommendation whose offering resolves to
// "offering-1" via the mock below.
func idempotencyTestRec() common.Recommendation {
	return common.Recommendation{
		Service:       common.ServiceRelationalDB,
		ResourceType:  "db.r6g.large",
		Count:         1,
		PaymentOption: "all-upfront",
		Term:          "1yr",
		Details: &common.DatabaseDetails{
			Engine:   "mysql",
			AZConfig: "single-az",
		},
	}
}

func expectOffering(m *MockRDSClient) { expectOfferingForClass(m, "db.r6g.large") }

func expectOfferingForClass(m *MockRDSClient, class string) {
	m.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{
			ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{
				{
					ReservedDBInstancesOfferingId: aws.String("offering-1"),
					DBInstanceClass:               aws.String(class),
					ProductDescription:            aws.String("mysql"),
					MultiAZ:                       aws.Bool(false),
					OfferingType:                  aws.String("All Upfront"),
					Duration:                      aws.Int32(31536000),
				},
			},
		}, nil)
}

// TestClient_PurchaseCommitment_Idempotent_GuardShortCircuits asserts that when a
// reservation already exists under the token-derived ID, a re-drive returns it
// WITHOUT calling PurchaseReservedDBInstancesOffering a second time (issue #641).
func TestClient_PurchaseCommitment_Idempotent_GuardShortCircuits(t *testing.T) {
	mockRDS := &MockRDSClient{}
	client := &Client{client: mockRDS, region: "eu-west-1"}

	token := common.DeriveIdempotencyToken("exec-1", 0)
	derivedID := common.IdempotentReservationID("rds-id-", token)

	expectOffering(mockRDS)
	// The by-ID guard finds an existing active reservation under the derived ID.
	mockRDS.On("DescribeReservedDBInstances", mock.Anything, mock.MatchedBy(func(in *rds.DescribeReservedDBInstancesInput) bool {
		return aws.ToString(in.ReservedDBInstanceId) == derivedID
	})).Return(&rds.DescribeReservedDBInstancesOutput{
		ReservedDBInstances: []types.ReservedDBInstance{
			{ReservedDBInstanceId: aws.String(derivedID), State: aws.String("active")},
		},
	}, nil)

	result, err := client.PurchaseCommitment(context.Background(), idempotencyTestRec(), common.PurchaseOptions{IdempotencyToken: token})

	assert.NoError(t, err)
	assert.True(t, result.Success)
	assert.True(t, result.ExistingCommitment)
	assert.Equal(t, derivedID, result.CommitmentID)
	mockRDS.AssertNotCalled(t, "PurchaseReservedDBInstancesOffering", mock.Anything, mock.Anything)
}

// TestClient_PurchaseCommitment_Idempotent_NotFoundProceeds asserts a first-time
// purchase proceeds: the by-ID guard reports not-found (NotFound fault), the
// purchase runs, and the derived ID is used.
func TestClient_PurchaseCommitment_Idempotent_NotFoundProceeds(t *testing.T) {
	mockRDS := &MockRDSClient{}
	client := &Client{client: mockRDS, region: "eu-west-1"}

	token := common.DeriveIdempotencyToken("exec-2", 0)
	derivedID := common.IdempotentReservationID("rds-id-", token)

	expectOffering(mockRDS)
	mockRDS.On("DescribeReservedDBInstances", mock.Anything, mock.Anything).
		Return((*rds.DescribeReservedDBInstancesOutput)(nil), &types.ReservedDBInstanceNotFoundFault{})
	mockRDS.On("PurchaseReservedDBInstancesOffering", mock.Anything, mock.MatchedBy(func(in *rds.PurchaseReservedDBInstancesOfferingInput) bool {
		return aws.ToString(in.ReservedDBInstanceId) == derivedID
	})).Return(&rds.PurchaseReservedDBInstancesOfferingOutput{
		ReservedDBInstance: &types.ReservedDBInstance{ReservedDBInstanceId: aws.String(derivedID)},
	}, nil)

	result, err := client.PurchaseCommitment(context.Background(), idempotencyTestRec(), common.PurchaseOptions{IdempotencyToken: token})

	assert.NoError(t, err)
	assert.True(t, result.Success)
	assert.False(t, result.ExistingCommitment)
	assert.Equal(t, derivedID, result.CommitmentID)
	mockRDS.AssertExpectations(t)
}

// TestClient_PurchaseCommitment_Idempotent_AlreadyExistsRecovers asserts that if
// the guard missed but AWS rejects the duplicate ID with the AlreadyExists fault,
// the re-drive recovers the existing reservation instead of erroring.
func TestClient_PurchaseCommitment_Idempotent_AlreadyExistsRecovers(t *testing.T) {
	mockRDS := &MockRDSClient{}
	client := &Client{client: mockRDS, region: "eu-west-1"}

	token := common.DeriveIdempotencyToken("exec-3", 0)
	derivedID := common.IdempotentReservationID("rds-id-", token)

	expectOffering(mockRDS)
	// First Describe (guard): not found. Second Describe (recovery): found.
	mockRDS.On("DescribeReservedDBInstances", mock.Anything, mock.Anything).
		Return((*rds.DescribeReservedDBInstancesOutput)(nil), &types.ReservedDBInstanceNotFoundFault{}).Once()
	mockRDS.On("PurchaseReservedDBInstancesOffering", mock.Anything, mock.Anything).
		Return((*rds.PurchaseReservedDBInstancesOfferingOutput)(nil), &types.ReservedDBInstanceAlreadyExistsFault{})
	mockRDS.On("DescribeReservedDBInstances", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOutput{
			ReservedDBInstances: []types.ReservedDBInstance{
				{ReservedDBInstanceId: aws.String(derivedID), State: aws.String("active")},
			},
		}, nil).Once()

	result, err := client.PurchaseCommitment(context.Background(), idempotencyTestRec(), common.PurchaseOptions{IdempotencyToken: token})

	assert.NoError(t, err)
	assert.True(t, result.Success)
	assert.True(t, result.ExistingCommitment)
	assert.Equal(t, derivedID, result.CommitmentID)
}

// TestClient_PurchaseCommitment_Idempotent_FailLoudOnLookupError asserts a lookup
// error fails loud and does NOT fall through to a purchase (no double-buy).
func TestClient_PurchaseCommitment_Idempotent_FailLoudOnLookupError(t *testing.T) {
	mockRDS := &MockRDSClient{}
	client := &Client{client: mockRDS, region: "eu-west-1"}

	token := common.DeriveIdempotencyToken("exec-4", 0)

	expectOffering(mockRDS)
	mockRDS.On("DescribeReservedDBInstances", mock.Anything, mock.Anything).
		Return((*rds.DescribeReservedDBInstancesOutput)(nil), fmt.Errorf("access denied"))

	result, err := client.PurchaseCommitment(context.Background(), idempotencyTestRec(), common.PurchaseOptions{IdempotencyToken: token})

	assert.Error(t, err)
	assert.False(t, result.Success)
	assert.False(t, result.ExistingCommitment)
	assert.Contains(t, err.Error(), "refusing to purchase")
	mockRDS.AssertNotCalled(t, "PurchaseReservedDBInstancesOffering", mock.Anything, mock.Anything)
}

// TestFindOfferingID_PaginationCapFires asserts that findOfferingID returns a
// "pagination cap reached" error after maxOfferingPages empty pages and does NOT
// make a (maxOfferingPages+1)th call (issue #688).
func TestFindOfferingID_PaginationCapFires(t *testing.T) {
	mockRDS := &MockRDSClient{}
	t.Cleanup(func() { mockRDS.AssertExpectations(t) })
	client := &Client{client: mockRDS, region: "us-east-1"}

	rec := idempotencyTestRec()
	for i := range maxOfferingPages {
		mockRDS.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
			Return(&rds.DescribeReservedDBInstancesOfferingsOutput{
				ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{},
				Marker:                       aws.String(fmt.Sprintf("tok-%d", i+1)),
			}, nil).Once()
	}

	_, err := client.findOfferingID(context.Background(), rec, "")

	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "pagination cap reached")
	}
	mockRDS.AssertNumberOfCalls(t, "DescribeReservedDBInstancesOfferings", maxOfferingPages)
}

// TestFindOfferingID_WrongVariantRejected asserts that findOfferingID never
// returns an offering whose OfferingType does not match the requested payment
// option (issues #688, #288).
func TestFindOfferingID_WrongVariantRejected(t *testing.T) {
	mockRDS := &MockRDSClient{}
	t.Cleanup(func() { mockRDS.AssertExpectations(t) })
	client := &Client{client: mockRDS, region: "us-east-1"}

	rec := idempotencyTestRec() // requests "all-upfront"

	mockRDS.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{
			ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{
				{
					ReservedDBInstancesOfferingId: aws.String("wrong-offering"),
					DBInstanceClass:               aws.String("db.r6g.large"),
					OfferingType:                  aws.String("No Upfront"), // mismatch
					Duration:                      aws.Int32(31536000),
				},
			},
		}, nil).Once()

	_, err := client.findOfferingID(context.Background(), rec, "")

	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "no offerings found")
	}
}

// TestFindOfferingID_HappyPath asserts that findOfferingID returns the correct
// offering ID when a matching offering is returned on the first page (issue #688).
func TestFindOfferingID_HappyPath(t *testing.T) {
	mockRDS := &MockRDSClient{}
	t.Cleanup(func() { mockRDS.AssertExpectations(t) })
	client := &Client{client: mockRDS, region: "us-east-1"}

	rec := idempotencyTestRec() // requests "all-upfront", "1yr", "db.r6g.large"

	mockRDS.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{
			ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{
				{
					ReservedDBInstancesOfferingId: aws.String("offering-ok"),
					DBInstanceClass:               aws.String("db.r6g.large"),
					ProductDescription:            aws.String("mysql"),
					MultiAZ:                       aws.Bool(false),
					OfferingType:                  aws.String("All Upfront"),
					Duration:                      aws.Int32(31536000),
				},
			},
		}, nil).Once()

	id, err := client.findOfferingID(context.Background(), rec, "")

	assert.NoError(t, err)
	assert.Equal(t, "offering-ok", id)
}

// TestFindOfferingID_EmptyAZConfig_Errors is the M4 regression test:
// findOfferingID must error when AZConfig is empty rather than silently
// assuming single-az. An empty AZConfig means the CE recommendation did not
// include DeploymentOption; single-AZ and multi-AZ RDS RIs have different
// prices so fabricating the value risks buying the wrong offering class.
func TestFindOfferingID_EmptyAZConfig_Errors(t *testing.T) {
	mockRDS := &MockRDSClient{}
	t.Cleanup(func() { mockRDS.AssertExpectations(t) })
	client := &Client{client: mockRDS, region: "us-east-1"}

	rec := common.Recommendation{
		Service:       common.ServiceRelationalDB,
		ResourceType:  "db.r5.large",
		PaymentOption: "all-upfront",
		Term:          "1yr",
		Details: &common.DatabaseDetails{
			Engine:   "mysql",
			AZConfig: "", // not set -- CE omitted DeploymentOption
		},
	}

	// No mock calls should be made: findOfferingID must fail before the API call.
	_, err := client.findOfferingID(context.Background(), rec, "")

	require.Error(t, err, "findOfferingID must error when AZConfig is empty (M4)")
	assert.Contains(t, err.Error(), "AZConfig")
}

// TestFindOfferingID_InvalidAZConfig_Errors is the CR #1085 regression test:
// findOfferingID must reject a non-empty but invalid AZConfig value rather than
// silently falling through to multiAZ==false in paginateRDSOfferings (which
// would treat the bad value as single-AZ, the same mis-buy as the old default).
func TestFindOfferingID_InvalidAZConfig_Errors(t *testing.T) {
	mockRDS := &MockRDSClient{}
	t.Cleanup(func() { mockRDS.AssertExpectations(t) })
	client := &Client{client: mockRDS, region: "us-east-1"}

	rec := common.Recommendation{
		Service:       common.ServiceRelationalDB,
		ResourceType:  "db.r5.large",
		Region:        "us-east-1",
		PaymentOption: "all-upfront",
		Term:          "1yr",
		Details: &common.DatabaseDetails{
			Engine:   "mysql",
			AZConfig: "typo-az", // non-empty but not a valid enum value
		},
	}

	// No mock calls should be made: findOfferingID must fail before the API call.
	_, err := client.findOfferingID(context.Background(), rec, "")

	require.Error(t, err, "findOfferingID must error on invalid non-empty AZConfig (CR #1085)")
	assert.Contains(t, err.Error(), "AZConfig")
	assert.Contains(t, err.Error(), "typo-az")
}

// TestFindOfferingID_InvalidTerm_ErrorsBeforeAPICall is the ARCH-04 (issue
// #1192) call-path regression test: an unrecognized or empty term must abort
// the offering lookup before any DescribeReservedDBInstancesOfferings call,
// rather than silently matching (and buying) a 1-year offering. A "0" term is
// what a 0/NULL Term DB row produces on the scheduler purchase path.
func TestFindOfferingID_InvalidTerm_ErrorsBeforeAPICall(t *testing.T) {
	mockRDS := &MockRDSClient{}
	t.Cleanup(func() { mockRDS.AssertExpectations(t) })
	client := &Client{client: mockRDS, region: "us-east-1"}

	rec := common.Recommendation{
		Service:       common.ServiceRelationalDB,
		ResourceType:  "db.r5.large",
		PaymentOption: "all-upfront",
		Term:          "0",
		Details: &common.DatabaseDetails{
			Engine:   "mysql",
			AZConfig: "single-az",
		},
	}

	_, err := client.findOfferingID(context.Background(), rec, "")

	require.Error(t, err, "findOfferingID must error on an unrecognized term (ARCH-04)")
	assert.Contains(t, err.Error(), "unsupported RDS reservation term")
	mockRDS.AssertNotCalled(t, "DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything)
}

// TestNormalizeEngineName_AmbiguousErrors is the M6 regression test:
// normalizeEngineName must error for bare Oracle/SQL Server/Aurora inputs.
// CE returns "Oracle" (title-case) for any Oracle engine when no edition is
// specified; the normalizer rejects the bare name so the caller surfaces the
// problem rather than silently guessing an edition.
// Edition-qualified strings (oracle-se2, sqlserver-web, etc.) are valid RDS
// ProductDescription values and must pass through (see CR #1085).
func TestNormalizeEngineName_AmbiguousErrors(t *testing.T) {
	client := &Client{}

	ambiguous := []string{
		"oracle",
		"Oracle",
		"sqlserver",
		"SQLServer",
		"sql-server",
		"Aurora",
		"aurora",
		"aurora-unknown",
	}

	for _, engine := range ambiguous {
		t.Run(engine, func(t *testing.T) {
			_, err := client.normalizeEngineName(engine)
			assert.Error(t, err, "engine %q must error (M6 regression guard)", engine)
		})
	}
}

// TestNormalizeEngineName_EditionTokensPassThrough is the CR #1085 regression
// test: edition-qualified Oracle and SQL Server tokens must pass through
// normalizeEngineName rather than being rejected by the bare-name ambiguity
// check. These are valid RDS ProductDescription values that CE can supply.
func TestNormalizeEngineName_EditionTokensPassThrough(t *testing.T) {
	client := &Client{}

	cases := []struct {
		input    string
		expected string
	}{
		{"oracle-se2", "oracle-se2"},
		{"oracle-ee", "oracle-ee"},
		{"Oracle-EE", "oracle-ee"},
		{"oracle-se2-ex", "oracle-se2-ex"},
		{"sqlserver-se", "sqlserver-se"},
		{"sqlserver-ee", "sqlserver-ee"},
		{"sqlserver-web", "sqlserver-web"},
		{"sqlserver-ex", "sqlserver-ex"},
		{"sql-server-ex", "sql-server-ex"},
		{"sql-server-se", "sql-server-se"},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			result, err := client.normalizeEngineName(tc.input)
			assert.NoError(t, err, "edition token %q must pass through (not ambiguous)", tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestFindOfferingID_CtxCancelledBeforePage asserts that findOfferingID returns
// context.Canceled immediately when the context is already canceled before the
// first pagination iteration, without calling the AWS API (issue #515).
func TestFindOfferingID_CtxCancelledBeforePage(t *testing.T) {
	mockRDS := &MockRDSClient{}
	t.Cleanup(func() { mockRDS.AssertExpectations(t) })
	client := &Client{client: mockRDS, region: "us-east-1"}

	rec := idempotencyTestRec()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the first iteration

	// The mock must not be called: ctx.Err() fires at the top of the loop.
	_, err := client.findOfferingID(ctx, rec, "")

	assert.ErrorIs(t, err, context.Canceled)
	mockRDS.AssertNumberOfCalls(t, "DescribeReservedDBInstancesOfferings", 0)
}

// TestFindOfferingID_EmptyStringTokenEndsPagination asserts that a page whose
// Marker is a pointer to an empty string (rather than nil) is treated as the
// terminal page and does not cause an extra API call (issue #515).
func TestFindOfferingID_EmptyStringTokenEndsPagination(t *testing.T) {
	mockRDS := &MockRDSClient{}
	t.Cleanup(func() { mockRDS.AssertExpectations(t) })
	client := &Client{client: mockRDS, region: "us-east-1"}

	rec := idempotencyTestRec()

	// Single page with zero results and Marker = ""; must not loop again.
	mockRDS.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{
			ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{},
			Marker:                       aws.String(""),
		}, nil).Once()

	_, err := client.findOfferingID(context.Background(), rec, "")

	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "no offerings found")
	}
	mockRDS.AssertNumberOfCalls(t, "DescribeReservedDBInstancesOfferings", 1)
}

// TestClient_PurchaseCommitment_NoToken_RichReservationName asserts the
// no-token CLI path (issue #687) composes a self-describing
// ReservedDBInstanceId carrying the service code, region, SKU, count, and
// term. The token-based path is exercised by the Idempotent_* tests above.
func TestClient_PurchaseCommitment_NoToken_RichReservationName(t *testing.T) {
	mockRDS := &MockRDSClient{}
	client := &Client{client: mockRDS, region: "eu-west-1"}

	rec := common.Recommendation{
		Service:       common.ServiceRelationalDB,
		ResourceType:  "db.t4g.medium",
		Region:        "eu-west-1",
		Count:         1,
		PaymentOption: "all-upfront",
		Term:          "1yr",
		Details: &common.DatabaseDetails{
			Engine:   "mysql",
			AZConfig: "single-az",
		},
	}

	expectOfferingForClass(mockRDS, "db.t4g.medium")
	var capturedID string
	mockRDS.On("PurchaseReservedDBInstancesOffering", mock.Anything, mock.MatchedBy(func(in *rds.PurchaseReservedDBInstancesOfferingInput) bool {
		capturedID = aws.ToString(in.ReservedDBInstanceId)
		return true
	})).Return(&rds.PurchaseReservedDBInstancesOfferingOutput{
		ReservedDBInstance: &types.ReservedDBInstance{ReservedDBInstanceId: aws.String("ri-x")},
	}, nil)

	_, err := client.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})
	assert.NoError(t, err)
	assert.True(t, strings.HasPrefix(capturedID, "rds-"), "name must lead with rds- service code: %q", capturedID)
	assert.Contains(t, capturedID, "eu-west-1", "region must be embedded: %q", capturedID)
	assert.Contains(t, capturedID, "db-t4g-medium", "SKU (dots->hyphens) must be embedded: %q", capturedID)
	assert.Contains(t, capturedID, "1x-1yr", "count and term must be embedded: %q", capturedID)
	assert.LessOrEqual(t, len(capturedID), 60, "must fit AWS reservation-ID cap")
}

// Issue #211: a duplicate-ID rejection whose recovery lookup finds nothing is a
// failure, not an adoption.
func TestClient_PurchaseCommitment_Idempotent_RejectedNotFlagged(t *testing.T) {
	mockRDS := &MockRDSClient{}
	client := &Client{client: mockRDS, region: "eu-west-1"}
	token := common.DeriveIdempotencyToken("exec-211", 0)

	expectOffering(mockRDS)
	mockRDS.On("DescribeReservedDBInstances", mock.Anything, mock.Anything).
		Return((*rds.DescribeReservedDBInstancesOutput)(nil), &types.ReservedDBInstanceNotFoundFault{})
	mockRDS.On("PurchaseReservedDBInstancesOffering", mock.Anything, mock.Anything).
		Return((*rds.PurchaseReservedDBInstancesOfferingOutput)(nil), &types.ReservedDBInstanceAlreadyExistsFault{})

	result, err := client.PurchaseCommitment(context.Background(), idempotencyTestRec(), common.PurchaseOptions{IdempotencyToken: token})
	assert.Error(t, err)
	assert.False(t, result.Success)
	assert.False(t, result.ExistingCommitment)
}

func exactOffering(id string) types.ReservedDBInstancesOffering {
	return types.ReservedDBInstancesOffering{
		ReservedDBInstancesOfferingId: aws.String(id),
		DBInstanceClass:               aws.String("db.r6g.large"),
		ProductDescription:            aws.String("mysql"),
		MultiAZ:                       aws.Bool(false),
		OfferingType:                  aws.String("All Upfront"),
		Duration:                      aws.Int32(31536000),
	}
}

func findWithPage(t *testing.T, offerings ...types.ReservedDBInstancesOffering) (string, error) {
	t.Helper()
	mockRDS := &MockRDSClient{}
	t.Cleanup(func() { mockRDS.AssertExpectations(t) })
	client := &Client{client: mockRDS, region: "us-east-1"}
	mockRDS.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{ReservedDBInstancesOfferings: offerings}, nil).Once()
	return client.findOfferingID(context.Background(), idempotencyTestRec(), "")
}

// TestFindOfferingID_PartialEngineMatchSkipped is the #288 regression: the
// server-side ProductDescription filter is a partial match, so an aurora-mysql
// row listed first must not be bought for a mysql recommendation.
func TestFindOfferingID_PartialEngineMatchSkipped(t *testing.T) {
	aurora := exactOffering("offering-aurora")
	aurora.ProductDescription = aws.String("aurora-mysql")

	id, err := findWithPage(t, aurora, exactOffering("offering-mysql"))

	assert.NoError(t, err)
	assert.Equal(t, "offering-mysql", id)
}

func TestFindOfferingID_OnlyNonExactRowsIsNoMatch(t *testing.T) {
	mut := map[string]func(*types.ReservedDBInstancesOffering){
		"engine":      func(o *types.ReservedDBInstancesOffering) { o.ProductDescription = aws.String("aurora-mysql") },
		"class":       func(o *types.ReservedDBInstancesOffering) { o.DBInstanceClass = aws.String("db.r6g.xlarge") },
		"multiaz":     func(o *types.ReservedDBInstancesOffering) { o.MultiAZ = aws.Bool(true) },
		"term":        func(o *types.ReservedDBInstancesOffering) { o.Duration = aws.Int32(94608000) },
		"payment":     func(o *types.ReservedDBInstancesOffering) { o.OfferingType = aws.String("No Upfront") },
		"nil-engine":  func(o *types.ReservedDBInstancesOffering) { o.ProductDescription = nil },
		"nil-multiaz": func(o *types.ReservedDBInstancesOffering) { o.MultiAZ = nil },
	}
	for name, m := range mut {
		t.Run(name, func(t *testing.T) {
			o := exactOffering("offering-x")
			m(&o)

			id, err := findWithPage(t, o)

			assert.Empty(t, id)
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), "no offerings found")
			}
		})
	}
}

func TestFindOfferingID_TwoDistinctExactMatchesIsError(t *testing.T) {
	id, err := findWithPage(t, exactOffering("offering-a"), exactOffering("offering-b"))

	assert.Empty(t, id)
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "ambiguous")
	}
}

func TestFindOfferingID_RepeatedSameIDIsNotAmbiguous(t *testing.T) {
	id, err := findWithPage(t, exactOffering("offering-a"), exactOffering("offering-a"))

	assert.NoError(t, err)
	assert.Equal(t, "offering-a", id)
}

func findAcrossPages(t *testing.T, pages ...[]types.ReservedDBInstancesOffering) (string, error) {
	t.Helper()
	mockRDS := &MockRDSClient{}
	t.Cleanup(func() { mockRDS.AssertExpectations(t) })
	client := &Client{client: mockRDS, region: "us-east-1"}
	for i, p := range pages {
		out := &rds.DescribeReservedDBInstancesOfferingsOutput{ReservedDBInstancesOfferings: p}
		if i < len(pages)-1 {
			out.Marker = aws.String(fmt.Sprintf("tok-%d", i+1))
		}
		mockRDS.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).Return(out, nil).Once()
	}
	return client.findOfferingID(context.Background(), idempotencyTestRec(), "")
}

func TestFindOfferingID_ExactMatchesOnDifferentPagesIsError(t *testing.T) {
	id, err := findAcrossPages(t,
		[]types.ReservedDBInstancesOffering{exactOffering("offering-a")},
		[]types.ReservedDBInstancesOffering{exactOffering("offering-b")})

	assert.Empty(t, id)
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "ambiguous")
	}
}

func TestFindOfferingID_SameIDOnDifferentPagesIsNotAmbiguous(t *testing.T) {
	id, err := findAcrossPages(t,
		[]types.ReservedDBInstancesOffering{exactOffering("offering-a")},
		[]types.ReservedDBInstancesOffering{exactOffering("offering-a")})

	assert.NoError(t, err)
	assert.Equal(t, "offering-a", id)
}

func TestFindOfferingID_MatchOnLaterPageAfterNonExactFirstPage(t *testing.T) {
	aurora := exactOffering("offering-aurora")
	aurora.ProductDescription = aws.String("aurora-mysql")

	id, err := findAcrossPages(t,
		[]types.ReservedDBInstancesOffering{aurora},
		[]types.ReservedDBInstancesOffering{exactOffering("offering-mysql")})

	assert.NoError(t, err)
	assert.Equal(t, "offering-mysql", id)
}

func TestFindOfferingID_NoMatchReportsPagesFetched(t *testing.T) {
	_, err := findAcrossPages(t, nil, nil)

	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "after 2 page(s)")
	}
}

// TestFindOfferingID_LicensedEnginesRefusedBeforeAPICall: Oracle and SQL Server
// offerings are listed as "oracle-se2(li)" / "oracle-se2(byol)", so the license
// model must be known; the lookup refuses without calling the API.
func TestFindOfferingID_LicensedEnginesRefusedBeforeAPICall(t *testing.T) {
	for _, engine := range []string{"oracle-se2", "oracle-ee", "sqlserver-se", "sqlserver-web", "sql-server-ex"} {
		t.Run(engine, func(t *testing.T) {
			mockRDS := &MockRDSClient{}
			t.Cleanup(func() { mockRDS.AssertExpectations(t) })
			client := &Client{client: mockRDS, region: "us-east-1"}
			rec := idempotencyTestRec()
			rec.Details = &common.DatabaseDetails{Engine: engine, AZConfig: "single-az"}

			id, err := client.findOfferingID(context.Background(), rec, "")

			assert.Empty(t, id)
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), "license model")
			}
			mockRDS.AssertNotCalled(t, "DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything)
		})
	}
}
