package synapse

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
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/internal/reservations/reservationstest"
)

// Issue #705: a Synapse reservation CUDly (or anyone) already holds must
// reduce the matching recommendation. Fixture dates are UTC calendar dates,
// because the Reservations API returns date-only values.
var (
	fixtureToday = time.Now().UTC().Truncate(24 * time.Hour)
	fixturePast  = fixtureToday.AddDate(0, 0, -30)
)

const synapseSKU = "DW1000c"

func testPricer() *recommendations.Pricer {
	return recommendations.NewPricer("synapse", func(context.Context, string, string, int) (recommendations.ReservationPrice, error) {
		return recommendations.ReservationPrice{Total: 1000, Currency: "USD"}, nil
	})
}

// upfrontRec runs the real converter and variant expansion for a count-2
// P1Y recommendation and returns its upfront variant.
func upfrontRec(t *testing.T, c *Client) common.Recommendation {
	t.Helper()
	raw := mocks.BuildLegacyReservationRecommendation(
		mocks.WithRegion("eastus"), mocks.WithTerm("P1Y"), mocks.WithQuantity(2),
		mocks.WithNormalizedSize(synapseSKU), mocks.WithCosts(1000, 700, 300))
	recs, err := c.appendRegionRecommendations(context.Background(), testPricer(), nil,
		[]armconsumption.ReservationRecommendationClassification{raw})
	require.NoError(t, err)
	require.Len(t, recs, 2)
	upfront := recs[0]
	require.Equal(t, common.PaymentOption("upfront"), upfront.PaymentOption)
	require.Equal(t, 2, upfront.Count)
	// The dedupe key reads engine and deployment from the details; data-warehouse
	// recommendations carry neither, so held commitments must carry neither.
	require.Empty(t, common.EngineFromDetails(upfront.Details))
	require.Empty(t, common.DeploymentFromDetails(upfront.Details))
	return upfront
}

// purchasedRow buys rec through a capturing transport and builds the held
// reservation from the request body CUDly actually sent. region is the
// recommending client's, so the location assertion can fail.
func purchasedRow(t *testing.T, region string, rec common.Recommendation, qty int32) *armreservations.ReservationResponse {
	t.Helper()
	capture := &reservationstest.Capture{}
	buyer := NewClientWithHTTP(&fake.TokenCredential{}, "sub", region, capture)
	_, err := buyer.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{Source: common.PurchaseSourceCLI})
	require.NoError(t, err)
	var body struct {
		Location string `json:"location"`
		SKU      struct {
			Name string `json:"name"`
		} `json:"sku"`
		Properties struct {
			ReservedResourceType string `json:"reservedResourceType"`
			Quantity             int32  `json:"quantity"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(capture.BodyBytes, &body))
	require.Equal(t, rec.Region, body.Location)
	require.Equal(t, rec.Count, int(body.Properties.Quantity))
	return reservationstest.LiveRow(armreservations.ReservedResourceType(body.Properties.ReservedResourceType),
		"sub", "held", body.SKU.Name, body.Location, qty)
}

func adjust(t *testing.T, c *Client, recs ...common.Recommendation) ([]common.Recommendation, error) {
	t.Helper()
	passed, _, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(context.Background(), recs, c)
	return passed, err
}

func TestHeldReservation_PartialHoldReducesRecommendation(t *testing.T) {
	t.Parallel()
	c := NewClient(nil, "sub", "eastus")
	upfront := upfrontRec(t, c)
	c.SetInventoryFactories(factoriesRows(nil, purchasedRow(t, c.region, upfront, 1)))

	passed, err := adjust(t, c, upfront)
	require.NoError(t, err)
	require.Len(t, passed, 1)
	assert.Equal(t, 1, passed[0].Count)
	assert.InDelta(t, upfront.CommitmentCost/2, passed[0].CommitmentCost, 0.001, "money fields scale with the count")
}

func TestHeldReservation_FullHoldFiltersRecommendation(t *testing.T) {
	t.Parallel()
	c := NewClient(nil, "sub", "eastus")
	upfront := upfrontRec(t, c)
	c.SetInventoryFactories(factoriesRows(nil, purchasedRow(t, c.region, upfront, 2)))

	passed, err := adjust(t, c, upfront)
	require.NoError(t, err)
	assert.Empty(t, passed)
}

func TestHeldReservation_Table(t *testing.T) {
	t.Parallel()
	state := func(s armreservations.ProvisioningState) func(*armreservations.ReservationResponse) {
		return func(r *armreservations.ReservationResponse) { r.Properties.ProvisioningState = &s }
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
		{"creating", state(armreservations.ProvisioningStateCreating), 1, ""},
		{"shared, billed to another subscription", func(r *armreservations.ReservationResponse) {
			r.Properties.BillingScopeID = mocks.StringPtr("/subscriptions/other")
		}, 2, ""},
		{"canceled", state(armreservations.ProvisioningStateCancelled), 2, ""},
		{"expired state", state(armreservations.ProvisioningStateExpired), 2, ""},
		{"succeeded past expiry", expiry(fixturePast), 2, ""},
		{"other region", func(r *armreservations.ReservationResponse) { r.Location = mocks.StringPtr("westus") }, 2, ""},
		{"other sku", func(r *armreservations.ReservationResponse) { r.SKU.Name = mocks.StringPtr("DW2000c") }, 2, ""},
		{"missing quantity", func(r *armreservations.ReservationResponse) { r.Properties.Quantity = nil }, 0, "quantity"},
		{"expires today", expiry(fixtureToday), 0, "expiryDate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := NewClient(nil, "sub", "eastus")
			upfront := upfrontRec(t, c)
			row := purchasedRow(t, c.region, upfront, 1)
			tc.mutate(row)
			c.SetInventoryFactories(factoriesRows(nil, row))
			passed, err := adjust(t, c, upfront)
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
