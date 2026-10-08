package compute_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/compute"
)

type inventorySDKCredential struct{}

func (inventorySDKCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "test", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type inventorySDKTransport struct {
	child             string
	reservationStatus int
}

func (t inventorySDKTransport) Do(req *http.Request) (*http.Response, error) {
	body := `{"properties":{"reservationOrderIds":{"value":["/providers/Microsoft.Capacity/reservationOrders/order-one"]}}}`
	status := http.StatusOK
	if strings.HasSuffix(req.URL.Path, "/reservations") {
		body = `{"value":[` + t.child + `]}`
		if t.reservationStatus != 0 {
			status = t.reservationStatus
		}
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func sdkInventoryClient(t *testing.T, subscriptionID string, transport inventorySDKTransport) *compute.Client {
	t.Helper()
	opts := &arm.ClientOptions{ClientOptions: azcore.ClientOptions{
		Transport: transport,
		Retry:     policy.RetryOptions{MaxRetries: -1},
	}}
	applied, err := armreservations.NewAzureReservationAPIClient(inventorySDKCredential{}, opts)
	require.NoError(t, err)
	orders, err := armreservations.NewReservationClient(inventorySDKCredential{}, opts)
	require.NoError(t, err)
	client := compute.NewClient(inventorySDKCredential{}, subscriptionID, "eastus")
	client.SetInventoryFactories(&compute.InventoryFactories{
		NewAppliedLister: func() (compute.AppliedReservationsLister, error) { return applied, nil },
		NewOrderPager: func(orderID string) compute.OrderReservationsPager {
			return orders.NewListPager(orderID, nil)
		},
	})
	return client
}

type sdkChild struct {
	id, resourceType, scopeType, scopes, region, state string
	quantity                                           int
}

func sdkReservationChild(child sdkChild, purchaseDate, expiryDate time.Time) string {
	return fmt.Sprintf(`{"id":%q,"location":%q,"sku":{"name":"Standard_D2s_v3"},"properties":{"reservedResourceType":%q,"provisioningState":%q,"appliedScopeType":%s,"appliedScopes":%s,"quantity":%d,"purchaseDate":%q,"expiryDate":%q}}`,
		"/providers/Microsoft.Capacity/reservationOrders/order-one/reservations/"+child.id,
		child.region, child.resourceType, child.state, child.scopeType, child.scopes, child.quantity,
		purchaseDate.UTC().Format(time.DateOnly), expiryDate.UTC().Format(time.DateOnly))
}

func TestGetExistingCommitmentsSDKSharedVMFailsClosed(t *testing.T) {
	now := time.Now()
	child := sdkReservationChild(sdkChild{
		id: "child-one", resourceType: "VirtualMachines", scopeType: `"Shared"`, scopes: `null`,
		region: "eastus", state: "Succeeded", quantity: 1,
	}, now, now.AddDate(1, 0, 0))
	for _, subscriptionID := range []string{"sub-a", "sub-b"} {
		t.Run(subscriptionID, func(t *testing.T) {
			client := sdkInventoryClient(t, subscriptionID, inventorySDKTransport{child: child})
			inventory, err := client.GetExistingCommitments(context.Background())
			require.ErrorContains(t, err, "cannot attribute")
			require.Nil(t, inventory)

			recommendation := common.Recommendation{
				Provider: common.ProviderAzure, Service: common.ServiceCompute,
				Account: subscriptionID, Region: "eastus", ResourceType: "Standard_D2s_v3", Count: 1,
			}
			passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(
				context.Background(), []common.Recommendation{recommendation}, client)
			require.ErrorContains(t, err, "cannot attribute")
			require.Len(t, passed, 1)
			require.Empty(t, filtered)
		})
	}
}

func TestGetExistingCommitmentsSDKScopeBoundary(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name         string
		resourceType string
		scopeType    string
		scopes       string
		wantError    bool
		wantCount    int
	}{
		{name: "shared SQL is ignored", resourceType: "SQLDatabases", scopeType: `"Shared"`, scopes: `null`},
		{name: "missing VM scope fails", resourceType: "VirtualMachines", scopeType: `null`, scopes: `null`, wantError: true},
		{name: "unknown VM scope fails", resourceType: "VirtualMachines", scopeType: `"Mystery"`, scopes: `null`, wantError: true},
		{name: "single VM remains attributed", resourceType: "VirtualMachines", scopeType: `"Single"`, scopes: `["/subscriptions/sub-a"]`, wantCount: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{
				child: sdkReservationChild(sdkChild{
					id: "child-one", resourceType: tc.resourceType, scopeType: tc.scopeType, scopes: tc.scopes,
					region: "westus2", state: "Succeeded", quantity: 2,
				}, now, now.AddDate(1, 0, 0)),
			})
			inventory, err := client.GetExistingCommitments(context.Background())
			if tc.wantError {
				require.ErrorContains(t, err, "cannot attribute")
				require.Nil(t, inventory)
				return
			}
			require.NoError(t, err)
			require.Len(t, inventory, tc.wantCount)
			if tc.wantCount == 1 {
				row := inventory[0]
				require.Equal(t, "sub-a", row.Account)
				require.Equal(t, "westus2", row.Region)
				require.Equal(t, "Standard_D2s_v3", row.ResourceType)
				require.Equal(t, common.CommitmentStateActive, row.State)
				require.Equal(t, 2, row.Count)
				require.Equal(t, now.Format("2006-01-02"), row.StartDate.UTC().Format("2006-01-02"))
				require.Equal(t, now.AddDate(1, 0, 0).Format("2006-01-02"), row.EndDate.UTC().Format("2006-01-02"))
			}
		})
	}
}

func TestGetExistingCommitmentsSDKSplitSingleAndTerminal(t *testing.T) {
	now := time.Now()
	first := sdkReservationChild(sdkChild{
		id: "child-a", resourceType: "VirtualMachines", scopeType: `"Single"`, scopes: `["/subscriptions/sub-a"]`,
		region: "westus2", state: "Succeeded", quantity: 2,
	}, now, now.AddDate(1, 0, 0))
	other := sdkReservationChild(sdkChild{
		id: "child-b", resourceType: "VirtualMachines", scopeType: `"Single"`, scopes: `["/subscriptions/sub-b"]`,
		region: "eastus", state: "Succeeded", quantity: 3,
	}, now, now.AddDate(1, 0, 0))
	client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: first + "," + other})
	inventory, err := client.GetExistingCommitments(context.Background())
	require.NoError(t, err)
	require.Len(t, inventory, 1)
	require.Equal(t, "/providers/Microsoft.Capacity/reservationOrders/order-one/reservations/child-a", inventory[0].CommitmentID)
	require.Equal(t, 2, inventory[0].Count)
	rec := common.Recommendation{Provider: common.ProviderAzure, Service: common.ServiceCompute,
		Account: "sub-a", Region: "westus2", ResourceType: "Standard_D2s_v3", Count: 2}
	passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(
		context.Background(), []common.Recommendation{rec}, client)
	require.NoError(t, err)
	require.Empty(t, passed)
	require.Len(t, filtered, 1)

	terminal := sdkReservationChild(sdkChild{
		id: "child-c", resourceType: "VirtualMachines", scopeType: `"Single"`, scopes: `["/subscriptions/sub-a"]`,
		region: "westus2", state: string(armreservations.ProvisioningStateCancelled), quantity: 2,
	}, now, now.AddDate(1, 0, 0))
	client = sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: terminal})
	passed, filtered, err = recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(
		context.Background(), []common.Recommendation{rec}, client)
	require.NoError(t, err)
	require.Len(t, passed, 1)
	require.Empty(t, filtered)
}

func TestGetExistingCommitmentsSDKPageFailureFailsClosed(t *testing.T) {
	client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{reservationStatus: http.StatusInternalServerError})
	inventory, err := client.GetExistingCommitments(context.Background())
	require.ErrorContains(t, err, "list reservations")
	require.Nil(t, inventory)
	rec := common.Recommendation{Provider: common.ProviderAzure, Service: common.ServiceCompute,
		Account: "sub-a", Region: "eastus", ResourceType: "Standard_D2s_v3", Count: 1}
	passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(
		context.Background(), []common.Recommendation{rec}, client)
	require.ErrorContains(t, err, "list reservations")
	require.Len(t, passed, 1)
	require.Empty(t, filtered)
}

func TestGetExistingCommitmentsSDKExpiryTodayFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	child := sdkChild{
		id: "expiry-today", resourceType: "VirtualMachines", scopeType: `"Single"`, scopes: `["/subscriptions/sub-a"]`,
		region: "eastus", state: "Succeeded", quantity: 1,
	}
	ambiguous := sdkReservationChild(child, now.AddDate(-1, 0, 0), now)
	client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: ambiguous})
	inventory, err := client.GetExistingCommitments(context.Background())
	require.ErrorContains(t, err, "date-only expiryDate")
	require.ErrorContains(t, err, "expiry-today")
	require.Nil(t, inventory)
	rec := common.Recommendation{Provider: common.ProviderAzure, Service: common.ServiceCompute,
		Account: "sub-a", Region: "eastus", ResourceType: "Standard_D2s_v3", Count: 1}
	passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(
		context.Background(), []common.Recommendation{rec}, client)
	require.ErrorContains(t, err, "date-only expiryDate")
	require.Len(t, passed, 1)
	require.Empty(t, filtered)

	child.id = "valid-first"
	valid := sdkReservationChild(child, now.AddDate(-1, 0, 0), now.AddDate(0, 0, 1))
	client = sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: valid + "," + ambiguous})
	inventory, err = client.GetExistingCommitments(context.Background())
	require.ErrorContains(t, err, "date-only expiryDate")
	require.Nil(t, inventory)
}

func TestGetExistingCommitmentsSDKExpiryDateControls(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name         string
		resourceType string
		state        string
		expiry       time.Time
		nilState     bool
		nilExpiry    bool
		wantCount    int
		wantState    common.CommitmentState
	}{
		{name: "future succeeded", resourceType: "VirtualMachines", state: "Succeeded", expiry: now.AddDate(0, 0, 1), wantCount: 1, wantState: common.CommitmentStateActive},
		{name: "past succeeded", resourceType: "VirtualMachines", state: "Succeeded", expiry: now.AddDate(0, 0, -1), wantCount: 1, wantState: common.CommitmentStateExpired},
		{name: "today canceled", resourceType: "VirtualMachines", state: string(armreservations.ProvisioningStateCancelled), expiry: now, wantCount: 1, wantState: common.CommitmentStateCanceled},
		{name: "today nil state", resourceType: "VirtualMachines", state: "Succeeded", expiry: now, nilState: true, wantCount: 1},
		{name: "today unknown state", resourceType: "VirtualMachines", state: "Mystery", expiry: now, wantCount: 1},
		{name: "missing expiry succeeded", resourceType: "VirtualMachines", state: "Succeeded", expiry: now, nilExpiry: true, wantCount: 1, wantState: common.CommitmentStateActive},
		{name: "today SQL", resourceType: "SQLDatabases", state: "Succeeded", expiry: now},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			child := sdkReservationChild(sdkChild{
				id: "date-control", resourceType: tc.resourceType, scopeType: `"Single"`, scopes: `["/subscriptions/sub-a"]`,
				region: "eastus", state: tc.state, quantity: 1,
			}, now.AddDate(-1, 0, 0), tc.expiry)
			if tc.nilState {
				child = strings.Replace(child, `"provisioningState":"Succeeded"`, `"provisioningState":null`, 1)
				require.Contains(t, child, `"provisioningState":null`)
			}
			if tc.nilExpiry {
				child = strings.Replace(child, `"expiryDate":`+fmt.Sprintf("%q", tc.expiry.Format(time.DateOnly)), `"expiryDate":null`, 1)
				require.Contains(t, child, `"expiryDate":null`)
			}
			client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: child})
			inventory, err := client.GetExistingCommitments(context.Background())
			require.NoError(t, err)
			require.Len(t, inventory, tc.wantCount)
			if tc.wantCount == 1 {
				require.Equal(t, tc.wantState, inventory[0].State)
			}
		})
	}
}
