package computeengine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/option"
)

// catalogServer serves total synthetic SKUs in pages of pageSize and records the
// page tokens it was asked for.
func catalogServer(t *testing.T, total, pageSize int, tokens *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("pageToken")
		*tokens = append(*tokens, token)
		start := 0
		if token != "" {
			start, _ = strconv.Atoi(token)
		}
		end := min(start+pageSize, total)
		var body strings.Builder
		body.WriteString(`{"skus":[`)
		for i := start; i < end; i++ {
			if i > start {
				body.WriteString(",")
			}
			fmt.Fprintf(&body, `{"skuId":"sku-%d"}`, i)
		}
		body.WriteString("]")
		if end < total {
			fmt.Fprintf(&body, `,"nextPageToken":"%d"`, end)
		}
		body.WriteString("}")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body.String()))
	}))
}

func billingAgainst(t *testing.T, srv *httptest.Server) *realBillingService {
	t.Helper()
	api, err := cloudbilling.NewService(context.Background(), option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	require.NoError(t, err)
	return &realBillingService{ctx: context.Background(), service: api}
}

// The live Compute Engine catalog is 33,453 SKUs over 7 pages of 5000; reading
// only the first page left the commitment SKUs unseen.
func TestRealBillingService_ListSKUsFollowsEveryPage(t *testing.T) {
	t.Parallel()
	var tokens []string
	srv := catalogServer(t, 33453, 5000, &tokens)
	defer srv.Close()

	resp, err := billingAgainst(t, srv).ListSKUs("services/6F81-5844-456A")
	require.NoError(t, err)
	assert.Len(t, resp.Skus, 33453)
	assert.Len(t, tokens, 7)
}
