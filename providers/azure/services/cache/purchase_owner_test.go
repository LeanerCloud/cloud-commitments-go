package cache

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/mocks"
)

type unusedCred struct{ t *testing.T }

func (c unusedCred) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	c.t.Error("the refusal must not use the credential")
	return azcore.AccessToken{}, errors.New("credential used")
}

// Only managedredis buys RedisCache reservations (#538), so the same Redis
// capacity cannot be bought once through each service.
func TestCacheClient_PurchaseCommitment_RefusesRedisPurchases(t *testing.T) {
	t.Parallel()
	mockHTTP := &mocks.MockHTTPClient{}
	client := NewClientWithHTTP(unusedCred{t}, "test-subscription", "eastus", mockHTTP)
	rec := common.Recommendation{Service: common.ServiceCache, ResourceType: "Premium_P1", Term: "1yr", Count: 1, PaymentOption: "no-upfront"}

	result, err := client.PurchaseCommitment(context.Background(), rec,
		common.PurchaseOptions{Source: common.PurchaseSourceCLI, IdempotencyToken: "token-1"})

	require.Error(t, err)
	assert.True(t, errors.Is(err, common.ErrRefusedBeforePurchase), "refusal must say nothing was sent: %v", err)
	assert.Contains(t, err.Error(), string(common.ServiceMemoryDB))
	assert.False(t, result.Success)
	assert.Empty(t, result.CommitmentID)
	assert.Equal(t, err, result.Error)
	assert.Equal(t, rec, result.Recommendation)
	mockHTTP.AssertNotCalled(t, "Do", mock.Anything)
}
