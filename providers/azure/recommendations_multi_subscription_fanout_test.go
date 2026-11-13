package azure

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
)

type delayedSubClient struct {
	peakRecommendationsClient
	delay time.Duration
	subID string
}

func (d *delayedSubClient) GetRecommendations(ctx context.Context, _ *common.RecommendationParams) ([]common.Recommendation, error) {
	select {
	case <-time.After(d.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []common.Recommendation{{Account: d.subID, Service: common.ServiceCompute}}, nil
}

// Merged output follows subscription order even when the last subscription
// finishes first.
func TestMultiSubscriptionRecommendationsClient_GetRecommendations_OrderFollowsSubscriptions(t *testing.T) {
	const subs = 4
	orig := newSubscriptionRecommendationsClientFn
	t.Cleanup(func() { newSubscriptionRecommendationsClientFn = orig })
	newSubscriptionRecommendationsClientFn = func(_ azcore.TokenCredential, id string) (provider.RecommendationsClient, error) {
		var idx int
		_, err := fmt.Sscanf(id, "sub-%d", &idx)
		require.NoError(t, err)
		return &delayedSubClient{subID: id, delay: time.Duration(subs-idx) * 20 * time.Millisecond}, nil
	}
	accounts := make([]common.Account, subs)
	for i := range accounts {
		accounts[i] = common.Account{Provider: common.ProviderAzure, ID: fmt.Sprintf("sub-%d", i)}
	}
	client, err := NewMultiSubscriptionRecommendationsClient(&mockAzureTokenCredential{}, accounts)
	require.NoError(t, err)

	recs, err := client.GetAllRecommendations(context.Background())

	require.NoError(t, err)
	require.Len(t, recs, subs)
	for i, rec := range recs {
		assert.Equal(t, fmt.Sprintf("sub-%d", i), rec.Account)
	}
}
