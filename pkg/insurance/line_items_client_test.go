package insurance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lineItemsPage(start, count int) string {
	rows := make([]string, count)
	for i := range rows {
		rows[i] = strings.Replace(lineItemJSON(), "22222222-2222-4222-8222-222222222222", fmt.Sprintf("%08d-2222-4222-8222-222222222222", start+i), 1)
	}
	return "[" + strings.Join(rows, ",") + "]"
}

type lineItemsTransport func(*http.Request) (*http.Response, error)

func (f lineItemsTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type lineItemsBody struct {
	io.Reader
	closed bool
}

func (b *lineItemsBody) Close() error { b.closed = true; return nil }

func TestClientLineItemsClosesBodies(t *testing.T) {
	t.Parallel()
	for _, header := range []string{paginationJSON(1, 1, 1), "", paginationJSON(2, 1, 1)} {
		body := &lineItemsBody{Reader: strings.NewReader(lineItemsPage(1, 1))}
		c, err := newClient(BaseURL, Config{APIKey: NewSecret(testKey), OrgID: testOrg}, &http.Client{Transport: lineItemsTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"X-Pagination": []string{header}}, Body: body}, nil
		})})
		require.NoError(t, err)
		_, _ = c.LineItems(context.Background(), testPlan)
		assert.True(t, body.closed)
	}
}

func TestClientLineItemsSecondPageTransportAndCancellation(t *testing.T) {
	t.Parallel()
	for _, failure := range []error{errors.New("connection reset"), context.Canceled} {
		requests := 0
		body := &lineItemsBody{Reader: strings.NewReader(lineItemsPage(1, 100))}
		c, err := newClient(BaseURL, Config{APIKey: NewSecret(testKey), OrgID: testOrg}, &http.Client{Transport: lineItemsTransport(func(*http.Request) (*http.Response, error) {
			requests++
			if requests == 2 {
				return nil, failure
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"X-Pagination": []string{paginationJSON(101, 2, 1)}}, Body: body}, nil
		})})
		require.NoError(t, err)
		rows, err := c.LineItems(context.Background(), testPlan)
		require.ErrorIs(t, err, failure)
		assert.Nil(t, rows)
		assert.True(t, body.closed)
		assert.Equal(t, 2, requests)
		assert.NotContains(t, err.Error(), testPlan)
	}
}

func TestClientLineItemsRejectsCaseEquivalentDuplicatesAcrossPages(t *testing.T) {
	t.Parallel()
	const id = "abcdef03-2345-4678-89ab-cdef01234567"
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		w.Header().Set("X-Pagination", paginationJSON(101, 2, page))
		if page == 1 {
			_, _ = w.Write([]byte(strings.Replace(lineItemsPage(1, 100), "00000001-2222-4222-8222-222222222222", id, 1)))
			return
		}
		_, _ = w.Write([]byte(strings.Replace(lineItemsPage(101, 1), "00000101-2222-4222-8222-222222222222", strings.ToUpper(id), 1)))
	}))
	rows, err := c.LineItems(context.Background(), testPlan)
	require.ErrorContains(t, err, "duplicate")
	assert.Nil(t, rows)
}

func paginationJSON(total, pages, page int) string {
	return fmt.Sprintf(`{"total":%d,"total_pages":%d,"page":%d,"first_page":1,"last_page":%d,"next_page":%d,"previous_page":%d}`, total, pages, page, pages, page+1, page-1)
}

func TestClientLineItemsPagination(t *testing.T) {
	t.Parallel()
	requests := 0
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal(t, "/v1/org/"+testOrg+"/commitment-plans/"+testPlan+"/line-items", r.URL.Path)
		assert.Equal(t, testKey, r.Header.Get("x-api-key"))
		assert.Equal(t, "100", r.URL.Query().Get("page_size"))
		assert.Equal(t, "id", r.URL.Query().Get("order_by"))
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		require.NoError(t, err)
		w.Header().Set("X-Pagination", paginationJSON(101, 2, page))
		if page == 1 {
			_, _ = w.Write([]byte(lineItemsPage(1, 100)))
			return
		}
		_, _ = w.Write([]byte(lineItemsPage(101, 1)))
	}))
	rows, err := c.LineItems(context.Background(), testPlan)
	require.NoError(t, err)
	assert.Len(t, rows, 101)
	assert.Equal(t, 2, requests)
}

func TestClientLineItemsEmpty(t *testing.T) {
	t.Parallel()
	for _, pages := range []int{0, 1} {
		c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Pagination", paginationJSON(0, pages, 1))
			_, _ = w.Write([]byte(`[]`))
		}))
		rows, err := c.LineItems(context.Background(), testPlan)
		require.NoError(t, err)
		assert.NotNil(t, rows)
		assert.Empty(t, rows)
	}
}

func TestClientLineItemsRejectsIncompletePages(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ header, body string }{
		"missing header":          {"", "[]"},
		"null header":             {"null", "[]"},
		"missing total":           {`{"total_pages":0,"page":1,"first_page":1,"last_page":0}`, "[]"},
		"null total":              {`{"total":null,"total_pages":0,"page":1,"first_page":1,"last_page":0}`, "[]"},
		"wrong page":              {paginationJSON(1, 1, 2), lineItemsPage(1, 1)},
		"bad pages":               {paginationJSON(101, 1, 1), lineItemsPage(1, 100)},
		"short page":              {paginationJSON(101, 2, 1), lineItemsPage(1, 99)},
		"empty middle":            {paginationJSON(201, 3, 1), "[]"},
		"terminal missing row":    {paginationJSON(1, 1, 1), "[]"},
		"terminal unexpected row": {paginationJSON(0, 0, 1), lineItemsPage(1, 1)},
		"terminal excess row":     {paginationJSON(1, 1, 1), lineItemsPage(1, 2)},
		"overflow":                {`{"total":9223372036854775808,"total_pages":1,"page":1,"first_page":1,"last_page":1}`, "[]"},
		"max int":                 {`{"total":9223372036854775807,"total_pages":1,"page":1,"first_page":1,"last_page":1}`, "[]"},
		"bad next":                {strings.Replace(paginationJSON(101, 2, 1), `"next_page":2`, `"next_page":3`, 1), lineItemsPage(1, 100)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			requests := 0
			c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				w.Header().Set("X-Pagination", tc.header)
				_, _ = w.Write([]byte(tc.body))
			}))
			rows, err := c.LineItems(context.Background(), testPlan)
			require.Error(t, err)
			assert.Nil(t, rows)
			assert.Equal(t, 1, requests)
		})
	}
}

func TestClientLineItemsSecondPageFailure(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"HTTP", "total", "duplicate", "size", "previous", "decode"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				w.Header().Set("X-Pagination", paginationJSON(101, 2, page))
				if page == 1 {
					_, _ = w.Write([]byte(lineItemsPage(1, 100)))
					return
				}
				switch failure {
				case "HTTP":
					w.WriteHeader(500)
					_, _ = w.Write([]byte(`{"message":"` + testKey + `"}`))
				case "total":
					w.Header().Set("X-Pagination", paginationJSON(102, 2, 2))
					_, _ = w.Write([]byte(lineItemsPage(101, 2)))
				case "duplicate":
					_, _ = w.Write([]byte(lineItemsPage(1, 1)))
				case "size":
					firstSize := len(lineItemsPage(1, 100))
					second := lineItemsPage(101, 1)
					second += strings.Repeat(" ", maxResponseBytes-firstSize/2-len(second))
					assert.Less(t, firstSize, maxResponseBytes)
					assert.Less(t, len(second), maxResponseBytes)
					assert.Greater(t, firstSize+len(second), maxResponseBytes)
					_, _ = w.Write([]byte(second))
				case "previous":
					w.Header().Set("X-Pagination", strings.Replace(paginationJSON(101, 2, 2), `"previous_page":1`, `"previous_page":0`, 1))
					_, _ = w.Write([]byte(lineItemsPage(101, 1)))
				case "decode":
					_, _ = w.Write([]byte(strings.Replace(lineItemsPage(101, 1), `"provider":"aws"`, `"provider":"`+testKey+`"`, 1)))
				}
			}))
			rows, err := c.LineItems(context.Background(), testPlan)
			require.Error(t, err)
			assert.Nil(t, rows)
			assert.NotContains(t, err.Error(), testKey)
		})
	}
}

func TestClientLineItemsPaginationGuards(t *testing.T) {
	t.Parallel()
	hdr := func(total, totalPages, page, first, last int, next string) string {
		extra := ""
		if next != "" {
			extra = `,"next_page":` + next
		}
		return fmt.Sprintf(`{"total":%d,"total_pages":%d,"page":%d,"first_page":%d,"last_page":%d%s}`, total, totalPages, page, first, last, extra)
	}
	cases := map[string]struct{ header, body, want string }{
		"empty with huge total_pages":  {hdr(0, 1000000, 1, 1, 1000000, ""), "[]", "invalid empty pagination"},
		"total pages disagree":         {hdr(250, 2, 1, 1, 2, "2"), lineItemsPage(1, 100), "inconsistent page totals"},
		"total pages alone disagrees":  {hdr(250, 2, 1, 1, 3, "2"), lineItemsPage(1, 100), "inconsistent page totals"},
		"last page disagrees":          {hdr(101, 2, 1, 1, 3, "2"), lineItemsPage(1, 100), "inconsistent page totals"},
		"response page not requested":  {hdr(201, 3, 2, 1, 3, "3"), lineItemsPage(1, 100), "invalid total or page counters"},
		"first page not one":           {hdr(1, 1, 1, 0, 1, ""), lineItemsPage(1, 1), "invalid total or page counters"},
		"negative total":               {hdr(-1, 1, 1, 1, 1, ""), "[]", "invalid total or page counters"},
		"null next page on first page": {hdr(101, 2, 1, 1, 2, "null"), lineItemsPage(1, 100), "next_page"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			requests := 0
			c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				w.Header().Set("X-Pagination", tc.header)
				_, _ = w.Write([]byte(tc.body))
			}))
			rows, err := c.LineItems(context.Background(), testPlan)
			require.ErrorContains(t, err, tc.want)
			assert.Nil(t, rows)
			assert.Equal(t, 1, requests)
		})
	}
}

func TestClientLineItemsRejectsNonUUIDPlanBeforeRequest(t *testing.T) {
	t.Parallel()
	requests := 0
	c := testClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	rows, err := c.LineItems(context.Background(), "../other/plan")
	require.ErrorContains(t, err, "UUID")
	assert.Nil(t, rows)
	assert.Zero(t, requests)
}
