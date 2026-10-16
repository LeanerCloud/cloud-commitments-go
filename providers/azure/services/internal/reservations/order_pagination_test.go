package reservations

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// pageHTTPClient serves reservation-order list pages from next(url, call),
// failing the test after maxCalls so a regression cannot hang the suite.
type pageHTTPClient struct {
	calls    int
	maxCalls int
	next     func(url string, call int) string // returns the nextLink for url
}

func (c *pageHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.calls++
	if c.calls > c.maxCalls {
		return nil, fmt.Errorf("mock ceiling: more than %d calls, pagination is unbounded", c.maxCalls)
	}
	body := fmt.Sprintf(`{"value":[{"name":"other","properties":{"provisioningState":"Succeeded"}}],"nextLink":%q}`,
		c.next(req.URL.String(), c.calls))
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestFindReservationOrderByIdempotencyToken_NextLinkCycleErrors(t *testing.T) {
	const a, b = "https://example.test/a", "https://example.test/b"
	c := &pageHTTPClient{maxCalls: 500}
	// First page is the real list URL, which links to a; a<->b then cycles.
	first := true
	c.next = func(url string, _ int) string {
		if first {
			first = false
			return a
		}
		if url == a {
			return b
		}
		return a
	}
	_, found, err := FindReservationOrderByIdempotencyToken(context.Background(), c, "bearer", "tok")
	require.Error(t, err)
	assert.False(t, found)
	assert.Contains(t, err.Error(), "self-referential nextLink")
	assert.Less(t, c.calls, 10)
}

func TestFindReservationOrderByIdempotencyToken_EndlessDistinctNextLinkHitsCap(t *testing.T) {
	c := &pageHTTPClient{maxCalls: 500, next: func(_ string, call int) string {
		return fmt.Sprintf("https://example.test/page-%d", call)
	}}
	_, found, err := FindReservationOrderByIdempotencyToken(context.Background(), c, "bearer", "tok")
	require.Error(t, err)
	assert.False(t, found)
	assert.Contains(t, err.Error(), fmt.Sprintf("exceeds the %d-page cap", 50))
	assert.Equal(t, 50, c.calls)
}

func TestFindReservationOrderByIdempotencyToken_TwoPageWalkFindsOrder(t *testing.T) {
	calls := 0
	c := clientFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		var body string
		switch calls {
		case 1:
			body = `{"value":[{"name":"x","properties":{"provisioningState":"Succeeded"}}],"nextLink":"https://example.test/p2"}`
		case 2:
			body = `{"value":[{"name":"order-2","tags":{"` + common.IdempotencyTagKey + `":"tok"},"properties":{"provisioningState":"Succeeded"}}]}`
		default:
			return nil, fmt.Errorf("unexpected call %d", calls)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	orderID, found, err := FindReservationOrderByIdempotencyToken(context.Background(), c, "bearer", "tok")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "order-2", orderID)
	assert.Equal(t, 2, calls, "both pages must be walked")
}

type clientFunc func(*http.Request) (*http.Response, error)

func (f clientFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }
