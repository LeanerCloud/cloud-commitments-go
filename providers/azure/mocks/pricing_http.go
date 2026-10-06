package mocks

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
)

// PricingHTTP serves a fixed Retail Prices API page and counts requests. A
// request whose URL contains FailMatch gets an HTTP 500, so a test can make
// the lookup for one SKU fail while the others succeed. The rows are
// hand-built (synthetic prices, not a live Azure quote).
type PricingHTTP struct {
	Items     []map[string]any
	FailMatch string
	// Err, when set, is returned from every request (a transport failure).
	Err error

	mu    sync.Mutex
	calls int
}

// Do implements the HTTPClient interface the service clients accept.
func (p *PricingHTTP) Do(req *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if p.Err != nil {
		return nil, p.Err
	}
	if p.FailMatch != "" && strings.Contains(req.URL.RawQuery, p.FailMatch) {
		return CreateMockHTTPResponse(http.StatusInternalServerError, "boom"), nil
	}
	body, err := json.Marshal(map[string]any{"Items": p.Items, "Count": len(p.Items), "NextPageLink": ""})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
}

// Calls returns the number of requests served so far.
func (p *PricingHTTP) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// ReservationRow builds one priceType 'Reservation' retail row. price is the
// term total per unit, as the Retail Prices API reports it (unitOfMeasure
// "1 Hour" notwithstanding).
func ReservationRow(service, product, region, armSKU, skuName, meterName, term string, price float64) map[string]any {
	return map[string]any{
		"currencyCode": "USD", "retailPrice": price, "unitPrice": price,
		"armRegionName": region, "productName": product, "serviceName": service,
		"armSkuName": armSKU, "skuName": skuName, "meterName": meterName,
		"unitOfMeasure": "1 Hour", "reservationTerm": term, "type": "Reservation",
	}
}
