package gcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/recommender/apiv1/recommenderpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func TestComputeRecommendationsSDKPageBudget(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		pages, items, dismissed, wantCalls int
		repeat, cancel, denied, cap        bool
	}{
		{name: "exactly20items", pages: 1, items: 20, wantCalls: 1},
		{name: "moreThan20items", pages: 1, items: 21, wantCalls: 1},
		{name: "dismissedDoNotConsumeBudget", pages: 1, items: 2, dismissed: 25, wantCalls: 1},
		{name: "exactly20pages", pages: 20, items: 2, wantCalls: 20},
		{name: "over20pages", pages: 21, items: 2, wantCalls: 20, cap: true},
		{name: "empty20pages", pages: 20, wantCalls: 20},
		{name: "emptyOver20pages", pages: 21, wantCalls: 20, cap: true},
		{name: "repeatedEmptyToken", pages: 21, repeat: true, wantCalls: 20, cap: true},
		{name: "cancelSecondPage", pages: 2, items: 2, cancel: true, wantCalls: 2},
		{name: "deniedSecondPage", pages: 2, items: 2, denied: true, wantCalls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			regions := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/compute/v1/projects/recommendation-project/regions", r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"items":[{"name":"us-central1","status":"UP"}]}`)
			}))
			defer regions.Close()
			listener := bufconn.Listen(1024 * 1024)
			defer listener.Close()
			server := grpc.NewServer()
			var calls atomic.Int32
			recommenderpb.RegisterRecommenderServer(server, &recommendationSDKServer{list: func(_ context.Context, req *recommenderpb.ListRecommendationsRequest) (*recommenderpb.ListRecommendationsResponse, error) {
				page := int(calls.Add(1))
				assert.Equal(t, "projects/recommendation-project/locations/us-central1/recommenders/google.compute.commitment.UsageCommitmentRecommender", req.GetParent())
				expectedToken := ""
				if page > 1 {
					expectedToken = strconv.Itoa(page)
				}
				if page > 1 && tc.repeat {
					expectedToken = "repeat"
				}
				assert.Equal(t, expectedToken, req.GetPageToken())
				if page == 2 && tc.cancel {
					cancel()
					return nil, status.Error(codes.Canceled, "canceled during second page")
				}
				if page == 2 && tc.denied {
					return nil, status.Error(codes.PermissionDenied, "second page denied")
				}
				response := &recommenderpb.ListRecommendationsResponse{}
				for i := 0; i < tc.items+tc.dismissed; i++ {
					state := recommenderpb.RecommendationStateInfo_ACTIVE
					if i < tc.dismissed {
						state = recommenderpb.RecommendationStateInfo_DISMISSED
					}
					rec := sdkCostRecommendation(10, state)
					rec.Name = fmt.Sprintf("page-%d-item-%d", page, i)
					rec.Content = sdkResourceAmounts([]string{"VCPU", "MEMORY"}, true, "us-central1")
					response.Recommendations = append(response.Recommendations, rec)
				}
				if page < tc.pages {
					response.NextPageToken = strconv.Itoa(page + 1)
				}
				if tc.repeat {
					response.NextPageToken = "repeat"
				}
				// Bound a broken implementation without hiding its extra fetches.
				if page > 21 {
					return nil, status.Error(codes.InvalidArgument, "test page safety bound")
				}
				return response, nil
			}})
			serveDone := make(chan error, 1)
			go func() { serveDone <- server.Serve(listener) }()
			defer func() { server.Stop(); assert.NoError(t, <-serveDone) }()
			provider := NewProviderWithProject(ctx, "recommendation-project", option.WithoutAuthentication(), option.WithEndpoint(regions.URL),
				option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
				option.WithGRPCDialOption(grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) })))
			client, err := provider.GetRecommendationsClient(ctx)
			require.NoError(t, err)
			recs, err := client.GetRecommendationsForService(ctx, common.ServiceCompute)
			assert.Equal(t, tc.wantCalls, int(calls.Load()))
			switch {
			case tc.cap:
				require.ErrorContains(t, err, "page cap (20 pages)")
				assert.Nil(t, recs)
			case tc.cancel:
				require.Error(t, err)
				assert.True(t, errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled, "cancellation cause: %v", err)
				assert.Nil(t, recs)
			case tc.denied:
				require.Error(t, err)
				assert.Equal(t, codes.PermissionDenied, status.Code(err))
				assert.Nil(t, recs)
			default:
				require.NoError(t, err)
				require.Len(t, recs, tc.pages*tc.items)
				for _, rec := range recs {
					assert.Equal(t, 4, rec.Count)
					assert.Equal(t, common.ComputeDetails{MemoryGB: 6}, rec.Details)
				}
			}
		})
	}
}
