package gcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/recommender/apiv1/recommenderpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// performanceSDK records every ListRecommendations parent and every regions
// HTTP call. Each PerformanceRecommender parent answers with an ACTIVE
// cost-saving recommendation (the shape that was relabeled as a 1yr CUD in
// issue #294); the Compute commitment recommender answers empty.
type performanceSDK struct {
	mu        sync.Mutex
	parents   []string
	httpCalls int
}

func (s *performanceSDK) snapshot() (parents []string, httpCalls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.parents...), s.httpCalls
}

func performanceSDKAdapter(t *testing.T) (*RecommendationsClientAdapter, *performanceSDK) {
	t.Helper()
	calls := &performanceSDK{}
	regions := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.mu.Lock()
		calls.httpCalls++
		calls.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"items":[{"name":"us-central1","status":"UP"},{"name":"europe-west1","status":"UP"}]}`)
	}))
	t.Cleanup(regions.Close)
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	recommenderpb.RegisterRecommenderServer(server, &recommendationSDKServer{
		list: func(_ context.Context, req *recommenderpb.ListRecommendationsRequest) (*recommenderpb.ListRecommendationsResponse, error) {
			calls.mu.Lock()
			calls.parents = append(calls.parents, req.GetParent())
			calls.mu.Unlock()
			if strings.HasSuffix(req.GetParent(), "PerformanceRecommender") {
				return &recommenderpb.ListRecommendationsResponse{Recommendations: []*recommenderpb.Recommendation{
					sdkCostRecommendation(100, recommenderpb.RecommendationStateInfo_ACTIVE),
				}}, nil
			}
			return &recommenderpb.ListRecommendationsResponse{}, nil
		},
	})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	adapter, err := NewProviderWithProject(context.Background(), "recommendation-project",
		option.WithoutAuthentication(),
		option.WithEndpoint(regions.URL),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		option.WithGRPCDialOption(grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		})),
	).GetRecommendationsClient(context.Background())
	require.NoError(t, err)
	return adapter.(*RecommendationsClientAdapter), calls
}

// TestGetAllRecommendations_NoPerformanceRecommenderCUDs is the issue #294
// regression test: the default collection must not query the Cloud SQL or
// Memorystore PerformanceRecommender, and must not emit their per-instance
// performance advice as Cloud SQL or Memorystore CUD recommendations.
func TestGetAllRecommendations_NoPerformanceRecommenderCUDs(t *testing.T) {
	adapter, calls := performanceSDKAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	recs, err := adapter.GetAllRecommendations(ctx)

	require.NoError(t, err)
	for _, rec := range recs {
		assert.Equal(t, common.ServiceCompute, rec.Service, "only Compute Engine CUDs have a supported recommender source: %+v", rec)
	}
	assert.Empty(t, recs, "the Compute commitment recommender answered empty")
	parents, _ := calls.snapshot()
	assert.ElementsMatch(t, []string{
		"projects/recommendation-project/locations/europe-west1/recommenders/google.compute.commitment.UsageCommitmentRecommender",
		"projects/recommendation-project/locations/us-central1/recommenders/google.compute.commitment.UsageCommitmentRecommender",
	}, parents, "only the Compute commitment recommender may be queried")
}

// TestGetRecommendations_ExplicitSQLAndCacheAreNotSupported pins that an
// explicit Cloud SQL or Memorystore request fails loud with the not-supported
// sentinel before any region lookup or Recommender call, instead of returning
// PerformanceRecommender output as CUDs (issue #294).
func TestGetRecommendations_ExplicitSQLAndCacheAreNotSupported(t *testing.T) {
	for _, service := range []common.ServiceType{common.ServiceRelationalDB, common.ServiceCache} {
		t.Run(string(service), func(t *testing.T) {
			adapter, calls := performanceSDKAdapter(t)

			recs, err := adapter.GetRecommendationsForService(context.Background(), service)

			require.Error(t, err)
			assert.ErrorIs(t, err, common.ErrCommitmentPurchaseNotSupported)
			assert.Nil(t, recs)
			parents, httpCalls := calls.snapshot()
			assert.Empty(t, parents, "no Recommender call may be made")
			assert.Zero(t, httpCalls, "no region lookup may be made")
		})
	}
}
