package compute_test

import (
	"context"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
)

// SDK-decode fixtures (real JSON decode, real clock), not live Azure evidence.
func TestPurchaseDateGuardSDKDateOnlyPurchase(t *testing.T) {
	t.Parallel()
	built := time.Now().UTC()
	today := built.Truncate(24 * time.Hour)
	longAfter := today.AddDate(1, 0, 0)
	cases := []struct {
		name         string
		purchaseDate time.Time
		expiry       time.Time
		state        string
		wantFiltered bool
	}{
		{"purchased today", today, longAfter, "Succeeded", true},
		{"purchased yesterday", today.AddDate(0, 0, -1), longAfter, "Succeeded", true},
		{"purchased two days ago", today.AddDate(0, 0, -2), longAfter, "Succeeded", false},
		{"canceled yesterday", today.AddDate(0, 0, -1), longAfter, string(armreservations.ProvisioningStateCancelled), false},
		{"expired yesterday", today.AddDate(0, 0, -1), today.AddDate(0, 0, -3), "Succeeded", false},
	}
	rec := common.Recommendation{Provider: common.ProviderAzure, Service: common.ServiceCompute,
		Account: "sub-a", Region: "westus2", ResourceType: "Standard_D2s_v3", Count: 2}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			child := sdkReservationChild(sdkChild{
				id: "child-a", resourceType: "VirtualMachines", scopeType: `"Single"`, scopes: `["/subscriptions/sub-a"]`,
				region: "westus2", state: tc.state, quantity: 2,
			}, tc.purchaseDate, tc.expiry)
			client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: child})
			passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(
				context.Background(), []common.Recommendation{rec}, client)
			if time.Now().UTC().Truncate(24*time.Hour) != today {
				t.Skip("UTC date rolled over while the fixture ran")
			}
			require.NoError(t, err)
			if tc.wantFiltered {
				require.Empty(t, passed)
				require.Len(t, filtered, 1)
				return
			}
			require.Len(t, passed, 1)
			require.Empty(t, filtered)
		})
	}
}

func TestPurchaseDateGuardSDKPartialOverlapScalesCosts(t *testing.T) {
	t.Parallel()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	child := sdkReservationChild(sdkChild{
		id: "child-a", resourceType: "VirtualMachines", scopeType: `"Single"`, scopes: `["/subscriptions/sub-a"]`,
		region: "westus2", state: "Succeeded", quantity: 1,
	}, today, today.AddDate(1, 0, 0))
	client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: child})
	rec := common.Recommendation{Provider: common.ProviderAzure, Service: common.ServiceCompute,
		Account: "sub-a", Region: "westus2", ResourceType: "Standard_D2s_v3", Count: 3,
		CommitmentCost: 300, OnDemandCost: 600, EstimatedSavings: 150}

	passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(
		context.Background(), []common.Recommendation{rec}, client)

	require.NoError(t, err)
	require.Empty(t, filtered)
	require.Len(t, passed, 1)
	require.Equal(t, 2, passed[0].Count)
	require.InDelta(t, 200.0, passed[0].CommitmentCost, 1e-9)
	require.InDelta(t, 400.0, passed[0].OnDemandCost, 1e-9)
	require.InDelta(t, 100.0, passed[0].EstimatedSavings, 1e-9)
}
