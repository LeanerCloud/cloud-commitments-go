package reservations

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// currencyClient answers calculatePrice with a fixed body and records whether
// a purchase PUT/POST was attempted.
type currencyClient struct {
	calcBody  string
	purchased int
}

func (c *currencyClient) Do(req *http.Request) (*http.Response, error) {
	if strings.HasSuffix(req.URL.Path, "/calculatePrice") {
		return fakeResp(http.StatusOK, c.calcBody), nil
	}
	c.purchased++
	return fakeResp(http.StatusOK, `{}`), nil
}

func calcWithCurrency(code string) string {
	if code == "" {
		return `{"properties":{"reservationOrderId":"ord"}}`
	}
	return `{"properties":{"billingCurrencyTotal":{"currencyCode":"` + code + `"},"reservationOrderId":"ord"}}`
}

func calcWithCurrencyAndOrder(code, order string) string {
	return `{"properties":{"billingCurrencyTotal":{"currencyCode":"` + code + `"},"reservationOrderId":"` + order + `"}}`
}

func TestPurchaseChecksBillingCurrency(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		rec     string
		billing string
		wantErr string
	}{
		{name: "USD rec and USD billing buys", rec: "USD", billing: "USD"},
		{name: "empty rec and USD billing buys", rec: "", billing: "USD"},
		{name: "case-insensitive match buys", rec: "usd", billing: "USD"},
		{name: "matching non-USD buys", rec: "EUR", billing: "EUR"},
		{name: "USD rec and EUR billing refuses", rec: "USD", billing: "EUR", wantErr: "bills in EUR"},
		{name: "empty rec and EUR billing refuses", rec: "", billing: "EUR", wantErr: "bills in EUR"},
		{name: "missing billing currency refuses", rec: "USD", billing: "", wantErr: "no billing currency"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &currencyClient{calcBody: calcWithCurrency(tt.billing)}
			id, _, err := DoIdempotentPurchaseTwoStep(context.Background(), c, calcURL, []byte(testBody), "tok", "", tt.rec)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Zero(t, c.purchased, "no purchase call after a currency refusal")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "ord", id)
			assert.Equal(t, 1, c.purchased)
		})
	}
}
