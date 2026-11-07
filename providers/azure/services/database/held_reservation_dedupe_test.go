package database

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/fake"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/sql/armsql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/recommendations"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/mocks"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/internal/reservations/reservationstest"
)

// Issue #705: a SQL Database reservation CUDly (or anyone) already holds must
// reduce the matching recommendation. Fixture dates are UTC calendar dates,
// because the Reservations API returns date-only values.
var (
	fixtureToday = time.Now().UTC().Truncate(24 * time.Hour)
	fixturePast  = fixtureToday.AddDate(0, 0, -30)
)

const sqlSKU = "GP_Gen5_2"

func testPricer() *recommendations.Pricer {
	return recommendations.NewPricer("database", func(context.Context, string, string, int) (recommendations.ReservationPrice, error) {
		return recommendations.ReservationPrice{Total: 1000, Currency: "USD"}, nil
	})
}

// fleetClient returns a client whose subscription has the given managed
// instances (one ZoneRedundant flag each); the same client serves the
// recommendation and the held-reservation lookup, as in production.
func fleetClient(zoneRedundant ...bool) *Client {
	c := NewClient(nil, "sub", "eastus")
	c.SetCapabilitiesClient(&MockCapabilitiesClient{})
	mi := &MockSQLManagedInstancesPager{}
	if len(zoneRedundant) > 0 {
		mi.pages = []armsql.ManagedInstancesClientListResponse{buildMIPage(zoneRedundant...)}
	}
	c.SetManagedInstancesPager(mi)
	c.SetServersPager(&MockSQLServersPager{})
	return c
}

// upfrontRec runs the real converter (including the AZConfig enrichment) and
// variant expansion for a count-2 P1Y recommendation and returns its upfront
// variant.
func upfrontRec(t *testing.T, c *Client) common.Recommendation {
	t.Helper()
	raw := mocks.BuildLegacyReservationRecommendation(
		mocks.WithRegion("eastus"), mocks.WithTerm("P1Y"), mocks.WithQuantity(2),
		mocks.WithNormalizedSize(sqlSKU), mocks.WithCosts(1000, 700, 300))
	recs, err := c.appendPage(context.Background(), testPricer(), nil,
		[]armconsumption.ReservationRecommendationClassification{raw})
	require.NoError(t, err)
	require.Len(t, recs, 2)
	upfront := recs[0]
	require.Equal(t, "upfront", upfront.PaymentOption)
	require.Equal(t, 2, upfront.Count)
	require.Equal(t, "sqlserver", common.EngineFromDetails(upfront.Details))
	return upfront
}

// purchasedRow buys rec through a capturing transport and builds the held
// reservation from the request body CUDly actually sent.
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
	c := fleetClient()
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
	c := fleetClient()
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
		{"other sku", func(r *armreservations.ReservationResponse) { r.SKU.Name = mocks.StringPtr("GP_Gen5_4") }, 2, ""},
		{"missing quantity", func(r *armreservations.ReservationResponse) { r.Properties.Quantity = nil }, 0, "quantity"},
		{"expires today", expiry(fixtureToday), 0, "expiryDate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := fleetClient()
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

// The key carries the subscription's dominant zone-redundancy, which the held
// reservation must match exactly or the guard fails open.
func TestHeldReservation_FleetShapes(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		fleet []bool
		miErr error
		az    string
	}{
		"no managed instances":        {nil, nil, ""},
		"all zone-redundant":          {[]bool{true, true}, nil, "zoneRedundant"},
		"none zone-redundant":         {[]bool{false}, nil, "none"},
		"mixed fleet":                 {[]bool{true, false}, nil, ""},
		"managed-instance walk fails": {nil, errors.New("mi list failed"), ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := fleetClient(tc.fleet...)
			var failing *MockSQLManagedInstancesPager
			if tc.miErr != nil {
				failing = &MockSQLManagedInstancesPager{err: tc.miErr, pages: []armsql.ManagedInstancesClientListResponse{buildMIPage(true)}}
				c.SetManagedInstancesPager(failing)
			}
			upfront := upfrontRec(t, c)
			require.Equal(t, tc.az, common.DeploymentFromDetails(upfront.Details), "the rec key's deployment")
			c.SetInventoryFactories(factoriesRows(nil, purchasedRow(t, c.region, upfront, 1)))

			held, err := c.GetExistingCommitments(context.Background())
			require.NoError(t, err)
			require.Len(t, held, 1)
			assert.Equal(t, tc.az, held[0].Deployment)

			passed, err := adjust(t, c, upfront)
			require.NoError(t, err)
			require.Len(t, passed, 1)
			assert.Equal(t, 1, passed[0].Count, "the hold must match the key whatever the fleet shape")
			if failing != nil {
				assert.Positive(t, failing.CallCount, "the failing walk must actually run")
			}
		})
	}
}

// A walk that fails on the recommendation call and succeeds on the inventory
// call must not give the held reservation a different deployment than the key
// the recommendation already carries.
func TestHeldReservation_UsesRecommendationsAZSnapshot(t *testing.T) {
	t.Parallel()
	c := fleetClient()
	c.SetManagedInstancesPager(&flakyMIPager{failures: 1})
	c.serverInfoRetryDelay = 10 * time.Millisecond
	upfront := upfrontRec(t, c)
	require.Empty(t, common.DeploymentFromDetails(upfront.Details), "the rec was keyed while the walk failed")
	time.Sleep(30 * time.Millisecond)
	c.SetInventoryFactories(factoriesRows(nil, purchasedRow(t, c.region, upfront, 1)))

	held, err := c.GetExistingCommitments(context.Background())
	require.NoError(t, err)
	require.Len(t, held, 1)
	assert.Empty(t, held[0].Deployment, "the held reservation must reuse the rec's snapshot")

	passed, err := adjust(t, c, upfront)
	require.NoError(t, err)
	require.Len(t, passed, 1)
	assert.Equal(t, 1, passed[0].Count)
}
