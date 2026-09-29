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
		name    string
		empty   bool
		failure map[string]bool
	}{
		{name: "regional recommendations and pagination"},
		{name: "empty success", empty: true},
		{name: "all regions denied", failure: map[string]bool{central: true, west: true}},
		{name: "partial regional success", failure: map[string]bool{west: true}},
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
						return &recommenderpb.ListRecommendationsResponse{Recommendations: []*recommenderpb.Recommendation{sdkCostRecommendation(30, recommenderpb.RecommendationStateInfo_ACTIVE)}}, nil
					}
					switch req.GetPageToken() {
					case "":
						return &recommenderpb.ListRecommendationsResponse{
							Recommendations: []*recommenderpb.Recommendation{
								sdkCostRecommendation(10, recommenderpb.RecommendationStateInfo_ACTIVE),
								sdkCostRecommendation(999, recommenderpb.RecommendationStateInfo_DISMISSED),
							},
							NextPageToken: "second-page",
						}, nil
					case "second-page":
						return &recommenderpb.ListRecommendationsResponse{Recommendations: []*recommenderpb.Recommendation{sdkCostRecommendation(20, recommenderpb.RecommendationStateInfo_ACTIVE)}}, nil
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
			if len(tc.failure) == 2 {
				require.Error(t, err)
				assert.Equal(t, codes.PermissionDenied, status.Code(err))
				assert.Contains(t, err.Error(), "all 2 GCP recommendation service calls failed across 2 regions")
				assert.Nil(t, recs)
			} else {
				require.NoError(t, err)
				var savings []float64
				for _, rec := range recs {
					assert.Equal(t, common.ProviderGCP, rec.Provider)
					assert.Equal(t, common.ServiceCompute, rec.Service)
					assert.Equal(t, "recommendation-project", rec.Account)
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
			if !tc.empty && !tc.failure[central] {
				expectedRequests = append(expectedRequests, central+"|second-page")
			}
			assert.ElementsMatch(t, expectedRequests, requests)
		})
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
