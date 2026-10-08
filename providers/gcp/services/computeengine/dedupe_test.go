package computeengine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
)

func TestCUDStoredTypePublicPath(t *testing.T) {
	for _, kind := range []string{"", "MEMORY_OPTIMIZED_M4_6TB", "MEMORY_OPTIMIZED_X4_480_6T", "UNDEFINED_TYPE", "TYPE_UNSPECIFIED", "UNKNOWN", "ACCELERATOR_OPTIMIZED", "GRAPHICS_OPTIMIZED", "STORAGE_OPTIMIZED_Z3"} {
		for _, decoded := range []bool{false, true} {
			want := kind
			if want == "" {
				want = "GENERAL_PURPOSE_N2"
			}
			valid := kind == "" || kind == "MEMORY_OPTIMIZED_M4_6TB" || kind == "MEMORY_OPTIMIZED_X4_480_6T"
			for _, sameBucket := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/decoded=%t/same=%t", kind, decoded, sameBucket), func(t *testing.T) {
					ctx := context.Background()
					rec := cudRecommendation()
					rec.Details = common.ComputeDetails{MemoryGB: 256, GCPCommitmentType: kind}
					if decoded {
						raw, err := common.MarshalServiceDetails(rec.Details)
						require.NoError(t, err)
						rec.Details, err = common.DecodeServiceDetailsFor(string(common.ServiceCompute), raw)
						require.NoError(t, err)
					}
					client, err := NewClient(ctx, rec.Account, rec.Region)
					require.NoError(t, err)
					cud := recentCUD()
					cud.Type = stringPtr("MEMORY_OPTIMIZED_X4_960_12T")
					if sameBucket && valid {
						cud.Type = stringPtr(want)
					}
					service := &MockCommitmentsService{operation: &MockOperation{}, commitments: []*computepb.Commitment{cud}}
					client.SetCommitmentsService(service)
					passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(ctx, []common.Recommendation{rec}, client)
					if !valid {
						require.Error(t, err)
						_, err = client.PurchaseCommitment(ctx, rec, common.PurchaseOptions{})
						require.Error(t, err)
						assert.Empty(t, service.insertReqs)
						return
					}
					require.NoError(t, err)
					if sameBucket {
						assert.Len(t, filtered, 1)
						assert.Empty(t, passed)
						assert.Empty(t, service.insertReqs)
						return
					}
					require.Len(t, passed, 1)
					_, err = client.PurchaseCommitment(ctx, passed[0], common.PurchaseOptions{})
					require.NoError(t, err)
					require.Len(t, service.insertReqs, 1)
					request := service.insertReqs[0]
					assert.Equal(t, want, request.CommitmentResource.GetType())
					assert.Equal(t, rec.Account, request.GetProject())
					assert.Equal(t, rec.Region, request.GetRegion())
					assert.Equal(t, int64(32), request.CommitmentResource.Resources[0].GetAmount())
					assert.Equal(t, int64(262144), request.CommitmentResource.Resources[1].GetAmount())
				})
			}
		}
	}
}

func TestCUDTypeFallbackWithAbsentDetails(t *testing.T) {
	for _, details := range []common.ServiceDetails{nil, (*common.ComputeDetails)(nil)} {
		rec := cudRecommendation()
		rec.Details = details
		client, err := NewClient(context.Background(), rec.Account, rec.Region)
		require.NoError(t, err)
		service := &MockCommitmentsService{operation: &MockOperation{}, commitments: []*computepb.Commitment{recentCUD()}}
		client.SetCommitmentsService(service)
		passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(context.Background(), []common.Recommendation{rec}, client)
		require.NoError(t, err)
		assert.Empty(t, passed)
		assert.Len(t, filtered, 1)
		_, err = client.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})
		require.ErrorContains(t, err, "MEMORY resource amount absent")
		assert.Empty(t, service.insertReqs)
	}
}

func recentCUD() *computepb.Commitment {
	return &computepb.Commitment{
		Name: stringPtr("recent-cud"), Type: stringPtr("GENERAL_PURPOSE_N2"),
		Status: stringPtr("ACTIVE"), StartTimestamp: stringPtr(time.Now().Add(-time.Hour).Format(time.RFC3339)),
		Resources: []*computepb.ResourceCommitment{
			{Type: stringPtr("MEMORY"), Amount: int64Ptr(65536)},
			{Type: stringPtr("VCPU"), Amount: int64Ptr(16)},
		},
	}
}

func cudRecommendation() common.Recommendation {
	return common.Recommendation{
		Provider: common.ProviderGCP, Account: "test-project", Service: common.ServiceCompute,
		Region: "us-central1", ResourceType: "n2-highmem-32", Count: 32,
		CommitmentType: common.CommitmentCUD, Term: "1yr", PaymentOption: "monthly",
		Details: &common.ComputeDetails{MemoryGB: 256}, Timestamp: time.Now(),
	}
}

func TestCUDDuplicatePurchasePath(t *testing.T) {
	for _, tc := range []struct {
		name          string
		change        func(*computepb.Commitment, *common.Recommendation)
		wantPurchases int
	}{
		{"partial same family refreshed advice", func(*computepb.Commitment, *common.Recommendation) {}, 0},
		{"exact retry", func(c *computepb.Commitment, r *common.Recommendation) {
			r.Count = 16
			r.Details = common.ComputeDetails{MemoryGB: 64}
		}, 0},
		{"different family", func(c *computepb.Commitment, r *common.Recommendation) { r.ResourceType = "c3-standard-32" }, 1},
		{"queued future", func(c *computepb.Commitment, r *common.Recommendation) {
			c.Status = stringPtr("NOT_YET_ACTIVE")
			c.StartTimestamp = stringPtr(time.Now().Add(time.Hour).Format(time.RFC3339))
		}, 0},
		{"creating", func(c *computepb.Commitment, r *common.Recommendation) { c.Status = stringPtr("CREATING") }, 0},
		{"unknown state", func(c *computepb.Commitment, r *common.Recommendation) { c.Status = stringPtr("FUTURE_STATE") }, 0},
		{"expired", func(c *computepb.Commitment, r *common.Recommendation) { c.Status = stringPtr("EXPIRED") }, 1},
		{"canceled", func(c *computepb.Commitment, r *common.Recommendation) {
			c.Status = stringPtr(computepb.Commitment_CANCELLED.String()) //nolint:misspell // SDK identifier
		}, 1},
		{"stale advice outside window", func(c *computepb.Commitment, r *common.Recommendation) {
			c.StartTimestamp = stringPtr(time.Now().Add(-48 * time.Hour).Format(time.RFC3339))
			r.Timestamp = time.Now().Add(-72 * time.Hour)
		}, 1},
		{"legacy custom N1", func(c *computepb.Commitment, r *common.Recommendation) {
			c.Type = stringPtr("GENERAL_PURPOSE")
			r.ResourceType = "custom-32-262144"
		}, 0},
		{"M1 M2 shared pool", func(c *computepb.Commitment, r *common.Recommendation) {
			c.Type = stringPtr("MEMORY_OPTIMIZED")
			r.ResourceType = "m2-ultramem-208"
		}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cud, rec := recentCUD(), cudRecommendation()
			tc.change(cud, &rec)
			client, err := NewClient(ctx, rec.Account, rec.Region)
			require.NoError(t, err)
			svc := &MockCommitmentsService{commitments: []*computepb.Commitment{cud}, operation: &MockOperation{}}
			client.SetCommitmentsService(svc)
			passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(ctx, []common.Recommendation{rec, rec}, client)
			require.NoError(t, err)
			for _, purchase := range passed {
				_, err := client.PurchaseCommitment(ctx, purchase, common.PurchaseOptions{})
				require.NoError(t, err)
			}
			assert.Len(t, svc.insertReqs, 2*tc.wantPurchases)
			if tc.wantPurchases == 0 {
				assert.Equal(t, []common.Recommendation{rec, rec}, filtered)
			} else {
				assert.Equal(t, []common.Recommendation{rec, rec}, passed)
				assert.Equal(t, int64(rec.Count), svc.lastInsertReq.CommitmentResource.Resources[0].GetAmount())
				mem, err := memoryMBFromDetails(rec)
				require.NoError(t, err)
				assert.Equal(t, mem, svc.lastInsertReq.CommitmentResource.Resources[1].GetAmount())
			}
		})
	}
}

func TestCUDInventoryRejectsUnusableMetadata(t *testing.T) {
	for name, mutate := range map[string]func(*computepb.Commitment){
		"missing name":       func(c *computepb.Commitment) { c.Name = nil },
		"missing start":      func(c *computepb.Commitment) { c.StartTimestamp = nil },
		"invalid start":      func(c *computepb.Commitment) { c.StartTimestamp = stringPtr("yesterday") },
		"unknown family":     func(c *computepb.Commitment) { c.Type = stringPtr("FUTURE_FAMILY") },
		"undefined family":   func(c *computepb.Commitment) { c.Type = stringPtr("UNDEFINED_TYPE") },
		"unknown resource":   func(c *computepb.Commitment) { c.Resources[0].Type = stringPtr("FUTURE_RESOURCE") },
		"undefined resource": func(c *computepb.Commitment) { c.Resources[0].Type = stringPtr("UNDEFINED_TYPE") },
		"nil resource type":  func(c *computepb.Commitment) { c.Resources[0].Type = nil },
		"nil memory amount":  func(c *computepb.Commitment) { c.Resources[0].Amount = nil },
		"nil vcpu amount":    func(c *computepb.Commitment) { c.Resources[1].Amount = nil },
		"negative amount":    func(c *computepb.Commitment) { c.Resources[1].Amount = int64Ptr(-1) },
		"zero amount":        func(c *computepb.Commitment) { c.Resources[1].Amount = int64Ptr(0) },
		"missing vcpu":       func(c *computepb.Commitment) { c.Resources = c.Resources[:1] },
		"nil resource":       func(c *computepb.Commitment) { c.Resources[0] = nil },
		"duplicate vcpu":     func(c *computepb.Commitment) { c.Resources = append(c.Resources, c.Resources[1]) },
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			cud := recentCUD()
			mutate(cud)
			client, err := NewClient(ctx, "test-project", "us-central1")
			require.NoError(t, err)
			svc := &MockCommitmentsService{commitments: []*computepb.Commitment{recentCUD(), cud}}
			client.SetCommitmentsService(svc)
			got, err := client.GetExistingCommitments(ctx)
			require.Error(t, err)
			assert.Nil(t, got)
			_, _, err = recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(ctx, []common.Recommendation{cudRecommendation()}, client)
			require.Error(t, err)
			assert.Empty(t, svc.insertReqs)
		})
	}
}

func TestCUDDuplicateIdentity(t *testing.T) {
	for name, change := range map[string]func(*common.Recommendation){
		"another project":        func(r *common.Recommendation) { r.Account = "other-project" },
		"another region":         func(r *common.Recommendation) { r.Region = "europe-west1" },
		"missing project":        func(r *common.Recommendation) { r.Account = "" },
		"missing region":         func(r *common.Recommendation) { r.Region = "" },
		"wrong provider":         func(r *common.Recommendation) { r.Provider = common.ProviderAWS },
		"wrong service":          func(r *common.Recommendation) { r.Service = common.ServiceEC2 },
		"wrong commitment kind":  func(r *common.Recommendation) { r.CommitmentType = common.CommitmentReservedInstance },
		"unknown machine family": func(r *common.Recommendation) { r.ResourceType = "future-standard-32" },
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			client, err := NewClient(ctx, "test-project", "us-central1")
			require.NoError(t, err)
			client.SetCommitmentsService(&MockCommitmentsService{commitments: []*computepb.Commitment{recentCUD()}})
			rec := cudRecommendation()
			change(&rec)
			passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(ctx, []common.Recommendation{rec}, client)
			if name == "another project" || name == "another region" {
				require.NoError(t, err)
				assert.Equal(t, []common.Recommendation{rec}, passed)
				assert.Empty(t, filtered)
			} else {
				require.Error(t, err)
				assert.Empty(t, passed)
			}
		})
	}
}

func TestCUDNilInventoryRecord(t *testing.T) {
	client, err := NewClient(context.Background(), "test-project", "us-central1")
	require.NoError(t, err)
	client.SetCommitmentsService(&MockCommitmentsService{commitments: []*computepb.Commitment{nil}})
	got, err := client.GetExistingCommitments(context.Background())
	require.Error(t, err)
	assert.Nil(t, got)
}
