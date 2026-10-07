package reservations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
)

func strPtr(s string) *string { return &s }

func int32Ptr(i int32) *int32 { return &i }

// stubAppliedLister is a hermetic AppliedReservationsLister.
type stubAppliedLister struct {
	resp armreservations.AzureReservationAPIClientGetAppliedReservationListResponse
	err  error
}

func (s stubAppliedLister) GetAppliedReservationList(context.Context, string, *armreservations.AzureReservationAPIClientGetAppliedReservationListOptions) (armreservations.AzureReservationAPIClientGetAppliedReservationListResponse, error) {
	return s.resp, s.err
}

func appliedListResponse(orderIDs []*string, nextLink *string) armreservations.AzureReservationAPIClientGetAppliedReservationListResponse {
	return armreservations.AzureReservationAPIClientGetAppliedReservationListResponse{
		AppliedReservations: armreservations.AppliedReservations{
			Properties: &armreservations.AppliedReservationsProperties{
				ReservationOrderIDs: &armreservations.AppliedReservationList{Value: orderIDs, NextLink: nextLink},
			},
		},
	}
}

// stubOrderPager replays pages, then reports no more pages.
type stubOrderPager struct {
	pages []armreservations.ReservationClientListResponse
	err   error
	idx   int
}

func (p *stubOrderPager) More() bool { return p.err != nil || p.idx < len(p.pages) }

func (p *stubOrderPager) NextPage(context.Context) (armreservations.ReservationClientListResponse, error) {
	if p.err != nil {
		return armreservations.ReservationClientListResponse{}, p.err
	}
	page := p.pages[p.idx]
	p.idx++
	return page, nil
}

// endlessPager never terminates; used for the page-cap and cancellation tests.
type endlessPager struct{}

func (endlessPager) More() bool { return true }

func (endlessPager) NextPage(context.Context) (armreservations.ReservationClientListResponse, error) {
	return armreservations.ReservationClientListResponse{}, nil
}

func vmReservation(id string, state *armreservations.ProvisioningState) *armreservations.ReservationResponse {
	vmType := armreservations.ReservedResourceTypeVirtualMachines
	return &armreservations.ReservationResponse{
		ID:       strPtr(id),
		Location: strPtr("eastus"),
		SKU:      &armreservations.SKUName{Name: strPtr("Standard_D2s_v3")},
		Properties: &armreservations.Properties{
			ReservedResourceType: &vmType,
			ProvisioningState:    state,
			Quantity:             int32Ptr(1),
		},
	}
}

func TestCommitmentStateFromProvisioning_AllStates(t *testing.T) {
	t.Parallel()
	now := time.Now()
	future := now.Add(24 * time.Hour)
	unknown := armreservations.ProvisioningState("SomethingNew")

	cases := []struct {
		name  string
		state *armreservations.ProvisioningState
		want  common.CommitmentState
	}{
		{"nil", nil, ""},
		{"unknown", &unknown, ""},
	}
	for _, state := range armreservations.PossibleProvisioningStateValues() {
		want := map[armreservations.ProvisioningState]common.CommitmentState{
			armreservations.ProvisioningStateSucceeded:             common.CommitmentStateActive,
			armreservations.ProvisioningStateCreating:              common.CommitmentStatePaymentPending,
			armreservations.ProvisioningStateCreated:               common.CommitmentStatePaymentPending,
			armreservations.ProvisioningStatePendingBilling:        common.CommitmentStatePaymentPending,
			armreservations.ProvisioningStateConfirmedBilling:      common.CommitmentStatePaymentPending,
			armreservations.ProvisioningStatePendingResourceHold:   common.CommitmentStatePaymentPending,
			armreservations.ProvisioningStateConfirmedResourceHold: common.CommitmentStatePaymentPending,
			armreservations.ProvisioningStateCancelled:             common.CommitmentStateCanceled,
			armreservations.ProvisioningStateExpired:               common.CommitmentStateExpired,
			armreservations.ProvisioningStateFailed:                common.CommitmentStateFailed,
			armreservations.ProvisioningStateBillingFailed:         common.CommitmentStateFailed,
			armreservations.ProvisioningStateSplit:                 common.CommitmentStateRetired,
			armreservations.ProvisioningStateMerged:                common.CommitmentStateRetired,
		}[state]
		s := state
		cases = append(cases, struct {
			name  string
			state *armreservations.ProvisioningState
			want  common.CommitmentState
		}{string(state), &s, want})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commitmentStateFromProvisioning(tc.state, &future, now)
			assert.Equal(t, tc.want, got, "state %v", tc.state)
		})
	}
}

func TestCommitmentStateFromProvisioning_SucceededButExpired(t *testing.T) {
	t.Parallel()
	now := time.Now()
	succeeded := armreservations.ProvisioningStateSucceeded
	past := now.Add(-time.Hour)

	assert.Equal(t, common.CommitmentStateExpired, commitmentStateFromProvisioning(&succeeded, &past, now),
		"a Succeeded reservation past its ExpiryDate must be treated as expired")

	pastMilli := now.Add(-time.Millisecond)
	assert.Equal(t, common.CommitmentStateExpired, commitmentStateFromProvisioning(&succeeded, &pastMilli, now))
	assert.Equal(t, common.CommitmentStateActive, commitmentStateFromProvisioning(&succeeded, nil, now),
		"a nil ExpiryDate must not trigger the defensive expiry check")
}

func TestCommitmentFromReservation_NilAndTypeFilter(t *testing.T) {
	t.Parallel()
	now := time.Now()
	vmType := armreservations.ReservedResourceTypeVirtualMachines
	sqlType := armreservations.ReservedResourceTypeSQLDatabases

	assert.Nil(t, CommitmentFromReservation(nil, "sub", common.ServiceCompute, vmType, now))
	assert.Nil(t, CommitmentFromReservation(&armreservations.ReservationResponse{}, "sub", common.ServiceCompute, vmType, now))

	r := vmReservation("id", nil)
	r.Properties.ReservedResourceType = nil
	assert.Nil(t, CommitmentFromReservation(r, "sub", common.ServiceCompute, vmType, now))

	r = vmReservation("id", nil)
	r.Properties.ReservedResourceType = &sqlType
	assert.Nil(t, CommitmentFromReservation(r, "sub", common.ServiceCompute, vmType, now),
		"a non-VM reservation must be skipped by the compute converter")
}

func TestCommitmentFromReservation_StartDateUsesPurchaseDate(t *testing.T) {
	t.Parallel()
	now := time.Now()
	purchase := now.Add(-48 * time.Hour)
	effective := now.Add(-time.Hour) // revision start, moved forward by an exchange
	expiry := now.Add(365 * 24 * time.Hour)
	succeeded := armreservations.ProvisioningStateSucceeded

	r := vmReservation("id", &succeeded)
	r.Properties.PurchaseDate = &purchase
	r.Properties.EffectiveDateTime = &effective
	r.Properties.ExpiryDate = &expiry

	c := CommitmentFromReservation(r, "sub", common.ServiceCompute, armreservations.ReservedResourceTypeVirtualMachines, now)
	require.NotNil(t, c)
	assert.Equal(t, purchase, c.StartDate, "StartDate must be the purchase time, not the revision EffectiveDateTime")
	assert.Equal(t, expiry, c.EndDate)
	assert.Equal(t, 1, c.Count)
	assert.Equal(t, "sub", c.Account)
	assert.Equal(t, common.ProviderAzure, c.Provider)
	assert.Equal(t, common.CommitmentReservedInstance, c.CommitmentType)
	assert.Equal(t, common.ServiceCompute, c.Service)
	assert.Equal(t, "eastus", c.Region)
	assert.Equal(t, "Standard_D2s_v3", c.ResourceType)
	assert.Zero(t, c.Cost, "the reservations API carries no price; Cost stays zero meaning unknown")
}

func TestAppliedOrderIDs_ExtractionAndDedupe(t *testing.T) {
	t.Parallel()
	resp := appliedListResponse([]*string{
		strPtr("/providers/Microsoft.Capacity/reservationOrders/order-a"),
		strPtr("/providers/Microsoft.Capacity/reservationOrders/order-b/"), // trailing slash
		strPtr("order-a"), // duplicate of the first, already bare
		nil,
		strPtr("/"),
	}, nil)

	ids, err := appliedOrderIDs(resp)
	require.NoError(t, err)
	assert.Equal(t, []string{"order-a", "order-b"}, ids)

	ids, err = appliedOrderIDs(armreservations.AzureReservationAPIClientGetAppliedReservationListResponse{})
	require.NoError(t, err)
	assert.Nil(t, ids, "a response without properties yields no order IDs and no error")
}

func TestListAppliedReservations_TruncatedOrderListIsError(t *testing.T) {
	t.Parallel()
	factories := InventoryFactories{
		NewAppliedLister: func() (AppliedReservationsLister, error) {
			return stubAppliedLister{resp: appliedListResponse([]*string{strPtr("order-a")}, strPtr("https://next"))}, nil
		},
		NewOrderPager: func(string) OrderReservationsPager { return endlessPager{} },
	}

	_, err := ListAppliedReservations(context.Background(), "sub", factories, 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "truncated")
}

func TestListAppliedReservations_PagesAndDedupesOrders(t *testing.T) {
	t.Parallel()
	succeeded := armreservations.ProvisioningStateSucceeded
	pagedOrders := make(map[string]int)
	factories := InventoryFactories{
		NewAppliedLister: func() (AppliedReservationsLister, error) {
			return stubAppliedLister{resp: appliedListResponse([]*string{
				strPtr("/providers/Microsoft.Capacity/reservationOrders/order-a"),
				strPtr("/providers/Microsoft.Capacity/reservationOrders/order-a"),
			}, nil)}, nil
		},
		NewOrderPager: func(orderID string) OrderReservationsPager {
			pagedOrders[orderID]++
			return &stubOrderPager{pages: []armreservations.ReservationClientListResponse{
				{ReservationList: armreservations.ReservationList{Value: []*armreservations.ReservationResponse{vmReservation("r1", &succeeded)}}},
				{ReservationList: armreservations.ReservationList{Value: []*armreservations.ReservationResponse{vmReservation("r2", &succeeded)}}},
			}}
		},
	}

	reservations, err := ListAppliedReservations(context.Background(), "sub", factories, 10)
	require.NoError(t, err)
	assert.Len(t, reservations, 2)
	assert.Equal(t, map[string]int{"order-a": 1}, pagedOrders, "the deduped order must be paged exactly once")
}

func TestListAppliedReservations_PageCap(t *testing.T) {
	t.Parallel()
	factories := InventoryFactories{
		NewAppliedLister: func() (AppliedReservationsLister, error) {
			return stubAppliedLister{resp: appliedListResponse([]*string{strPtr("order-a")}, nil)}, nil
		},
		NewOrderPager: func(string) OrderReservationsPager { return endlessPager{} },
	}

	_, err := ListAppliedReservations(context.Background(), "sub", factories, 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "order-a")
	assert.Contains(t, err.Error(), "2")
}

func TestListAppliedReservations_ContextCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	factories := InventoryFactories{
		NewAppliedLister: func() (AppliedReservationsLister, error) {
			return stubAppliedLister{resp: appliedListResponse([]*string{strPtr("order-a")}, nil)}, nil
		},
		NewOrderPager: func(string) OrderReservationsPager { return endlessPager{} },
	}

	_, err := ListAppliedReservations(ctx, "sub", factories, 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(), "order-a")
}

func TestListAppliedReservations_ListerAndPageErrors(t *testing.T) {
	t.Parallel()

	_, err := ListAppliedReservations(context.Background(), "sub", InventoryFactories{
		NewAppliedLister: func() (AppliedReservationsLister, error) { return nil, errors.New("ctor") },
		NewOrderPager:    func(string) OrderReservationsPager { return endlessPager{} },
	}, 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create applied reservations lister")

	_, err = ListAppliedReservations(context.Background(), "sub", InventoryFactories{
		NewAppliedLister: func() (AppliedReservationsLister, error) {
			return stubAppliedLister{err: errors.New("api down")}, nil
		},
		NewOrderPager: func(string) OrderReservationsPager { return endlessPager{} },
	}, 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api down")

	_, err = ListAppliedReservations(context.Background(), "sub", InventoryFactories{
		NewAppliedLister: func() (AppliedReservationsLister, error) {
			return stubAppliedLister{resp: appliedListResponse([]*string{strPtr("order-a")}, nil)}, nil
		},
		NewOrderPager: func(string) OrderReservationsPager {
			return &stubOrderPager{err: errors.New("page boom")}
		},
	}, 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list reservations for order order-a")
}

// fakeServiceClient is a minimal provider.ServiceClient implementing only
// GetExistingCommitments, which is all recfilter.DuplicateChecker uses.
// Mirrors the fake in pkg/recfilter/dedupe_test.go.
type fakeServiceClient struct {
	commitments []common.Commitment
	err         error
}

func (f *fakeServiceClient) GetServiceType() common.ServiceType { return common.ServiceCompute }
func (f *fakeServiceClient) GetRegion() string                  { return "eastus" }
func (f *fakeServiceClient) GetRecommendations(context.Context, *common.RecommendationParams) ([]common.Recommendation, error) {
	return nil, nil
}
func (f *fakeServiceClient) GetExistingCommitments(context.Context) ([]common.Commitment, error) {
	return f.commitments, f.err
}
func (f *fakeServiceClient) PurchaseCommitment(context.Context, common.Recommendation, common.PurchaseOptions) (common.PurchaseResult, error) {
	return common.PurchaseResult{}, nil
}
func (f *fakeServiceClient) ValidateOffering(context.Context, common.Recommendation) error {
	return nil
}
func (f *fakeServiceClient) GetOfferingDetails(context.Context, common.Recommendation) (*common.OfferingDetails, error) {
	return nil, nil
}
func (f *fakeServiceClient) GetValidResourceTypes(context.Context) ([]string, error) { return nil, nil }

// TestReservationJSONDecodesIntoDuplicateChecker decodes a realistic raw
// reservation payload with the public SDK model, converts it through
// CommitmentFromReservation, and feeds the result into the actual coverage
// consumer (pkg/recfilter DuplicateChecker): a recent active VM reservation
// must suppress a matching recommendation, an expired one must not.
func TestReservationJSONDecodesIntoDuplicateChecker(t *testing.T) {
	t.Parallel()
	now := time.Now()
	// The reservations API returns purchaseDate and expiryDate as date-only
	// strings (the SDK decodes them with the 2006-01-02 layout); today's UTC
	// date decodes to midnight UTC, which always lands inside the 24h
	// lookback window.
	purchaseDate := now.UTC().Format("2006-01-02")
	effectiveDate := now.Add(-30 * 24 * time.Hour) // older revision start; must be ignored
	expiryDate := now.Add(365 * 24 * time.Hour).UTC().Format("2006-01-02")

	payload := func(state string) string {
		return fmt.Sprintf(`{
			"id": "/providers/Microsoft.Capacity/reservationOrders/order-1/reservations/res-1",
			"location": "eastus",
			"sku": {"name": "Standard_D2s_v3"},
			"properties": {
				"reservedResourceType": "VirtualMachines",
				"provisioningState": %q,
				"quantity": 2,
				"purchaseDate": %q,
				"effectiveDateTime": %q,
				"expiryDate": %q
			}
		}`, state, purchaseDate, effectiveDate.Format(time.RFC3339), expiryDate)
	}

	recommendation := common.Recommendation{
		Provider:     common.ProviderAzure,
		Service:      common.ServiceCompute,
		Region:       "eastus",
		ResourceType: "Standard_D2s_v3",
		Count:        2,
	}

	decode := func(t *testing.T, state string) common.Commitment {
		t.Helper()
		var r armreservations.ReservationResponse
		require.NoError(t, json.Unmarshal([]byte(payload(state)), &r))
		c := CommitmentFromReservation(&r, "sub", common.ServiceCompute, armreservations.ReservedResourceTypeVirtualMachines, now)
		require.NotNil(t, c)
		return *c
	}

	t.Run("recent active suppresses", func(t *testing.T) {
		commitment := decode(t, "Succeeded")
		assert.Equal(t, common.CommitmentStateActive, commitment.State)
		assert.Equal(t, purchaseDate, commitment.StartDate.UTC().Format("2006-01-02"),
			"the decoded commitment's StartDate must be the purchase date, not effectiveDateTime")

		passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(
			context.Background(), []common.Recommendation{recommendation}, &fakeServiceClient{commitments: []common.Commitment{commitment}})
		require.NoError(t, err)
		assert.Empty(t, passed, "a recent active VM reservation must suppress a matching recommendation")
		assert.Len(t, filtered, 1)
	})

	t.Run("expired does not suppress", func(t *testing.T) {
		commitment := decode(t, "Expired")
		assert.Equal(t, common.CommitmentStateExpired, commitment.State)

		passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(
			context.Background(), []common.Recommendation{recommendation}, &fakeServiceClient{commitments: []common.Commitment{commitment}})
		require.NoError(t, err)
		assert.Len(t, passed, 1, "an expired reservation is terminal and must not suppress a recommendation")
		assert.Empty(t, filtered)
	})
}
