package cache_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/cache"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/managedredis"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type redisOfferingClient interface {
	GetOfferingDetails(context.Context, common.Recommendation) (*common.OfferingDetails, error)
}

type redisClientCase struct {
	name, prefix string
	newClient    func(string, *http.Client) redisOfferingClient
}

func redisClients() []redisClientCase {
	return []redisClientCase{
		{"cache", "azure-redis-", func(region string, h *http.Client) redisOfferingClient {
			return cache.NewClientWithHTTP(nil, "synthetic-subscription", region, h)
		}},
		{"managedredis", "azure-managed-redis-", func(region string, h *http.Client) redisOfferingClient {
			return managedredis.NewClientWithHTTP(nil, "synthetic-subscription", region, h)
		}},
	}
}

type redisCatalogCase struct {
	arm, sku, product, meter string
	oneYear, threeYears      float64
}

// Legacy rows from the October 1 public capture, SHA256 fd306ac19fc1f3d5c59fceba789c305e38b069a8af524576cc56220415f188a0.
func redisCatalog() []redisCatalogCase {
	return []redisCatalogCase{
		{"Azure_Redis_Cache_Premium_P1_Cache", "P1", "Azure Redis Cache Premium", "P1 Cache Instance", 1553, 3276},
		{"Azure_Redis_Cache_Premium_P2_Cache", "P2", "Azure Redis Cache Premium", "P2 Cache Instance", 3112, 6563},
		{"Azure_Redis_Cache_Premium_P5_Cache", "P5", "Azure Redis Cache Premium", "P5 Cache Instance", 28172, 59426},
		{"Azure_Redis_Cache_Enterprise_E1", "E1", "Azure Redis Cache Enterprise", "E1 Cache", 228, 474},
		{"Azure_Redis_Cache_Enterprise_E10", "E10", "Azure Redis Cache Enterprise", "E10 Cache", 2736, 5682},
		{"Azure_Redis_Cache_Enterprise_Flash_F300", "F300", "Azure Redis Cache Enterprise Flash", "F300 Cache", 11420, 23719},
		{"Azure_Redis_Cache_Enterprise_Flash_F1500", "F1500", "Azure Redis Cache Enterprise Flash", "F1500 Cache", 45683, 94880},
	}
}

func (c redisCatalogCase) row(years int) map[string]any {
	term, price := "1 Year", c.oneYear
	if years == 3 {
		term, price = "3 Years", c.threeYears
	}
	return map[string]any{
		"serviceName": "Redis Cache", "armRegionName": "eastus", "armSkuName": c.arm,
		"skuName": c.sku, "productName": c.product, "meterName": c.meter,
		"currencyCode": "USD", "unitOfMeasure": "1 Hour", "type": "Reservation",
		"reservationTerm": term, "retailPrice": price, "unitPrice": price,
	}
}

func redisConsumption() map[string]any {
	r := redisCatalog()[0].row(1)
	r["type"], r["reservationTerm"], r["retailPrice"], r["unitPrice"] = "Consumption", "", 1, 1
	return r
}

type redisRetailTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (c redisRetailTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || req.URL.Scheme != "https" || req.URL.Hostname() != "prices.azure.com" || (req.URL.Port() != "" && req.URL.Port() != "443") || req.URL.Path != "/api/retail/prices" {
		return nil, fmt.Errorf("unexpected retail request: %s %s", req.Method, req.URL)
	}
	local := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = c.target.Scheme, c.target.Host
	local.URL = &u
	return c.base.RoundTrip(local)
}

func redisHTTP(t *testing.T, pages [][]map[string]any, filter []string) (*http.Client, *atomic.Int32) {
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
		} else if filter != nil {
			assert.Equal(t, strings.Join(filter, " and "), req.URL.Query().Get("$filter"))
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
		if filter != nil && strings.Contains(req.URL.Query().Get("$filter"), "priceType eq 'Reservation'") {
			rows = slices.DeleteFunc(slices.Clone(rows), func(row map[string]any) bool {
				return row["type"] != "Reservation"
			})
		}
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"Items": rows, "NextPageLink": next}))
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	h := server.Client()
	h.Transport = redisRetailTransport{h.Transport, target}
	h.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return h, requests
}

func redisQuote(t *testing.T, c redisClientCase, sku string, pages [][]map[string]any) (*common.OfferingDetails, error) {
	t.Helper()
	h, requests := redisHTTP(t, pages, nil)
	q, err := c.newClient("eastus", h).GetOfferingDetails(context.Background(), common.Recommendation{
		ResourceType: sku, Term: "1yr", PaymentOption: "upfront",
	})
	assert.Equal(t, int32(len(pages)), requests.Load())
	return q, err
}

func assertRedisQuote(t *testing.T, c redisClientCase, q *common.OfferingDetails, sku, term, payment, currency string, price float64, years int) {
	t.Helper()
	require.NotNil(t, q)
	assert.Equal(t, c.prefix+sku+"-eastus-"+term, q.OfferingID)
	assert.Equal(t, sku, q.ResourceType)
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

func TestRedisOffering_CapturedReservations(t *testing.T) {
	for _, c := range redisClients() {
		for _, fixture := range redisCatalog() {
			aliases := []string{fixture.arm}
			if strings.HasPrefix(fixture.sku, "P") {
				aliases = append(aliases, "Premium_"+fixture.sku)
			}
			for _, sku := range aliases {
				for _, years := range []int{1, 3} {
					for _, payment := range []string{"upfront", "monthly"} {
						t.Run(fmt.Sprintf("%s/%s/%d/%s", c.name, sku, years, payment), func(t *testing.T) {
							r := fixture.row(years)
							h, requests := redisHTTP(t, [][]map[string]any{{r}}, nil)
							term := strconv.Itoa(years) + "yr"
							q, err := c.newClient("eastus", h).GetOfferingDetails(context.Background(), common.Recommendation{ResourceType: sku, Term: term, PaymentOption: payment})
							require.NoError(t, err)
							assert.Equal(t, int32(1), requests.Load())
							assertRedisQuote(t, c, q, sku, term, payment, "USD", r["retailPrice"].(float64), years)
						})
					}
				}
			}
		}
	}
}

func TestRedisOffering_FilterAndEscaping(t *testing.T) {
	for _, c := range redisClients() {
		t.Run(c.name, func(t *testing.T) {
			r := redisCatalog()[0].row(1)
			r["armRegionName"] = "east'us"
			filter := []string{"serviceName eq 'Redis Cache'", "armRegionName eq 'east''us'", "armSkuName eq 'Azure_Redis_Cache_Premium_P1_Cache'", "priceType eq 'Reservation'"}
			h, _ := redisHTTP(t, [][]map[string]any{{redisConsumption(), r}}, filter)
			q, err := c.newClient("east'us", h).GetOfferingDetails(context.Background(), common.Recommendation{ResourceType: "Premium_P1", Term: "1yr", PaymentOption: "upfront"})
			require.NoError(t, err)
			assert.Equal(t, 1553.0, q.TotalCost)
		})
	}
}

func TestRedisOffering_UnknownAliasesAvoidHTTP(t *testing.T) {
	for _, c := range redisClients() {
		for _, sku := range []string{"", "Premium_P10", "Premium_P1'", "premium_p1", " Premium_P1", "Standard_C1", "Enterprise_E1", "Azure_Redis_Cache_Enterprise_E", "Azure_Redis_Cache_Enterprise_E1x", "Azure_Redis_Cache_Enterprise_E１", "Azure_Redis_Cache_Enterprise_Flash_F", "Azure_Managed_Redis_Balanced_B1"} {
			t.Run(c.name+"/"+sku, func(t *testing.T) {
				q, err := redisQuote(t, c, sku, nil)
				require.Error(t, err)
				assert.Nil(t, q)
			})
		}
	}
}

func TestRedisOffering_IdentityAndOrderSynthetic(t *testing.T) {
	badFields := map[string]any{
		"armSkuName": "Azure_Redis_Cache_Premium_P10_Cache", "armRegionName": "westus",
		"serviceName": "Azure Cache for Redis", "productName": "Azure Redis Cache Enterprise",
		"meterName": "P1 Cache", "skuName": "P10", "type": "Consumption", "reservationTerm": "11 Year",
	}
	for _, c := range redisClients() {
		for field, value := range badFields {
			for _, valid := range []bool{false, true} {
				for _, reverse := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/valid=%t/reverse=%t", c.name, field, valid, reverse), func(t *testing.T) {
						bad := redisCatalog()[0].row(1)
						bad[field], bad["retailPrice"], bad["unitPrice"] = value, 99999, 99999
						first, second := []map[string]any{redisConsumption()}, []map[string]any{bad}
						if valid {
							first = append(first, redisCatalog()[0].row(1))
						}
						if reverse {
							first, second = second, first
						}
						q, err := redisQuote(t, c, "Premium_P1", [][]map[string]any{first, second})
						if !valid {
							require.Error(t, err)
							assert.Nil(t, q)
							return
						}
						require.NoError(t, err)
						assertRedisQuote(t, c, q, "Premium_P1", "1yr", "upfront", "USD", 1553, 1)
					})
				}
			}
		}
	}
}

func TestRedisOffering_SelectedValidationSynthetic(t *testing.T) {
	for _, c := range redisClients() {
		for _, bad := range []struct {
			field string
			value any
		}{
			{"unitOfMeasure", ""}, {"unitOfMeasure", "100 Hours"}, {"currencyCode", ""},
			{"retailPrice", 0}, {"retailPrice", -1}, {"retailPrice", nil},
		} {
			t.Run(fmt.Sprintf("%s/%s/%v", c.name, bad.field, bad.value), func(t *testing.T) {
				r := redisCatalog()[0].row(1)
				r[bad.field] = bad.value
				q, err := redisQuote(t, c, "Premium_P1", [][]map[string]any{{redisConsumption(), r}})
				require.Error(t, err)
				assert.Nil(t, q)
			})
		}
	}
}

func TestRedisOffering_EquivalentAndConflictingQuotesSynthetic(t *testing.T) {
	for _, c := range redisClients() {
		for _, change := range []string{"equivalent", "price", "currency", "invalid selected unit"} {
			for _, reverse := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/%t", c.name, change, reverse), func(t *testing.T) {
					first := redisCatalog()[0].row(1)
					second := maps.Clone(first)
					second["unitOfMeasure"], second["meterId"], second["isPrimaryMeterRegion"], second["unitPrice"] = "1/Hour", "other", false, 5
					switch change {
					case "price":
						second["retailPrice"] = 2000
					case "currency":
						second["currencyCode"] = "EUR"
					case "invalid selected unit":
						second["unitOfMeasure"] = "100 Hours"
					}
					if reverse {
						first, second = second, first
					}
					q, err := redisQuote(t, c, "Premium_P1", [][]map[string]any{{redisConsumption(), first}, {second}})
					if change != "equivalent" {
						require.Error(t, err)
						assert.Nil(t, q)
						return
					}
					require.NoError(t, err)
					assertRedisQuote(t, c, q, "Premium_P1", "1yr", "upfront", "USD", 1553, 1)
				})
			}
		}
	}
}

func TestRedisOffering_PrefixNeighborsAndMalformedRowsSynthetic(t *testing.T) {
	for _, c := range redisClients() {
		for _, selected := range redisCatalog() {
			for _, reverse := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/%t", c.name, selected.sku, reverse), func(t *testing.T) {
					first, second := []map[string]any{redisConsumption(), selected.row(1)}, []map[string]any{}
					for _, neighbor := range redisCatalog() {
						if neighbor.arm != selected.arm {
							r := neighbor.row(1)
							r["unitOfMeasure"], r["currencyCode"] = "invalid", ""
							second = append(second, r)
						}
					}
					if reverse {
						first, second = second, first
					}
					q, err := redisQuote(t, c, selected.arm, [][]map[string]any{first, second})
					require.NoError(t, err)
					assertRedisQuote(t, c, q, selected.arm, "1yr", "upfront", "USD", selected.oneYear, 1)
				})
			}
		}
	}
}

func TestRedisOffering_ValidShapesSynthetic(t *testing.T) {
	for _, c := range redisClients() {
		for _, currency := range []string{"USD", "EUR"} {
			for _, unit := range []string{"1 Hour", "1/Hour"} {
				t.Run(c.name+"/"+currency+"/"+unit, func(t *testing.T) {
					r := redisCatalog()[0].row(1)
					r["currencyCode"], r["unitOfMeasure"] = currency, unit
					q, err := redisQuote(t, c, "Premium_P1", [][]map[string]any{{redisConsumption(), r}})
					require.NoError(t, err)
					assertRedisQuote(t, c, q, "Premium_P1", "1yr", "upfront", currency, 1553, 1)
				})
			}
		}
	}
}

func TestRedisOffering_PaginatedReservation(t *testing.T) {
	for _, c := range redisClients() {
		t.Run(c.name, func(t *testing.T) {
			q, err := redisQuote(t, c, "Premium_P1", [][]map[string]any{{redisConsumption()}, {redisCatalog()[0].row(1)}})
			require.NoError(t, err)
			assertRedisQuote(t, c, q, "Premium_P1", "1yr", "upfront", "USD", 1553, 1)
		})
	}
}
