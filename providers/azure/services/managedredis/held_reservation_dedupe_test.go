package managedredis

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/fake"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/recommendations"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/mocks"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/internal/reservations"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/internal/reservations/reservationstest"
)

// Issue #699: a Redis reservation CUDly (or anyone) already holds must reduce
// the matching recommendation. Fixture dates are UTC calendar dates computed
// once, because the Reservations API returns date-only values.
var (
	fixtureToday     = time.Now().UTC().Truncate(24 * time.Hour)
	fixtureYesterday = fixtureToday.AddDate(0, 0, -1)
	fixtureThreeDays = fixtureToday.AddDate(0, 0, -3)
	fixtureFuture    = fixtureToday.AddDate(1, 0, 0)
	fixturePast      = fixtureToday.AddDate(0, 0, -30)
)

// redisRow is a complete live Redis reservation: Shared scope billed to the
// client's own subscription ("sub", the production purchase path), Succeeded,
// purchased today, expiring next year.
func redisRow(id, sku, location string, qty int32) *armreservations.ReservationResponse {
	redis := armreservations.ReservedResourceTypeRedisCache
	shared := armreservations.AppliedScopeTypeShared
	succeeded := armreservations.ProvisioningStateSucceeded
	purchased, expiry := fixtureToday, fixtureFuture
	return &armreservations.ReservationResponse{
		ID:       mocks.StringPtr(testOrderPath + "/reservations/" + id),
		Location: mocks.StringPtr(location),
		SKU:      &armreservations.SKUName{Name: mocks.StringPtr(sku)},
		Properties: &armreservations.Properties{
			ReservedResourceType: &redis,
			AppliedScopeType:     &shared,
			BillingScopeID:       mocks.StringPtr("/subscriptions/sub"),
			ProvisioningState:    &succeeded,
			Quantity:             &qty,
			PurchaseDate:         &purchased,
			ExpiryDate:           &expiry,
		},
	}
}

func testPricer(unit float64) *recommendations.Pricer {
	return recommendations.NewPricer("managedredis", func(context.Context, string, string, int) (recommendations.ReservationPrice, error) {
		return recommendations.ReservationPrice{Total: unit, Currency: "USD"}, nil
	})
}

func legacyRedisRec(term string) armconsumption.ReservationRecommendationClassification {
	return mocks.BuildLegacyReservationRecommendation(
		mocks.WithRegion("eastus"), mocks.WithSKU("Premium_P1"), mocks.WithTerm(term),
		mocks.WithQuantity(2), mocks.WithCosts(1000, 700, 300))
}

func modernRedisRec() armconsumption.ReservationRecommendationClassification {
	return mocks.BuildModernReservationRecommendation(
		mocks.WithModernRegion("eastus"), mocks.WithModernSKUName("Premium_P1"),
		mocks.WithModernQuantity(2), mocks.WithModernCosts(1000, 700, 300))
}

// variantsOf returns what GetRecommendations emits for the page: an upfront
// and a monthly variant per recommendation, in input order.
func variantsOf(t *testing.T, c *Client, page ...armconsumption.ReservationRecommendationClassification) []common.Recommendation {
	t.Helper()
	recs, err := c.appendPage(context.Background(), testPricer(1000), nil, page)
	require.NoError(t, err)
	return recs
}

func adjust(t *testing.T, c *Client, recs []common.Recommendation) (passed []common.Recommendation, err error) {
	t.Helper()
	passed, _, err = recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(context.Background(), recs, c)
	return passed, err
}

// purchasedReservation buys rec through a capturing transport and builds the
// held reservation from the request body CUDly actually sent, so the test
// proves that what CUDly buys is what the dedupe matches.
func purchasedReservation(t *testing.T, rec common.Recommendation, qty int32) *armreservations.ReservationResponse {
	t.Helper()
	capture := &reservationstest.Capture{}
	buyer := NewClientWithHTTP(&fake.TokenCredential{}, "sub", "eastus", capture)
	_, err := buyer.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{Source: common.PurchaseSourceCLI})
	require.NoError(t, err)
	var body struct {
		Location string `json:"location"`
		SKU      struct {
			Name string `json:"name"`
		} `json:"sku"`
	}
	require.NoError(t, json.Unmarshal(capture.BodyBytes, &body))
	require.Equal(t, rec.Region, body.Location, "the purchase must land in the region the recommendation is keyed on")
	return redisRow("held", body.SKU.Name, body.Location, qty)
}

func TestHeldReservation_RoundTripReducesRecommendation(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]armconsumption.ReservationRecommendationClassification{
		"legacy SKUProperties": legacyRedisRec("P1Y"),
		"modern SKUName":       modernRedisRec(),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := NewClient(nil, "sub", "eastus")
			recs := variantsOf(t, c, raw)
			require.Len(t, recs, 2)
			upfront := recs[0]
			require.Equal(t, 2, upfront.Count)
			require.Equal(t, "upfront", upfront.PaymentOption)

			c.SetInventoryFactories(factoriesRows(nil, purchasedReservation(t, upfront, 1)))
			passed, err := adjust(t, c, []common.Recommendation{upfront})
			require.NoError(t, err)
			require.Len(t, passed, 1)
			assert.Equal(t, 1, passed[0].Count)
			assert.InDelta(t, upfront.CommitmentCost/2, passed[0].CommitmentCost, 0.001, "money fields scale with the count")
		})
	}
}

func TestHeldReservation_FullyCoveredIsFiltered(t *testing.T) {
	t.Parallel()
	c := NewClient(nil, "sub", "eastus")
	upfront := variantsOf(t, c, legacyRedisRec("P1Y"))[0]
	c.SetInventoryFactories(factoriesRows(nil, purchasedReservation(t, upfront, 2)))
	passed, err := adjust(t, c, []common.Recommendation{upfront})
	require.NoError(t, err)
	assert.Empty(t, passed)
}

func TestHeldReservation_Table(t *testing.T) {
	t.Parallel()
	state := func(s armreservations.ProvisioningState) func(*armreservations.ReservationResponse) {
		return func(r *armreservations.ReservationResponse) { r.Properties.ProvisioningState = &s }
	}
	purchased := func(d time.Time) func(*armreservations.ReservationResponse) {
		return func(r *armreservations.ReservationResponse) { r.Properties.PurchaseDate = &d }
	}
	expiry := func(d time.Time) func(*armreservations.ReservationResponse) {
		return func(r *armreservations.ReservationResponse) { r.Properties.ExpiryDate = &d }
	}
	cases := []struct {
		name    string
		mutate  func(*armreservations.ReservationResponse)
		want    int
		wantErr string // non-empty: the lookup must fail with this text
	}{
		{"shared, this subscription", func(*armreservations.ReservationResponse) {}, 1, ""},
		{"single scope, this subscription", func(r *armreservations.ReservationResponse) {
			single := armreservations.AppliedScopeTypeSingle
			r.Properties.AppliedScopeType = &single
			r.Properties.AppliedScopes = []*string{mocks.StringPtr("/subscriptions/sub")}
		}, 1, ""},
		{"creating", state(armreservations.ProvisioningStateCreating), 1, ""},
		{"pending billing", state(armreservations.ProvisioningStatePendingBilling), 1, ""},
		{"purchased yesterday", purchased(fixtureYesterday), 1, ""},
		{"shared, billed to another subscription", func(r *armreservations.ReservationResponse) {
			r.Properties.BillingScopeID = mocks.StringPtr("/subscriptions/other")
		}, 2, ""},
		{"canceled", state(armreservations.ProvisioningStateCancelled), 2, ""},
		{"expired state", state(armreservations.ProvisioningStateExpired), 2, ""},
		{"succeeded past expiry", expiry(fixturePast), 2, ""},
		{"other region", func(r *armreservations.ReservationResponse) { r.Location = mocks.StringPtr("westus") }, 2, ""},
		{"other sku", func(r *armreservations.ReservationResponse) { r.SKU.Name = mocks.StringPtr("Premium_P2") }, 2, ""},
		{"purchased three days ago", purchased(fixtureThreeDays), 2, ""},
		{"missing quantity", func(r *armreservations.ReservationResponse) { r.Properties.Quantity = nil }, 0, "quantity"},
		{"expires today", expiry(fixtureToday), 0, "expiryDate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := NewClient(nil, "sub", "eastus")
			upfront := variantsOf(t, c, legacyRedisRec("P1Y"))[0]
			row := redisRow("held", "Premium_P1", "eastus", 1)
			tc.mutate(row)
			c.SetInventoryFactories(factoriesRows(nil, row))
			passed, err := adjust(t, c, []common.Recommendation{upfront})
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Len(t, passed, 1)
			assert.Equal(t, tc.want, passed[0].Count)
		})
	}
}

// TestHeldReservation_VariantPin pins today's shared-budget behavior of the
// recent-purchase guard on GetRecommendations-shaped output. The held
// quantity is consumed by the first matching variant in input order, so the P1Y
// upfront variant takes it and every other variant is untouched; the P1Y rec
// comes before the P3Y rec in the input and the pin depends on that order.
// The key has no term or payment option, so this is a known over-buy that this
// test pins and does not endorse; tracked in go#703.
func TestHeldReservation_VariantPin(t *testing.T) {
	t.Parallel()
	c := NewClient(nil, "sub", "eastus")
	recs := variantsOf(t, c, legacyRedisRec("P1Y"), legacyRedisRec("P3Y"))
	require.Len(t, recs, 4)
	c.SetInventoryFactories(factoriesRows(nil, redisRow("held", "Premium_P1", "eastus", 1)))

	passed, err := adjust(t, c, recs)
	require.NoError(t, err)
	type variant struct{ term, payment string }
	got := map[variant]int{}
	for _, r := range passed {
		got[variant{r.Term, r.PaymentOption}] = r.Count
	}
	assert.Equal(t, map[variant]int{
		{"1yr", "upfront"}: 1, {"1yr", "monthly"}: 2,
		{"3yr", "upfront"}: 2, {"3yr", "monthly"}: 2,
	}, got)
}

func TestNewClient_DefaultInventoryFactories(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]*Client{
		"NewClient":         NewClient(nil, "sub", "eastus"),
		"NewClientWithHTTP": NewClientWithHTTP(nil, "sub", "eastus", nil),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Nil(t, c.inventoryFactories, "production wiring injects nothing")
			f := c.factories()
			assert.NotNil(t, f.NewAppliedLister)
			assert.NotNil(t, f.NewOrderPager)
		})
	}
	injected := &reservations.InventoryFactories{}
	c := NewClient(nil, "sub", "eastus")
	c.SetInventoryFactories(injected)
	assert.Equal(t, *injected, c.factories())
}
