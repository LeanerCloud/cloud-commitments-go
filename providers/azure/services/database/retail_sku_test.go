package database

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/mocks"
)

// liveRetailRows are the eastus SQL Database reservation rows returned by
// https://prices.azure.com/api/retail/prices for armSkuName SQLDB_GP_Compute_Gen5,
// SQLDB_GP_Compute_Gen5_ZR, SQLDB_BC_Compute_Gen5 and SQLDB_HyperScale_Compute_Gen5
// (trimmed to the fields the client reads). Retail price is the per-vCore term
// total. No SQLDB_BC/HyperScale_Compute_Gen5_ZR row exists in the live catalog.
const liveRetailRows = `
[
{"currencyCode": "USD", "retailPrice": 867.0, "unitPrice": 867.0, "armRegionName": "eastus", "productName": "SQL Database Single/Elastic Pool General Purpose - Compute Gen5", "serviceName": "SQL Database", "armSkuName": "SQLDB_GP_Compute_Gen5", "skuName": "vCore", "meterName": "vCore", "unitOfMeasure": "1 Hour", "reservationTerm": "1 Year", "type": "Reservation"},
{"currencyCode": "USD", "retailPrice": 1800.0, "unitPrice": 1800.0, "armRegionName": "eastus", "productName": "SQL Database Single/Elastic Pool General Purpose - Compute Gen5", "serviceName": "SQL Database", "armSkuName": "SQLDB_GP_Compute_Gen5", "skuName": "vCore", "meterName": "vCore", "unitOfMeasure": "1 Hour", "reservationTerm": "3 Years", "type": "Reservation"},
{"currencyCode": "USD", "retailPrice": 520.0, "unitPrice": 520.0, "armRegionName": "eastus", "productName": "SQL Database Single/Elastic Pool General Purpose - Compute Gen5", "serviceName": "SQL Database", "armSkuName": "SQLDB_GP_Compute_Gen5_ZR", "skuName": "vCore ZR Zone Redundancy", "meterName": "Zone Redundancy vCore", "unitOfMeasure": "1 Hour", "reservationTerm": "1 Year", "type": "Reservation"},
{"currencyCode": "USD", "retailPrice": 1080.0, "unitPrice": 1080.0, "armRegionName": "eastus", "productName": "SQL Database Single/Elastic Pool General Purpose - Compute Gen5", "serviceName": "SQL Database", "armSkuName": "SQLDB_GP_Compute_Gen5_ZR", "skuName": "vCore ZR Zone Redundancy", "meterName": "Zone Redundancy vCore", "unitOfMeasure": "1 Hour", "reservationTerm": "3 Years", "type": "Reservation"},
{"currencyCode": "USD", "retailPrice": 1733.0, "unitPrice": 1733.0, "armRegionName": "eastus", "productName": "SQL Database Single/Elastic Pool Business Critical - Compute Gen5", "serviceName": "SQL Database", "armSkuName": "SQLDB_BC_Compute_Gen5", "skuName": "vCore", "meterName": "vCore", "unitOfMeasure": "1 Hour", "reservationTerm": "1 Year", "type": "Reservation"},
{"currencyCode": "USD", "retailPrice": 3600.0, "unitPrice": 3600.0, "armRegionName": "eastus", "productName": "SQL Database Single/Elastic Pool Business Critical - Compute Gen5", "serviceName": "SQL Database", "armSkuName": "SQLDB_BC_Compute_Gen5", "skuName": "vCore", "meterName": "vCore", "unitOfMeasure": "1 Hour", "reservationTerm": "3 Years", "type": "Reservation"},
{"currencyCode": "USD", "retailPrice": 1040.0, "unitPrice": 1040.0, "armRegionName": "eastus", "productName": "SQL Database SingleDB/Elastic Pool Hyperscale - Compute Gen5", "serviceName": "SQL Database", "armSkuName": "SQLDB_HyperScale_Compute_Gen5", "skuName": "vCore", "meterName": "vCore", "unitOfMeasure": "1 Hour", "reservationTerm": "1 Year", "type": "Reservation"},
{"currencyCode": "USD", "retailPrice": 2160.0, "unitPrice": 2160.0, "armRegionName": "eastus", "productName": "SQL Database SingleDB/Elastic Pool Hyperscale - Compute Gen5", "serviceName": "SQL Database", "armSkuName": "SQLDB_HyperScale_Compute_Gen5", "skuName": "vCore", "meterName": "vCore", "unitOfMeasure": "1 Hour", "reservationTerm": "3 Years", "type": "Reservation"}
]
`

// liveCatalog answers Retail Prices requests the way the live API does: only
// rows whose armSkuName equals the filter's armSkuName are returned.
type liveCatalog struct {
	rows    []map[string]any
	filters []string
}

func newLiveCatalog(t *testing.T) *liveCatalog {
	t.Helper()
	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(liveRetailRows), &rows))
	return &liveCatalog{rows: rows}
}

func (l *liveCatalog) Do(req *http.Request) (*http.Response, error) {
	filter := req.URL.Query().Get("$filter")
	l.filters = append(l.filters, filter)
	items := []map[string]any{}
	for _, r := range l.rows {
		if strings.Contains(filter, "armSkuName eq '"+r["armSkuName"].(string)+"'") {
			items = append(items, r)
		}
	}
	body, err := json.Marshal(map[string]any{"Items": items, "Count": len(items), "NextPageLink": ""})
	if err != nil {
		return nil, err
	}
	return mocks.CreateMockHTTPResponse(http.StatusOK, string(body)), nil
}

func TestRetailSKUForCapability(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		capability string
		armSKU     string
		vCores     int
	}{
		{"GP_Gen5_2", "SQLDB_GP_Compute_Gen5", 2},
		{"GeneralPurpose_Gen5_8", "SQLDB_GP_Compute_Gen5", 8},
		{"BC_Gen5_4", "SQLDB_BC_Compute_Gen5", 4},
		{"BusinessCritical_Gen5_4", "SQLDB_BC_Compute_Gen5", 4},
		{"HS_Gen5_8", "SQLDB_HyperScale_Compute_Gen5", 8},
		{"Hyperscale_Gen5_2", "SQLDB_HyperScale_Compute_Gen5", 2},
		{"GP_Gen5_2_ZR", "SQLDB_GP_Compute_Gen5_ZR", 2},
		{"SQLDB_GP_Compute_Gen5", "SQLDB_GP_Compute_Gen5", 1},
		{"SQLDB_GP_Compute_Gen5_ZR", "SQLDB_GP_Compute_Gen5_ZR", 1},
		{"SQLDB_HyperScale_Compute_Gen5", "SQLDB_HyperScale_Compute_Gen5", 1},
		{"GeneralPurpose_Gen5_4_ZR", "SQLDB_GP_Compute_Gen5_ZR", 4},
	} {
		armSKU, vCores, err := retailSKUForCapability(tc.capability)
		require.NoError(t, err, tc.capability)
		assert.Equal(t, tc.armSKU, armSKU, tc.capability)
		assert.Equal(t, tc.vCores, vCores, tc.capability)
	}
}

func TestRetailSKUForCapability_RejectsUnmappedOrMalformed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ capability, wantErr string }{
		{"GP_Gen4_2", "no retail SKU mapping"},
		{"XX_Gen5_2", "no retail SKU mapping"},
		{"Standard_S0", "is not <tier>_<generation>_<vCores>"},
		{"GP_Gen5", "is not <tier>_<generation>_<vCores>"},
		{"GP_Gen5_2_3", "is not <tier>_<generation>_<vCores>"},
		{"GP_Gen5_0", "invalid vCore count"},
		{"GP_Gen5_+2", "invalid vCore count"},
		{"GP_Gen5_02", "invalid vCore count"},
		{"GP_Gen5_x", "invalid vCore count"},
		{"GP_Gen5_-2", "invalid vCore count"},
		{"BC_Gen5_4_ZR", "no retail SKU mapping"},
		{"HS_Gen5_8_ZR", "no retail SKU mapping"},
		{"GP_Gen5_0_ZR", "invalid vCore count"},
	} {
		_, _, err := retailSKUForCapability(tc.capability)
		require.ErrorContains(t, err, tc.wantErr, tc.capability)
	}
}

// The per-vCore live row times the vCore count is the reservation total.
func TestClient_GetOfferingDetails_LiveRetailRows(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		capability, term, payment string
		total, recurring          float64
	}{
		{"GP_Gen5_2", "1yr", "upfront", 867 * 2, 0},
		{"GP_Gen5_2", "3yr", "monthly", 1800 * 2, 1800 * 2 / 36.0},
		{"BC_Gen5_4", "1yr", "upfront", 1733 * 4, 0},
		{"BC_Gen5_4", "3yr", "monthly", 3600 * 4, 3600 * 4 / 36.0},
		{"HS_Gen5_8", "1yr", "upfront", 1040 * 8, 0},
		{"HS_Gen5_8", "3yr", "monthly", 2160 * 8, 2160 * 8 / 36.0},
	} {
		t.Run(tc.capability+"_"+tc.term, func(t *testing.T) {
			catalog := newLiveCatalog(t)
			details, err := NewClientWithHTTP(nil, "sub", "eastus", catalog).GetOfferingDetails(context.Background(), common.Recommendation{
				ResourceType: tc.capability, Term: tc.term, PaymentOption: tc.payment,
			})
			require.NoError(t, err)
			require.NotNil(t, details)
			assert.Equal(t, tc.total, details.TotalCost)
			assert.Equal(t, "USD", details.Currency)
			if tc.payment == "upfront" {
				assert.Equal(t, tc.total, details.UpfrontCost)
				assert.Zero(t, details.RecurringCost)
			} else {
				assert.Zero(t, details.UpfrontCost)
				assert.InDelta(t, tc.recurring, details.RecurringCost, 1e-9)
			}
		})
	}
}

// Regression for #196: the request must name the retail armSkuName. The
// capability name (GP_Gen5_2) matches no catalog row, so this fails before
// the fix with "no pricing data found".
func TestClient_GetOfferingDetails_FilterUsesRetailArmSKU(t *testing.T) {
	t.Parallel()
	catalog := newLiveCatalog(t)
	details, err := NewClientWithHTTP(nil, "sub", "eastus", catalog).GetOfferingDetails(context.Background(), common.Recommendation{
		ResourceType: "GP_Gen5_2", Term: "1yr", PaymentOption: "upfront",
	})
	require.NoError(t, err)
	assert.Equal(t, 1734.0, details.TotalCost)
	require.Len(t, catalog.filters, 1)
	assert.Equal(t, "serviceName eq 'SQL Database' and armRegionName eq 'eastus' and armSkuName eq 'SQLDB_GP_Compute_Gen5' and priceType eq 'Reservation'", catalog.filters[0])
}

// Regression for #196 item 2: a zone-redundant capability is priced from the
// SQLDB_GP_Compute_Gen5_ZR row (520 and 1080 per vCore), never the non-ZR row
// (867 and 1800). Fails before the fix: "GP_Gen5_2_ZR" is not a valid capability.
func TestClient_GetOfferingDetails_ZoneRedundantUsesZRRow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		term, payment string
		total, recur  float64
	}{
		{"1yr", "upfront", 520 * 2, 0},
		{"3yr", "monthly", 1080 * 2, 1080 * 2 / 36.0},
	} {
		catalog := newLiveCatalog(t)
		details, err := NewClientWithHTTP(nil, "sub", "eastus", catalog).GetOfferingDetails(context.Background(), common.Recommendation{
			ResourceType: "GP_Gen5_2_ZR", Term: tc.term, PaymentOption: tc.payment,
		})
		require.NoError(t, err, tc.term)
		require.NotNil(t, details)
		assert.Equal(t, tc.total, details.TotalCost, tc.term)
		if tc.payment == "upfront" {
			assert.Equal(t, tc.total, details.UpfrontCost, tc.term)
		} else {
			assert.InDelta(t, tc.recur, details.RecurringCost, 1e-9, tc.term)
		}
		require.Len(t, catalog.filters, 1)
		assert.Contains(t, catalog.filters[0], "armSkuName eq 'SQLDB_GP_Compute_Gen5_ZR'")
	}
}

// Regression for #196 item 3: both capability spellings price the same row.
// Fails before the fix: the GeneralPurpose spelling had no retail mapping.
func TestClient_GetOfferingDetails_AcceptsBothGPSpellings(t *testing.T) {
	t.Parallel()
	for _, capability := range []string{"GP_Gen5_2", "GeneralPurpose_Gen5_2"} {
		catalog := newLiveCatalog(t)
		details, err := NewClientWithHTTP(nil, "sub", "eastus", catalog).GetOfferingDetails(context.Background(), common.Recommendation{
			ResourceType: capability, Term: "1yr", PaymentOption: "upfront",
		})
		require.NoError(t, err, capability)
		assert.Equal(t, 867.0*2, details.TotalCost, capability)
		require.Len(t, catalog.filters, 1)
		assert.Contains(t, catalog.filters[0], "armSkuName eq 'SQLDB_GP_Compute_Gen5'", capability)
	}
}

// A capability whose live row is absent fails loud rather than defaulting.
func TestClient_GetOfferingDetails_MissingLiveRowFailsLoud(t *testing.T) {
	t.Parallel()
	catalog := newLiveCatalog(t)
	catalog.rows = catalog.rows[:2]
	details, err := NewClientWithHTTP(nil, "sub", "eastus", catalog).GetOfferingDetails(context.Background(), common.Recommendation{
		ResourceType: "BC_Gen5_4", Term: "1yr", PaymentOption: "upfront",
	})
	require.ErrorContains(t, err, "no pricing data found for SKU BC_Gen5_4 (retail SQLDB_BC_Compute_Gen5)")
	assert.Nil(t, details)
}

// An unmapped capability is refused before any Retail Prices request.
func TestClient_GetOfferingDetails_UnmappedCapabilityMakesNoRequest(t *testing.T) {
	t.Parallel()
	details, err := NewClientWithHTTP(nil, "sub", "eastus", &mocks.MockHTTPClient{}).GetOfferingDetails(context.Background(), common.Recommendation{
		ResourceType: "GP_Gen4_2", Term: "1yr", PaymentOption: "upfront",
	})
	require.ErrorContains(t, err, "no retail SKU mapping")
	assert.Nil(t, details)
}
