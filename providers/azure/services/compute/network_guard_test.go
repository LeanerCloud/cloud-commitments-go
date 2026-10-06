package compute

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

	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/mocks"
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

// TestComputeClient_ConvertAzureVMRecommendation_MakesNoExternalRequests pins
// that conversion with a stubbed resource SKUs pager never reaches the network.
func TestComputeClient_ConvertAzureVMRecommendation_MakesNoExternalRequests(t *testing.T) {
	before := externalRequests.Load()
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetResourceSKUsPager(&mocks.MockResourceSKUsPager{})

	rec := mocks.BuildLegacyReservationRecommendation(mocks.WithRegion("eastus"), mocks.WithCosts(100, 70, 30))
	require.NotNil(t, client.convertAzureVMRecommendation(context.Background(), rec))
	assert.Zero(t, externalRequests.Load()-before, "conversion must not reach the network")
}
