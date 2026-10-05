package cosmosdb_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/cosmosdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cosmosCatalogCase struct {
	arm, sku, meter    string
	oneYear, threeYear float64
}

// Historical October 1 capture SHA256 9b2ab203eb72893601e4f069923e55e0ac1fe76913dae99de8e4b8ec093290c3; not live acceptance.
func cosmosCatalog() []cosmosCatalogCase {
	return []cosmosCatalogCase{
		{"Cosmos_DB_100_RUs", "100 RU/s", "100 RU/s", 56, 147},
		{"Cosmos_DB_1_Million_RUs", "1 Million RU/s", "100 RU/s", 511584, 1271952},
		{"Cosmos_DB_100_mRUs", "100 Multi-master RU/s", "100 Multi-master RU/s", 112, 294},
		{"Cosmos_DB_1_Million_mRUs", "1 Million Multi-master RU/s", "100 Multi-master RU/s", 953088, 2333664},
	}
}

func (c cosmosCatalogCase) row(years int) map[string]any {
	term, price := "1 Year", c.oneYear
	if years == 3 {
		term, price = "3 Years", c.threeYear
	}
	return map[string]any{
		"serviceName": "Azure Cosmos DB", "productName": "Azure Cosmos DB", "armRegionName": "Global",
		"armSkuName": c.arm, "skuName": c.sku, "meterName": c.meter, "unitOfMeasure": "1/Hour",
		"currencyCode": "USD", "type": "Reservation", "reservationTerm": term, "retailPrice": price, "unitPrice": price,
	}
}

// Synthetic consumption row makes existing parent selection reachable without inventing a captured ARM consumption identity.
func cosmosConsumption() map[string]any {
	r := cosmosCatalog()[0].row(1)
	r["type"], r["reservationTerm"], r["retailPrice"], r["unitPrice"] = "Consumption", "", 1, 1
	return r
}

type cosmosRetailTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (c cosmosRetailTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || req.URL.Scheme != "https" || req.URL.Hostname() != "prices.azure.com" || (req.URL.Port() != "" && req.URL.Port() != "443") || req.URL.Path != "/api/retail/prices" {
		return nil, fmt.Errorf("unexpected retail request: %s %s", req.Method, req.URL)
	}
	local := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = c.target.Scheme, c.target.Host
	local.URL = &u
	return c.base.RoundTrip(local)
}

// Adapted from the existing Redis public transport; package-local tests cannot import that external test package.
func cosmosHTTP(t *testing.T, pages [][]map[string]any, wantFilter string) (*http.Client, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
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
		} else if wantFilter != "" {
			assert.Equal(t, wantFilter, req.URL.Query().Get("$filter"))
			assert.Equal(t, "2023-01-01-preview", req.URL.Query().Get("api-version"))
		}
		if len(pages) == 0 {
			http.Error(w, "unexpected catalog call", http.StatusBadRequest)
			return
		}
		next := ""
		if page+1 < len(pages) {
			next = "https://prices.azure.com:443/api/retail/prices?page=" + strconv.Itoa(page+1)
		}
		rows := pages[page]
		if wantFilter != "" {
			for _, clause := range strings.Split(req.URL.Query().Get("$filter"), " and ") {
				parts := strings.SplitN(clause, " eq '", 2)
				if len(parts) != 2 || !strings.HasSuffix(parts[1], "'") {
					http.Error(w, "unexpected filter", http.StatusBadRequest)
					return
				}
				field, value := parts[0], strings.ReplaceAll(strings.TrimSuffix(parts[1], "'"), "''", "'")
				if field == "priceType" {
					field = "type"
				}
				rows = slices.DeleteFunc(slices.Clone(rows), func(row map[string]any) bool { return row[field] != value })
			}
		}
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Items": rows, "NextPageLink": next}))
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	h := server.Client()
	h.Transport = cosmosRetailTransport{h.Transport, target}
	h.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return h, requests
}

func cosmosQuote(t *testing.T, sku string, pages [][]map[string]any) (*common.OfferingDetails, error) {
	t.Helper()
	h, requests := cosmosHTTP(t, pages, "")
	q, err := cosmosdb.NewClientWithHTTP(nil, "synthetic-subscription", "eastus", h).GetOfferingDetails(context.Background(), common.Recommendation{
		ResourceType: sku, Term: "1yr", PaymentOption: "upfront",
	})
	assert.Equal(t, int32(len(pages)), requests.Load())
	return q, err
}

func assertCosmosQuote(t *testing.T, q *common.OfferingDetails, sku, region, term, payment, currency string, price float64, years int) {
	t.Helper()
	require.NotNil(t, q)
	assert.Equal(t, "azure-cosmos-"+sku+"-"+region+"-"+term, q.OfferingID)
	assert.Equal(t, sku, q.ResourceType)
	assert.Equal(t, term, q.Term)
	assert.Equal(t, payment, q.PaymentOption)
	assert.Equal(t, currency, q.Currency)
	assert.Equal(t, price, q.TotalCost)
	assert.InDelta(t, price/(8760*float64(years)), q.EffectiveHourlyRate, 1e-12)
	if payment == "monthly" || payment == "no-upfront" {
		assert.Zero(t, q.UpfrontCost)
		assert.Equal(t, price/(12*float64(years)), q.RecurringCost)
	} else {
		assert.Equal(t, price, q.UpfrontCost)
		assert.Zero(t, q.RecurringCost)
	}
}

func TestCosmosOffering_CapturedReservations(t *testing.T) {
	for i, fixture := range cosmosCatalog() {
		aliases := []string{fixture.arm}
		if i == 0 {
			aliases = append(aliases, "100RU", "100RUperSecond")
		}
		for _, sku := range aliases {
			for _, years := range []int{1, 3} {
				for _, payment := range []string{"upfront", "all-upfront", "monthly", "no-upfront"} {
					t.Run(fmt.Sprintf("%s/%d/%s", sku, years, payment), func(t *testing.T) {
						r := fixture.row(years)
						h, requests := cosmosHTTP(t, [][]map[string]any{{r}}, "")
						term := strconv.Itoa(years) + "yr"
						q, err := cosmosdb.NewClientWithHTTP(nil, "synthetic-subscription", "westus", h).GetOfferingDetails(context.Background(), common.Recommendation{ResourceType: sku, Term: term, PaymentOption: payment, Count: 3})
						require.NoError(t, err)
						assert.Equal(t, int32(1), requests.Load())
						assertCosmosQuote(t, q, sku, "westus", term, payment, "USD", r["retailPrice"].(float64), years)
					})
				}
			}
		}
	}
}

func TestCosmosOffering_FilterAndEscaping(t *testing.T) {
	for _, sku := range []string{"100RU", "100RUperSecond", "Cosmos_DB_100_RUs", "Cosmos_DB_test'RU"} {
		t.Run(sku, func(t *testing.T) {
			arm := "Cosmos_DB_100_RUs"
			if strings.HasPrefix(sku, "Cosmos_DB_") {
				arm = sku
			}
			r := cosmosCatalog()[0].row(1)
			r["armSkuName"] = arm
			filter := "serviceName eq 'Azure Cosmos DB' and productName eq 'Azure Cosmos DB' and armRegionName eq 'Global' and armSkuName eq '" + strings.ReplaceAll(arm, "'", "''") + "' and priceType eq 'Reservation'"
			h, requests := cosmosHTTP(t, [][]map[string]any{{cosmosConsumption(), r}}, filter)
			q, err := cosmosdb.NewClientWithHTTP(nil, "synthetic-subscription", "east'us", h).GetOfferingDetails(context.Background(), common.Recommendation{ResourceType: sku, Term: "1yr", PaymentOption: "upfront"})
			require.NoError(t, err)
			assert.Equal(t, int32(1), requests.Load())
			assertCosmosQuote(t, q, sku, "east'us", "1yr", "upfront", "USD", 56, 1)
		})
	}
}

func TestCosmosOffering_UnknownAliasesAvoidHTTP(t *testing.T) {
	for _, sku := range []string{"", "1000RU", "1000000RU", "100mRU", "100ru", "100RU'", " 100RU", "CosmosDB_RU_1000", "EnableCassandra"} {
		t.Run(sku, func(t *testing.T) {
			q, err := cosmosQuote(t, sku, nil)
			require.Error(t, err)
			assert.Nil(t, q)
		})
	}
}

func TestCosmosOffering_ForeignMalformedRowsSynthetic(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(strconv.FormatBool(reverse), func(t *testing.T) {
			first, second := []map[string]any{cosmosConsumption(), cosmosCatalog()[0].row(1)}, []map[string]any{}
			for _, arm := range []string{"", "Azure_DocumentDB_Coordinator_Node_1vCore", "Cosmos_DB_1000_RUs", "Cosmos_DB_1_Million_RUs"} {
				r := cosmosCatalog()[0].row(1)
				r["armSkuName"], r["meterName"], r["unitOfMeasure"], r["currencyCode"], r["retailPrice"] = arm, "Storage", "invalid", "", 0
				second = append(second, r)
			}
			if reverse {
				first, second = second, first
			}
			q, err := cosmosQuote(t, "Cosmos_DB_100_RUs", [][]map[string]any{first, second})
			require.NoError(t, err)
			assertCosmosQuote(t, q, "Cosmos_DB_100_RUs", "eastus", "1yr", "upfront", "USD", 56, 1)
		})
	}
}

func TestCosmosOffering_ValidShapesSynthetic(t *testing.T) {
	for _, currency := range []string{"USD", "EUR"} {
		for _, unit := range []string{"1 Hour", "1/Hour"} {
			t.Run(currency+"/"+unit, func(t *testing.T) {
				r := cosmosCatalog()[0].row(1)
				r["currencyCode"], r["unitOfMeasure"] = currency, unit
				q, err := cosmosQuote(t, "Cosmos_DB_100_RUs", [][]map[string]any{{cosmosConsumption(), r}})
				require.NoError(t, err)
				assertCosmosQuote(t, q, "Cosmos_DB_100_RUs", "eastus", "1yr", "upfront", currency, 56, 1)
			})
		}
	}
}

func TestCosmosOffering_PaginatedReservation(t *testing.T) {
	q, err := cosmosQuote(t, "Cosmos_DB_100_RUs", [][]map[string]any{{cosmosConsumption()}, {cosmosCatalog()[0].row(1)}})
	require.NoError(t, err)
	assertCosmosQuote(t, q, "Cosmos_DB_100_RUs", "eastus", "1yr", "upfront", "USD", 56, 1)
}

func TestCosmosOffering_NoCanonicalMatch(t *testing.T) {
	q, err := cosmosQuote(t, "Cosmos_DB_unknown", [][]map[string]any{{cosmosCatalog()[0].row(1)}})
	require.Error(t, err)
	assert.Nil(t, q)
}

func TestCosmosOffering_MixedTerms(t *testing.T) {
	for _, years := range []int{1, 3} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/reverse=%t", years, reverse), func(t *testing.T) {
				first, second := cosmosCatalog()[0].row(1), cosmosCatalog()[0].row(3)
				if reverse {
					first, second = second, first
				}
				h, requests := cosmosHTTP(t, [][]map[string]any{{cosmosConsumption(), first, second}}, "")
				term := strconv.Itoa(years) + "yr"
				q, err := cosmosdb.NewClientWithHTTP(nil, "synthetic-subscription", "eastus", h).GetOfferingDetails(context.Background(), common.Recommendation{ResourceType: "Cosmos_DB_100_RUs", Term: term, PaymentOption: "upfront"})
				require.NoError(t, err)
				assert.Equal(t, int32(1), requests.Load())
				want := 56.0
				if years == 3 {
					want = 147
				}
				assertCosmosQuote(t, q, "Cosmos_DB_100_RUs", "eastus", term, "upfront", "USD", want, years)
			})
		}
	}
}
