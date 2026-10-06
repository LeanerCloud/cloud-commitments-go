package database

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/sql/armsql"
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
	os.Exit(code)
}

// TestDatabaseClient_ConvertAzureSQLRecommendation_MakesNoExternalRequests
// pins that conversion with stubbed capabilities and pagers never reaches the
// network.
func TestDatabaseClient_ConvertAzureSQLRecommendation_MakesNoExternalRequests(t *testing.T) {
	skuName, versionName := "GeneralPurpose_Gen5_2", "12.0"
	tests := []struct {
		name string
		caps *MockCapabilitiesClient
	}{
		{"populated", &MockCapabilitiesClient{}},
		{"engine version", &MockCapabilitiesClient{response: armsql.CapabilitiesClientListByLocationResponse{
			LocationCapabilities: armsql.LocationCapabilities{
				SupportedServerVersions: []*armsql.ServerVersionCapability{{
					Name: &versionName,
					SupportedEditions: []*armsql.EditionCapability{{
						SupportedServiceLevelObjectives: []*armsql.ServiceObjectiveCapability{{SKU: &armsql.SKU{Name: &skuName}}},
					}},
				}},
			},
		}}},
		{"capabilities error", &MockCapabilitiesClient{err: errors.New("transient Azure API error")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := externalRequests.Load()
			client := NewClient(nil, "test-subscription", "eastus")
			client.SetCapabilitiesClient(tt.caps)
			client.SetManagedInstancesPager(&MockSQLManagedInstancesPager{})
			client.SetServersPager(&MockSQLServersPager{})

			rec := mocks.BuildLegacyReservationRecommendation(
				mocks.WithRegion("eastus"),
				mocks.WithNormalizedSize(skuName),
			)
			require.NotNil(t, client.convertAzureSQLRecommendation(context.Background(), rec))
			assert.Zero(t, externalRequests.Load()-before, "conversion must not reach the network")
		})
	}
}
