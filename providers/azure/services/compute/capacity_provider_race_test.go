package compute

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/mocks"
)

// TestComputeClient_EnsureCapacityProviderRegistered_CancelledCheckIsNotCached
// pins issue #745: a registration check cut short by the caller's cancellation
// must not spend the once-per-client cache. The first call fails with
// context.Canceled; the second call (live context) must issue the GET again and
// see the provider as registered. Before the fix, sync.Once marked the check
// done on the first call, so the second GET never happened.
func TestComputeClient_EnsureCapacityProviderRegistered_CancelledCheckIsNotCached(t *testing.T) {
	t.Parallel()
	mockHTTP := &mocks.MockHTTPClient{}
	client := NewClientWithHTTP(&MockTokenCredential{token: "test-token"}, "test-subscription", "eastus", mockHTTP)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	mockHTTP.On("Do", mock.MatchedBy(func(r *http.Request) bool {
		return r.Method == http.MethodGet && r.URL.Path == "/subscriptions/test-subscription/providers/Microsoft.Capacity"
	})).Return(nil, context.Canceled).Once()

	client.ensureCapacityProviderRegistered(canceled)
	require.Error(t, client.capacityProviderErr)

	mockCapacityProviderCheck(t, mockHTTP)
	client.ensureCapacityProviderRegistered(context.Background())
	require.NoError(t, client.capacityProviderErr)

	// A third call must not repeat the GET: the successful check is cached.
	client.ensureCapacityProviderRegistered(context.Background())
	mockHTTP.AssertExpectations(t)
	mockHTTP.AssertNumberOfCalls(t, "Do", 2)
}
