package managedredis

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// externalRequests counts requests that reached the loopback proxy that
// TestMain installs. The Azure SDK builds its own http.Transport, so swapping
// http.DefaultTransport would not see its calls; that transport honors
// HTTPS_PROXY, which does.
var externalRequests atomic.Int64

func TestMain(m *testing.M) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		externalRequests.Add(1)
		http.Error(w, "network access denied in tests", http.StatusForbidden)
	}))
	// Set before any request so the proxy lookup, which is cached per process, sees it.
	os.Setenv("HTTPS_PROXY", proxy.URL)
	os.Setenv("HTTP_PROXY", proxy.URL)
	os.Unsetenv("NO_PROXY")
	os.Unsetenv("no_proxy")
	code := m.Run()
	proxy.Close()
	if n := externalRequests.Load(); n > 0 && code == 0 {
		fmt.Fprintf(os.Stderr, "FAIL: %d test request(s) reached the network; stub the client or pager\n", n)
		code = 1
	}
	os.Exit(code)
}

// TestClient_SKUValidation_MakesNoExternalRequests pins that SKU
// validation with a stubbed Redis caches pager never reaches the network.
func TestClient_SKUValidation_MakesNoExternalRequests(t *testing.T) {
	before := externalRequests.Load()
	c := NewClient(nil, "test-subscription", "eastus")
	c.SetRedisCachesPager(&mockRedisPager{})

	skus, err := c.GetValidResourceTypes(context.Background())
	require.NoError(t, err)
	assert.Contains(t, skus, "Premium_P1")
	require.NoError(t, c.ValidateOffering(context.Background(), common.Recommendation{ResourceType: "Premium_P1"}))
	assert.Zero(t, externalRequests.Load()-before, "SKU validation must not reach the network")
}
