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
	// billingScope is emitted verbatim as properties.billingScopeId when
	// non-empty (a JSON literal, e.g. `"/subscriptions/sub-a"` or `null`).
	billingScope string
	quantity     int
}

func sdkReservationChild(child sdkChild, purchaseDate, expiryDate time.Time) string {
	billing := ""
	if child.billingScope != "" {
		billing = `"billingScopeId":` + child.billingScope + `,`
	}
	return fmt.Sprintf(`{"id":%q,"location":%q,"sku":{"name":"Standard_D2s_v3"},"properties":{%s"reservedResourceType":%q,"provisioningState":%q,"appliedScopeType":%s,"appliedScopes":%s,"quantity":%d,"purchaseDate":%q,"expiryDate":%q}}`,
		"/providers/Microsoft.Capacity/reservationOrders/order-one/reservations/"+child.id,
		child.region, billing, child.resourceType, child.state, child.scopeType, child.scopes, child.quantity,
		purchaseDate.UTC().Format(time.DateOnly), expiryDate.UTC().Format(time.DateOnly))
}

// Issue #293: CUDly buys VM reservations with Shared scope and
// billingScopeId=/subscriptions/{sub}. Such a reservation is attributed to the
// subscription that bought it (so its duplicate guard keeps working) and to no
// other subscription (so one Shared quantity is never credited twice). A
// Shared reservation whose purchaser cannot be identified still fails closed.
func TestGetExistingCommitmentsSDKSharedVMAttribution(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	cases := []struct {
		name         string
		billingScope string
		subscription string
		wantErr      string
		wantCount    int
	}{
		{name: "purchaser counts it", billingScope: `"/subscriptions/sub-a"`, subscription: "sub-a", wantCount: 1},
		{name: "purchaser match ignores case", billingScope: `"/Subscriptions/SUB-A"`, subscription: "sub-a", wantCount: 1},
		{name: "other subscription skips it", billingScope: `"/subscriptions/sub-a"`, subscription: "sub-b"},
		{name: "missing billing scope fails closed", subscription: "sub-a", wantErr: "cannot attribute"},
		{name: "null billing scope fails closed", billingScope: `null`, subscription: "sub-a", wantErr: "cannot attribute"},
		{name: "non-subscription billing scope fails closed", billingScope: `"/providers/Microsoft.Billing/billingAccounts/x"`, subscription: "sub-a", wantErr: "cannot attribute"},
		{name: "resource-group billing scope fails closed", billingScope: `"/subscriptions/sub-a/resourceGroups/rg"`, subscription: "sub-a", wantErr: "cannot attribute"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			child := sdkReservationChild(sdkChild{
				id: "child-one", resourceType: "VirtualMachines", scopeType: `"Shared"`, scopes: `null`,
				billingScope: tc.billingScope, region: "eastus", state: "Succeeded", quantity: 1,
			}, now, now.AddDate(1, 0, 0))
			client := sdkInventoryClient(t, tc.subscription, inventorySDKTransport{child: child})
			inventory, err := client.GetExistingCommitments(context.Background())
			recommendation := common.Recommendation{
				Provider: common.ProviderAzure, Service: common.ServiceCompute,
				Account: tc.subscription, Region: "eastus", ResourceType: "Standard_D2s_v3", Count: 1,
			}
			passed, filtered, guardErr := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(
				context.Background(), []common.Recommendation{recommendation}, client)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.Nil(t, inventory)
				require.ErrorContains(t, guardErr, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.NoError(t, guardErr)
			require.Len(t, inventory, tc.wantCount)
			if tc.wantCount == 0 {
				require.Len(t, passed, 1)
				require.Empty(t, filtered)
				return
			}
			require.Equal(t, tc.subscription, inventory[0].Account)
			require.Equal(t, 1, inventory[0].Count)
			require.Equal(t, common.CommitmentStateActive, inventory[0].State)
			require.Empty(t, passed, "a just-bought Shared reservation must suppress the same recommendation")
			require.Len(t, filtered, 1)
		})
	}
}

func TestGetExistingCommitmentsSDKScopeBoundary(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// Issue #315: the applied-scope attribution check must only gate live
// reservations. A terminal (canceled, expired, failed, split, merged) Shared
// or scope-less VM reservation no longer counts as coverage, so it must not
// fail the whole inventory and block purchases through the duplicate guard.
func TestGetExistingCommitmentsSDKTerminalSharedVMDoesNotFail(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name      string
		state     string
		scopeType string
		expiry    time.Time
		wantState common.CommitmentState
	}{
		{name: "canceled shared", state: string(armreservations.ProvisioningStateCancelled), scopeType: `"Shared"`, expiry: now.AddDate(1, 0, 0), wantState: common.CommitmentStateCanceled},
		{name: "expired shared", state: string(armreservations.ProvisioningStateExpired), scopeType: `"Shared"`, expiry: now.AddDate(0, 0, -3), wantState: common.CommitmentStateExpired},
		{name: "failed shared", state: string(armreservations.ProvisioningStateFailed), scopeType: `"Shared"`, expiry: now.AddDate(1, 0, 0), wantState: common.CommitmentStateFailed},
		{name: "split shared", state: string(armreservations.ProvisioningStateSplit), scopeType: `"Shared"`, expiry: now.AddDate(1, 0, 0), wantState: common.CommitmentStateRetired},
		{name: "merged shared", state: string(armreservations.ProvisioningStateMerged), scopeType: `"Shared"`, expiry: now.AddDate(1, 0, 0), wantState: common.CommitmentStateRetired},
		{name: "succeeded shared past expiry", state: "Succeeded", scopeType: `"Shared"`, expiry: now.AddDate(0, 0, -3), wantState: common.CommitmentStateExpired},
		{name: "canceled missing scope type", state: string(armreservations.ProvisioningStateCancelled), scopeType: `null`, expiry: now.AddDate(1, 0, 0), wantState: common.CommitmentStateCanceled},
	}
	rec := common.Recommendation{Provider: common.ProviderAzure, Service: common.ServiceCompute,
		Account: "sub-a", Region: "eastus", ResourceType: "Standard_D2s_v3", Count: 1}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			child := sdkReservationChild(sdkChild{
				id: "terminal-shared", resourceType: "VirtualMachines", scopeType: tc.scopeType, scopes: `null`,
				region: "eastus", state: tc.state, quantity: 1,
			}, now.AddDate(-1, 0, 0), tc.expiry)
			client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: child})
			inventory, err := client.GetExistingCommitments(context.Background())
			require.NoError(t, err)
			require.Len(t, inventory, 1)
			require.Equal(t, tc.wantState, inventory[0].State)

			passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(
				context.Background(), []common.Recommendation{rec}, client)
			require.NoError(t, err)
			require.Len(t, passed, 1)
			require.Empty(t, filtered)
		})
	}
}

// A terminal Shared row next to a live Single row must not hide the live
// coverage, and a live Shared row next to a terminal one still fails closed.
func TestGetExistingCommitmentsSDKTerminalSharedMixedWithLive(t *testing.T) {
	now := time.Now().UTC()
	canceledShared := sdkReservationChild(sdkChild{
		id: "canceled-shared", resourceType: "VirtualMachines", scopeType: `"Shared"`, scopes: `null`,
		region: "eastus", state: string(armreservations.ProvisioningStateCancelled), quantity: 1,
	}, now.AddDate(-1, 0, 0), now.AddDate(1, 0, 0))
	liveSingle := sdkReservationChild(sdkChild{
		id: "live-single", resourceType: "VirtualMachines", scopeType: `"Single"`, scopes: `["/subscriptions/sub-a"]`,
		region: "eastus", state: "Succeeded", quantity: 1,
	}, now, now.AddDate(1, 0, 0))
	client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: canceledShared + "," + liveSingle})
	rec := common.Recommendation{Provider: common.ProviderAzure, Service: common.ServiceCompute,
		Account: "sub-a", Region: "eastus", ResourceType: "Standard_D2s_v3", Count: 1}
	passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(
		context.Background(), []common.Recommendation{rec}, client)
	require.NoError(t, err)
	require.Empty(t, passed)
	require.Len(t, filtered, 1)

	for _, state := range []string{"Succeeded", string(armreservations.ProvisioningStatePendingBilling)} {
		t.Run("live shared "+state, func(t *testing.T) {
			liveShared := sdkReservationChild(sdkChild{
				id: "live-shared", resourceType: "VirtualMachines", scopeType: `"Shared"`, scopes: `null`,
				region: "eastus", state: state, quantity: 1,
			}, now, now.AddDate(1, 0, 0))
			client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: canceledShared + "," + liveShared})
			inventory, err := client.GetExistingCommitments(context.Background())
			require.ErrorContains(t, err, "cannot attribute")
			require.Nil(t, inventory)
		})
	}
}

// The date-only expiry ambiguity is not a scope check: a Succeeded Shared
// reservation expiring today converts to Expired but may still be live, so
// it must keep failing closed after the terminal exemption.
func TestGetExistingCommitmentsSDKSharedExpiryTodayStillFails(t *testing.T) {
	now := time.Now().UTC()
	child := sdkReservationChild(sdkChild{
		id: "shared-expiry-today", resourceType: "VirtualMachines", scopeType: `"Shared"`, scopes: `null`,
		region: "eastus", state: "Succeeded", quantity: 1,
	}, now.AddDate(-1, 0, 0), now)
	client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: child})
	inventory, err := client.GetExistingCommitments(context.Background())
	require.ErrorContains(t, err, "date-only expiryDate")
	require.Nil(t, inventory)
}

// A Split parent is exempt from the scope check only because its children
// are listed as their own rows. A live Shared child of that split must still
// fail closed, so the exemption cannot hide unattributable live coverage.
func TestGetExistingCommitmentsSDKSplitParentLiveSharedChildStillFails(t *testing.T) {
	now := time.Now().UTC()
	splitParent := sdkReservationChild(sdkChild{
		id: "split-parent", resourceType: "VirtualMachines", scopeType: `"Shared"`, scopes: `null`,
		region: "eastus", state: string(armreservations.ProvisioningStateSplit), quantity: 2,
	}, now.AddDate(-1, 0, 0), now.AddDate(1, 0, 0))
	liveSharedChild := sdkReservationChild(sdkChild{
		id: "split-child", resourceType: "VirtualMachines", scopeType: `"Shared"`, scopes: `null`,
		region: "eastus", state: "Succeeded", quantity: 1,
	}, now, now.AddDate(1, 0, 0))
	client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: splitParent + "," + liveSharedChild})
	inventory, err := client.GetExistingCommitments(context.Background())
	require.ErrorContains(t, err, "cannot attribute")
	require.Nil(t, inventory)
}
