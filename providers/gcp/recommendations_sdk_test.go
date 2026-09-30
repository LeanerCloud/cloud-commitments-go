package gcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/recommender/apiv1/recommenderpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/genproto/googleapis/type/money"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

type recommendationSDKServer struct {
	recommenderpb.UnimplementedRecommenderServer
	list func(context.Context, *recommenderpb.ListRecommendationsRequest) (*recommenderpb.ListRecommendationsResponse, error)
}

func (s *recommendationSDKServer) ListRecommendations(ctx context.Context, req *recommenderpb.ListRecommendationsRequest) (*recommenderpb.ListRecommendationsResponse, error) {
	return s.list(ctx, req)
}

func TestComputeRecommendationsThroughSDK(t *testing.T) {
	const central = "projects/recommendation-project/locations/us-central1/recommenders/google.compute.commitment.UsageCommitmentRecommender"
	const west = "projects/recommendation-project/locations/europe-west1/recommenders/google.compute.commitment.UsageCommitmentRecommender"
	cases := []struct {
		name       string
		empty      bool
		failure    map[string]bool
		resources  []string
		stringVCPU bool
		invalid    bool
	}{
		{name: "regional recommendations and pagination"},
		{name: "empty success", empty: true},
		{name: "all regions denied", failure: map[string]bool{central: true, west: true}},
		{name: "partial regional success", failure: map[string]bool{west: true}},
		{name: "accelerator memory cpu", resources: []string{"ACCELERATOR", "MEMORY", "VCPU"}},
		{name: "accelerator cpu memory", resources: []string{"ACCELERATOR", "VCPU", "MEMORY"}},
		{name: "memory accelerator cpu", resources: []string{"MEMORY", "ACCELERATOR", "VCPU"}},
		{name: "memory cpu accelerator", resources: []string{"MEMORY", "VCPU", "ACCELERATOR"}},
		{name: "cpu accelerator memory", resources: []string{"VCPU", "ACCELERATOR", "MEMORY"}},
		{name: "cpu memory accelerator", resources: []string{"VCPU", "MEMORY", "ACCELERATOR"}},
		{name: "local SSD before cpu", resources: []string{"LOCAL_SSD", "VCPU", "MEMORY"}},
		{name: "local SSD after cpu", resources: []string{"VCPU", "MEMORY", "LOCAL_SSD"}},
		{name: "string cpu", resources: []string{"ACCELERATOR", "VCPU", "MEMORY"}, stringVCPU: true},
		{name: "lowercase memory", resources: []string{"memory", "VCPU"}},
		{name: "legacy lowercase memory", resources: []string{"memory_mb", "VCPU"}},
		{name: "unknown resource", resources: []string{"UNKNOWN", "VCPU", "MEMORY"}, invalid: true},
		{name: "unknown after cpu", resources: []string{"VCPU", "MEMORY", "UNKNOWN"}, invalid: true},
		{name: "missing cpu with overview", resources: []string{"ACCELERATOR", "MEMORY"}, invalid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var mu sync.Mutex
			var requests []string
			regionsCalls := 0
			regions := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				if r.URL.Path != "/compute/v1/projects/recommendation-project/regions" {
					t.Errorf("unexpected HTTP path: %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				mu.Lock()
				regionsCalls++
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"items":[{"name":"us-central1","status":"UP"},{"name":"europe-west1","status":"UP"}]}`)
			}))
			defer regions.Close()
			listener := bufconn.Listen(1024 * 1024)
			defer listener.Close()
			server := grpc.NewServer()
			recommendation := func(savings int64, state recommenderpb.RecommendationStateInfo_State, region string) *recommenderpb.Recommendation {
				rec := sdkCostRecommendation(savings, state)
				if tc.resources != nil {
					rec.Content = sdkResourceAmounts(tc.resources, tc.stringVCPU, region)
				}
				return rec
			}
			recommenderpb.RegisterRecommenderServer(server, &recommendationSDKServer{
				list: func(ctx context.Context, req *recommenderpb.ListRecommendationsRequest) (*recommenderpb.ListRecommendationsResponse, error) {
					mu.Lock()
					requests = append(requests, req.GetParent()+"|"+req.GetPageToken())
					mu.Unlock()
					if req.GetParent() != central && req.GetParent() != west {
						return nil, status.Errorf(codes.NotFound, "unknown recommender parent %q", req.GetParent())
					}
					md, _ := metadata.FromIncomingContext(ctx)
					assert.Contains(t, md.Get("x-goog-request-params"), "parent="+url.QueryEscape(req.GetParent()))
					if tc.failure[req.GetParent()] {
						return nil, status.Error(codes.PermissionDenied, "recommendations permission denied")
					}
					if tc.empty {
						return &recommenderpb.ListRecommendationsResponse{}, nil
					}
					if req.GetParent() == west {
						return &recommenderpb.ListRecommendationsResponse{Recommendations: []*recommenderpb.Recommendation{recommendation(30, recommenderpb.RecommendationStateInfo_ACTIVE, "europe-west1")}}, nil
					}
					switch req.GetPageToken() {
					case "":
						return &recommenderpb.ListRecommendationsResponse{
							Recommendations: []*recommenderpb.Recommendation{
								recommendation(10, recommenderpb.RecommendationStateInfo_ACTIVE, "us-central1"),
								recommendation(999, recommenderpb.RecommendationStateInfo_DISMISSED, "us-central1"),
							},
							NextPageToken: "second-page",
						}, nil
					case "second-page":
						return &recommenderpb.ListRecommendationsResponse{Recommendations: []*recommenderpb.Recommendation{recommendation(20, recommenderpb.RecommendationStateInfo_ACTIVE, "us-central1")}}, nil
					default:
						return nil, status.Error(codes.InvalidArgument, "unexpected page token")
					}
				},
			})
			serveDone := make(chan error, 1)
			go func() { serveDone <- server.Serve(listener) }()
			defer func() {
				server.Stop()
				assert.NoError(t, <-serveDone)
			}()
			options := []option.ClientOption{
				option.WithoutAuthentication(),
				option.WithEndpoint(regions.URL),
				option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
				option.WithGRPCDialOption(grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
					return listener.DialContext(ctx)
				})),
			}
			adapter, err := NewProviderWithProject(ctx, "recommendation-project", options...).GetRecommendationsClient(ctx)
			require.NoError(t, err)
			recs, err := adapter.GetRecommendationsForService(ctx, common.ServiceCompute)
			if len(tc.failure) == 2 || tc.invalid {
				require.Error(t, err)
				if !tc.invalid {
					assert.Equal(t, codes.PermissionDenied, status.Code(err))
				} else {
					assert.Contains(t, err.Error(), "VCPU")
				}
				assert.Contains(t, err.Error(), "all 2 GCP recommendation service calls failed across 2 regions")
				assert.Nil(t, recs)
			} else {
				require.NoError(t, err)
				var savings []float64
				for _, rec := range recs {
					assert.Equal(t, common.ProviderGCP, rec.Provider)
					assert.Equal(t, common.ServiceCompute, rec.Service)
					assert.Equal(t, "recommendation-project", rec.Account)
					assert.Empty(t, rec.ResourceType)
					if tc.resources != nil {
						assert.Equal(t, 4, rec.Count)
						assert.Equal(t, common.ComputeDetails{MemoryGB: 6}, rec.Details)
					} else {
						assert.Zero(t, rec.Count)
					}
					if rec.EstimatedSavings == 30 {
						assert.Equal(t, "europe-west1", rec.Region)
					} else {
						assert.Equal(t, "us-central1", rec.Region)
					}
					savings = append(savings, rec.EstimatedSavings)
				}
				switch {
				case tc.empty:
					assert.Empty(t, recs)
				case tc.failure[west]:
					assert.Equal(t, []float64{10, 20}, savings)
				default:
					assert.Equal(t, []float64{30, 10, 20}, savings)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, 1, regionsCalls)
			expectedRequests := []string{central + "|", west + "|"}
			if !tc.empty && !tc.failure[central] && !tc.invalid {
				expectedRequests = append(expectedRequests, central+"|second-page")
			}
			assert.ElementsMatch(t, expectedRequests, requests)
		})
	}
}

// Synthetic explicit-filter operations exercise the generic API contract, not a captured cloud response.
func sdkResourceAmounts(kinds []string, stringVCPU bool, region string) *recommenderpb.RecommendationContent {
	group := &recommenderpb.OperationGroup{}
	for _, kind := range kinds {
		amount := structpb.NewNumberValue(map[string]float64{"VCPU": 4, "MEMORY": 6144, "memory": 6144, "memory_mb": 6144, "ACCELERATOR": 2, "LOCAL_SSD": 375, "UNKNOWN": 99}[kind])
		if kind == "VCPU" && stringVCPU {
			amount = structpb.NewStringValue("4")
		}
		group.Operations = append(group.Operations, &recommenderpb.Operation{
			Action: "AdD", ResourceType: "compute.googleapis.com/Commitment",
			Resource: "//compute.googleapis.com/projects/recommendation-project/regions/" + region + "/commitments/cud-001",
			Path:     "/resources/*/amount", PathFilters: map[string]*structpb.Value{"/resources/*/type": structpb.NewStringValue(kind)},
			PathValue: &recommenderpb.Operation_Value{Value: amount},
		})
	}
	return &recommenderpb.RecommendationContent{
		OperationGroups: []*recommenderpb.OperationGroup{group},
		Overview:        &structpb.Struct{Fields: map[string]*structpb.Value{"numericValue": structpb.NewNumberValue(999)}},
	}
}

// A cost-only fixture verifies listing without inventing purchasable machine resources.
func sdkCostRecommendation(savings int64, state recommenderpb.RecommendationStateInfo_State) *recommenderpb.Recommendation {
	return &recommenderpb.Recommendation{
		StateInfo: &recommenderpb.RecommendationStateInfo{State: state},
		PrimaryImpact: &recommenderpb.Impact{
			Category: recommenderpb.Impact_COST,
			Projection: &recommenderpb.Impact_CostProjection{CostProjection: &recommenderpb.CostProjection{
				Cost: &money.Money{CurrencyCode: "USD", Units: -savings},
			}},
		},
	}
}
