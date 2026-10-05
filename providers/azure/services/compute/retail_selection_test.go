package compute_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/compute"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Synthetic prices use the documented Retail Prices JSON shape, not a live quote.
type retailRow struct {
	Currency  string  `json:"currencyCode"`
	Region    string  `json:"armRegionName"`
	Service   string  `json:"serviceName"`
	ARM       string  `json:"armSkuName"`
	SKU       string  `json:"skuName"`
	Product   string  `json:"productName"`
	Meter     string  `json:"meterName"`
	Unit      string  `json:"unitOfMeasure"`
	Term      string  `json:"reservationTerm,omitempty"`
	Type      string  `json:"type"`
	Price     float64 `json:"retailPrice"`
	UnitPrice float64 `json:"unitPrice"`
	MeterID   string  `json:"meterId"`
	ProductID string  `json:"productId"`
	SKUID     string  `json:"skuId"`
	Effective string  `json:"effectiveStartDate"`
	Primary   bool    `json:"isPrimaryMeterRegion"`
}

func reservationRow() retailRow {
	return retailRow{
		Currency: "USD", Region: "eastus", Service: "Virtual Machines",
		ARM: "Standard_D2s_v3", SKU: "D2s v3", Product: "Virtual Machines Dsv3 Series",
		Meter: "D2s v3", Unit: "1 Hour", Term: "1 Year", Type: "Reservation",
		Price: 1200, UnitPrice: 1200, Primary: true,
	}
}

func consumptionRow() retailRow {
	r := reservationRow()
	r.Term, r.Type, r.Price, r.UnitPrice = "", "Consumption", 1, 1
	return r
}

type retailHTTP struct {
	client *http.Client
	target *url.URL
}

func (c retailHTTP) Do(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || req.URL.Scheme != "https" || req.URL.Hostname() != "prices.azure.com" || (req.URL.Port() != "" && req.URL.Port() != "443") || req.URL.Path != "/api/retail/prices" {
		return nil, fmt.Errorf("unexpected retail request: %s %s", req.Method, req.URL)
	}
	local := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = c.target.Scheme, c.target.Host
	local.URL = &u
	return c.client.Do(local)
}

func quoteRows(t *testing.T, pages [][]retailRow, term, payment string, honorTypeFilter bool) (*common.OfferingDetails, error) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		page := 0
		if raw := req.URL.Query().Get("page"); raw != "" {
			var err error
			page, err = strconv.Atoi(raw)
			if err != nil || page < 0 || page >= len(pages) {
				http.Error(w, "unexpected page", http.StatusBadRequest)
				return
			}
		} else {
			filter := req.URL.Query().Get("$filter")
			assert.Contains(t, filter, "serviceName eq 'Virtual Machines'")
			assert.Contains(t, filter, "armRegionName eq 'eastus'")
			assert.Contains(t, filter, "armSkuName eq 'Standard_D2s_v3'")
		}
		rows := pages[page]
		if honorTypeFilter && strings.Contains(req.URL.Query().Get("$filter"), "priceType eq 'Reservation'") {
			rows = nil
			for _, row := range pages[page] {
				if row.Type == "Reservation" {
					rows = append(rows, row)
				}
			}
		}
		next := ""
		if page+1 < len(pages) {
			next = "https://prices.azure.com:443/api/retail/prices?page=" + strconv.Itoa(page+1)
		}
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(struct {
			Items []retailRow `json:"Items"`
			Next  string      `json:"NextPageLink"`
		}{rows, next}))
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	client := server.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	c := compute.NewClientWithHTTP(nil, "synthetic-subscription", "eastus", retailHTTP{client, target})
	quote, err := c.GetOfferingDetails(context.Background(), common.Recommendation{
		ResourceType: "Standard_D2s_v3", Term: term, PaymentOption: payment,
	})
	assert.Equal(t, int32(len(pages)), requests.Load(), "all catalog pages must be consumed")
	return quote, err
}

func assertQuote(t *testing.T, q *common.OfferingDetails, price float64, currency, term, payment string, years int) {
	t.Helper()
	require.NotNil(t, q)
	assert.Equal(t, "azure-vm-Standard_D2s_v3-eastus-"+term, q.OfferingID)
	assert.Equal(t, "Standard_D2s_v3", q.ResourceType)
	assert.Equal(t, term, q.Term)
	assert.Equal(t, payment, q.PaymentOption)
	assert.Equal(t, currency, q.Currency)
	assert.Equal(t, price, q.TotalCost)
	assert.InDelta(t, price/(8760*float64(years)), q.EffectiveHourlyRate, 1e-12)
	if payment == "monthly" {
		assert.Zero(t, q.UpfrontCost)
		assert.Equal(t, price/(12*float64(years)), q.RecurringCost)
	} else {
		assert.Equal(t, price, q.UpfrontCost)
		assert.Zero(t, q.RecurringCost)
	}
}

func TestGetOfferingDetails_ReservationOnlySynthetic(t *testing.T) {
	for _, years := range []int{1, 3} {
		for _, payment := range []string{"upfront", "monthly"} {
			t.Run(fmt.Sprintf("%dyr/%s", years, payment), func(t *testing.T) {
				r := reservationRow()
				term := strconv.Itoa(years) + "yr"
				if years == 3 {
					r.Term, r.Price, r.UnitPrice = "3 Years", 3000, 3000
				}
				q, err := quoteRows(t, [][]retailRow{{r}}, term, payment, true)
				require.NoError(t, err)
				assertQuote(t, q, r.Price, "USD", term, payment, years)
			})
		}
	}
}

func TestGetOfferingDetails_RequestsReservationCatalogSynthetic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		assert.Contains(t, req.URL.Query().Get("$filter"), "priceType eq 'Reservation'")
		assert.NoError(t, json.NewEncoder(w).Encode(struct {
			Items []retailRow `json:"Items"`
		}{[]retailRow{consumptionRow(), reservationRow()}}))
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	client := server.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	c := compute.NewClientWithHTTP(nil, "synthetic-subscription", "eastus", retailHTTP{client, target})
	q, err := c.GetOfferingDetails(context.Background(), common.Recommendation{
		ResourceType: "Standard_D2s_v3", Term: "1yr", PaymentOption: "upfront",
	})
	require.NoError(t, err)
	assertQuote(t, q, 1200, "USD", "1yr", "upfront", 1)
}

func TestGetOfferingDetails_ValidReservationShapesSynthetic(t *testing.T) {
	cases := map[string]func(*retailRow){
		"ordinary":                   func(*retailRow) {},
		"slash hour":                 func(r *retailRow) { r.Unit = "1/Hour" },
		"EUR":                        func(r *retailRow) { r.Currency = "EUR" },
		"independent meter and SKU":  func(r *retailRow) { r.Meter = "Compute capacity" },
		"alternative product suffix": func(r *retailRow) { r.Product = "Virtual Machines Dsv3 Compute" },
		"alternative product prefix": func(r *retailRow) { r.Product = "Compute Capacity Dsv3" },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			r := reservationRow()
			edit(&r)
			q, err := quoteRows(t, [][]retailRow{{consumptionRow(), r}}, "1yr", "upfront", true)
			require.NoError(t, err)
			assertQuote(t, q, r.Price, r.Currency, "1yr", "upfront", 1)
		})
	}
}

func TestGetOfferingDetails_UnrelatedMalformedPricesSynthetic(t *testing.T) {
	for _, kind := range []string{"other SKU reservation", "Windows Spot consumption"} {
		t.Run(kind, func(t *testing.T) {
			decoy := reservationRow()
			decoy.Currency, decoy.Unit, decoy.Price, decoy.UnitPrice = "EUR", "100 Hours", 9900, 9900
			if kind == "other SKU reservation" {
				decoy.ARM = "Standard_D20s_v3"
			} else {
				decoy.Type, decoy.Term = "Consumption", ""
				decoy.Product += " Windows"
				decoy.Meter += " Spot"
			}
			q, err := quoteRows(t, [][]retailRow{{consumptionRow(), reservationRow()}, {decoy}}, "1yr", "upfront", false)
			require.NoError(t, err)
			assertQuote(t, q, 1200, "USD", "1yr", "upfront", 1)
		})
	}
}

func TestGetOfferingDetails_SelectedReservationValidationSynthetic(t *testing.T) {
	cases := map[string]func(*retailRow){
		"missing currency":  func(r *retailRow) { r.Currency = "" },
		"missing unit":      func(r *retailRow) { r.Unit = "" },
		"100 Hours":         func(r *retailRow) { r.Unit = "100 Hours" },
		"DTU unit":          func(r *retailRow) { r.Unit = "1 DTU/Hour" },
		"zero retail price": func(r *retailRow) { r.Price = 0 },
		"negative price":    func(r *retailRow) { r.Price = -1 },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			r := reservationRow()
			edit(&r)
			q, err := quoteRows(t, [][]retailRow{{consumptionRow(), r}}, "1yr", "upfront", false)
			require.Error(t, err)
			assert.Nil(t, q)
		})
	}
}

func TestGetOfferingDetails_ReservationIdentitySynthetic(t *testing.T) {
	cases := map[string]func(*retailRow){
		"wrong ARM SKU":                     func(r *retailRow) { r.ARM = "Standard_D20s_v3" },
		"wrong region":                      func(r *retailRow) { r.Region = "westus" },
		"wrong service":                     func(r *retailRow) { r.Service = "SQL Database" },
		"missing product":                   func(r *retailRow) { r.Product = "" },
		"missing meter":                     func(r *retailRow) { r.Meter = "" },
		"missing display SKU":               func(r *retailRow) { r.SKU = "" },
		"Windows product":                   func(r *retailRow) { r.Product += " Windows" },
		"Spot meter":                        func(r *retailRow) { r.Meter += " Spot" },
		"Spot display SKU":                  func(r *retailRow) { r.SKU += " Spot" },
		"Low Priority meter":                func(r *retailRow) { r.Meter += " Low Priority" },
		"Low Priority display SKU":          func(r *retailRow) { r.SKU += " Low Priority" },
		"consumption with reservation term": func(r *retailRow) { r.Type = "Consumption" },
		"DevTest with reservation term":     func(r *retailRow) { r.Type = "DevTestConsumption" },
		"substring term":                    func(r *retailRow) { r.Term = "11 Year" },
		"other term":                        func(r *retailRow) { r.Term = "3 Years" },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			bad := reservationRow()
			bad.Price, bad.UnitPrice = 9900, 9900
			edit(&bad)
			for _, includeValid := range []bool{false, true} {
				for _, badLast := range []bool{false, true} {
					t.Run(fmt.Sprintf("valid=%t/badLast=%t", includeValid, badLast), func(t *testing.T) {
						first, second := []retailRow{consumptionRow()}, []retailRow{bad}
						if includeValid {
							first = append(first, reservationRow())
						}
						if !badLast {
							first, second = second, first
						}
						q, err := quoteRows(t, [][]retailRow{first, second}, "1yr", "upfront", false)
						if !includeValid {
							require.Error(t, err)
							assert.Nil(t, q)
							return
						}
						require.NoError(t, err)
						assertQuote(t, q, 1200, "USD", "1yr", "upfront", 1)
					})
				}
			}
		})
	}
}

func TestGetOfferingDetails_ReservationAmbiguitySynthetic(t *testing.T) {
	cases := map[string]func(*retailRow){
		"price":                 func(r *retailRow) { r.Price, r.UnitPrice = 2400, 2400 },
		"currency":              func(r *retailRow) { r.Currency = "EUR" },
		"product at same price": func(r *retailRow) { r.Product = "Virtual Machines Alternate Product" },
		"meter at same price":   func(r *retailRow) { r.Meter = "Alternate compute capacity" },
		"SKU at same price":     func(r *retailRow) { r.SKU = "Alternate display" },
	}
	for name, edit := range cases {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reverse=%t", name, reverse), func(t *testing.T) {
				r := reservationRow()
				edit(&r)
				first, second := reservationRow(), r
				if reverse {
					first, second = second, first
				}
				q, err := quoteRows(t, [][]retailRow{{consumptionRow(), first}, {second}}, "1yr", "upfront", false)
				require.Error(t, err)
				assert.Nil(t, q)
			})
		}
	}
}

func TestGetOfferingDetails_EquivalentReservationMetadataSynthetic(t *testing.T) {
	for _, conflictingPrice := range []bool{false, true} {
		t.Run(fmt.Sprintf("conflictingPrice=%t", conflictingPrice), func(t *testing.T) {
			r := reservationRow()
			r.MeterID, r.ProductID, r.SKUID = "other-meter", "other-product", "other-SKU"
			r.Effective, r.Primary, r.Unit = "2020-01-01T00:00:00Z", false, "1/Hour"
			if conflictingPrice {
				r.Price, r.UnitPrice = 2000, 2000
			}
			q, err := quoteRows(t, [][]retailRow{{consumptionRow(), reservationRow()}, {r}}, "1yr", "upfront", false)
			if conflictingPrice {
				require.Error(t, err)
				assert.Nil(t, q)
				return
			}
			require.NoError(t, err)
			assertQuote(t, q, 1200, "USD", "1yr", "upfront", 1)
		})
	}
}
