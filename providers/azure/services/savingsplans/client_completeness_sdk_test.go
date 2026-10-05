package savingsplans

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/billingbenefits/armbillingbenefits"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
)

type inventoryCredential struct{}

func (inventoryCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "offline-inventory-test", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

func TestGetExistingCommitments_SDKPaginationCompleteness(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		for _, dedupe := range []bool{false, true} {
			t.Run(fmt.Sprintf("ambiguous=%t/dedupe=%t", ambiguous, dedupe), func(t *testing.T) {
				var requests atomic.Int32
				var endpoint string
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet || r.URL.Path != "/providers/Microsoft.BillingBenefits/savingsPlans" || r.URL.Query().Get("api-version") != "2022-11-01" {
						t.Errorf("unexpected SDK request: %s %s", r.Method, r.URL)
						http.Error(w, "unexpected request", http.StatusBadRequest)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					requests.Add(1)
					switch r.URL.Query().Get("page") {
					case "":
						_, err := fmt.Fprintf(w, `{"value":[{"id":"owned","name":"Compute","properties":{"billingScopeId":"/subscriptions/sub-a","provisioningState":"Succeeded","effectiveDateTime":%q}}],"nextLink":%q}`,
							time.Now().UTC().Format(time.RFC3339), endpoint+"/providers/Microsoft.BillingBenefits/savingsPlans?api-version=2022-11-01&page=2")
						assert.NoError(t, err)
					case "2":
						scope := "/subscriptions/sub-b"
						if ambiguous {
							scope = "/providers/Microsoft.Billing/billingAccounts/ba-1"
						}
						_, err := fmt.Fprintf(w, `{"value":[{"id":"second","properties":{"billingScopeId":%q,"appliedScopeType":"Shared"}}]}`, scope)
						assert.NoError(t, err)
					default:
						t.Errorf("unexpected page: %s", r.URL.RawQuery)
						http.Error(w, "unexpected page", http.StatusBadRequest)
					}
				}))
				t.Cleanup(server.Close)
				endpoint = server.URL
				httpClient := server.Client()
				transport := httpClient.Transport.(*http.Transport)
				transport.Proxy = nil
				transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
					if address != server.Listener.Addr().String() {
						return nil, fmt.Errorf("blocked non-fixture address %s", address)
					}
					return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
				}
				_, blocked := transport.DialContext(context.Background(), "tcp", "management.azure.com:443")
				require.ErrorContains(t, blocked, "blocked non-fixture")
				sdk, err := armbillingbenefits.NewSavingsPlanClient(nil, inventoryCredential{}, &arm.ClientOptions{
					ClientOptions: policy.ClientOptions{
						Transport: httpClient,
						Retry:     policy.RetryOptions{MaxRetries: -1},
						Cloud: cloud.Configuration{Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
							cloud.ResourceManager: {Endpoint: endpoint, Audience: cloud.AzurePublic.Services[cloud.ResourceManager].Audience},
						}},
					},
				})
				require.NoError(t, err)
				client := NewClient(nil, "sub-a", "eastus")
				client.SetListAllPager(sdk.NewListAllPager(nil))
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if dedupe {
					recs := []common.Recommendation{{Provider: common.ProviderAzure, Service: common.ServiceSavingsPlansAll, ResourceType: "Compute", Count: 1}}
					passed, filtered, checkErr := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(ctx, recs, client)
					if ambiguous {
						require.ErrorContains(t, checkErr, "incomplete savings plan inventory")
						assert.Equal(t, recs, passed)
						assert.Empty(t, filtered)
					} else {
						require.NoError(t, checkErr)
						assert.Empty(t, passed)
						assert.Len(t, filtered, 1)
					}
				} else {
					got, listErr := client.GetExistingCommitments(ctx)
					if ambiguous {
						require.ErrorContains(t, listErr, "incomplete savings plan inventory")
						assert.Nil(t, got)
					} else {
						require.NoError(t, listErr)
						require.Len(t, got, 1)
						assert.Equal(t, "owned", got[0].CommitmentID)
					}
				}
				assert.Equal(t, int32(2), requests.Load())
			})
		}
	}
}
