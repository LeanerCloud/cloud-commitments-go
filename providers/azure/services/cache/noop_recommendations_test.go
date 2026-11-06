package cache

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

type countingCred struct{ tokens atomic.Int32 }

func (c *countingCred) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	c.tokens.Add(1)
	return azcore.AccessToken{}, errors.New("GetRecommendations must not call Azure")
}

// Azure Redis recommendations are collected by managedredis and labeled
// ServiceMemoryDB. A ServiceCache-labeled Redis recommendation would route its
// purchase to this client, which must not buy Redis.
func TestGetRecommendations_NoOpNeverEmitsServiceCache(t *testing.T) {
	t.Parallel()
	cred := &countingCred{}

	recs, err := NewClient(cred, "sub", "eastus").GetRecommendations(context.Background(), &common.RecommendationParams{})

	require.NoError(t, err)
	assert.Equal(t, []common.Recommendation{}, recs)
	assert.Zero(t, cred.tokens.Load(), "no request is made")
}
