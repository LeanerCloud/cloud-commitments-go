package insurance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/httpclient"
)

const (
	testOrg  = "11111111-1111-4111-8111-111111111111"
	testPlan = "44444444-4444-4444-8444-444444444444"
	testLine = "22222222-2222-4222-8222-222222222222"
	testKey  = "sekret-key-value"
)

func testClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := newClient(srv.URL, Config{APIKey: testKey, OrgID: testOrg}, srv.Client())
	require.NoError(t, err)
	return c
}

func TestNewClient_ValidatesConfig(t *testing.T) {
	_, err := NewClient(Config{OrgID: testOrg}, nil)
	assert.Error(t, err)
	_, err = NewClient(Config{APIKey: testKey, OrgID: "../x"}, nil)
	assert.Error(t, err)
	c, err := NewClient(Config{APIKey: testKey, OrgID: testOrg}, nil)
	require.NoError(t, err)
	assert.Equal(t, "https://api.archera.ai", c.baseURL)
}

func TestClient_Comparison_RequestAndDecode(t *testing.T) {
	var gotPath, gotQuery, gotKey, gotMethod string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotKey, gotMethod = r.URL.Path, r.URL.RawQuery, r.Header.Get("x-api-key"), r.Method
		_, _ = w.Write([]byte(validComparisonJSON()))
	}))
	got, err := c.Comparison(context.Background(), ComparisonRequest{
		PlanID:         testPlan,
		LineItemIDs:    []string{testLine, testOrg},
		ContractTerms:  []string{"one_year_gris", "three_year"},
		PaymentOptions: []PaymentOption{PaymentNoUpfront, PaymentAllUpfront},
	})
	require.NoError(t, err)
	assert.Equal(t, http.MethodGet, gotMethod)
	assert.Equal(t, "/v1/org/"+testOrg+"/commitment-plans/"+testPlan+"/comparison", gotPath)
	assert.Equal(t, "line_item_ids="+testLine+"&line_item_ids="+testOrg+
		"&payment_options=no_upfront&payment_options=all_upfront"+
		"&contract_terms=one_year_gris&contract_terms=three_year", reorder(gotQuery))
	assert.Equal(t, testKey, gotKey)
	assert.Equal(t, testOrg, got.OrgID)
	assert.Equal(t, testPlan, got.PlanID)
	assert.WithinDuration(t, time.Now(), got.FetchedAt, time.Minute)
	assert.Len(t, got.Rows, 1)
}

// reorder puts the query in the fixed order line_item_ids, payment_options,
// contract_terms, keeping the order within each key, because url.Values.Encode
// sorts by key.
func reorder(raw string) string {
	var li, po, ct []string
	for _, kv := range strings.Split(raw, "&") {
		switch {
		case strings.HasPrefix(kv, "line_item_ids="):
			li = append(li, kv)
		case strings.HasPrefix(kv, "payment_options="):
			po = append(po, kv)
		case strings.HasPrefix(kv, "contract_terms="):
			ct = append(ct, kv)
		}
	}
	return strings.Join(append(append(li, po...), ct...), "&")
}

func TestClient_Comparison_NoQueryByDefault(t *testing.T) {
	var gotQuery string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(validComparisonJSON()))
	}))
	_, err := c.Comparison(context.Background(), ComparisonRequest{PlanID: testPlan})
	require.NoError(t, err)
	assert.Empty(t, gotQuery)
}

func TestClient_RejectsBadInputsWithoutSending(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	ctx := context.Background()
	bad := []ComparisonRequest{
		{PlanID: "../../etc"},
		{PlanID: testPlan + "/x"},
		{PlanID: testPlan, LineItemIDs: []string{"a&b=c"}},
		{PlanID: testPlan, ContractTerms: []string{"forever"}},
		{PlanID: testPlan, PaymentOptions: []PaymentOption{"No Upfront"}},
	}
	for _, r := range bad {
		_, err := c.Comparison(ctx, r)
		assert.Error(t, err, "%+v", r)
	}
	_, err := c.Plan(ctx, "not-a-uuid")
	assert.Error(t, err)
	assert.Zero(t, calls.Load())
}

func TestClient_Plan(t *testing.T) {
	var gotPath string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(validPlanJSON))
	}))
	p, err := c.Plan(context.Background(), testPlan)
	require.NoError(t, err)
	assert.Equal(t, "/v1/org/"+testOrg+"/commitment-plans/"+testPlan, gotPath)
	assert.Equal(t, PlanStatusReviewed, p.Status)
}

func TestClient_DoesNotFollowRedirectsOrLeakKey(t *testing.T) {
	var otherHits atomic.Int32
	var leaked atomic.Value
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		otherHits.Add(1)
		leaked.Store(r.Header.Get("x-api-key"))
	}))
	t.Cleanup(other.Close)
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	_, err := c.Plan(context.Background(), testPlan)
	var he *HTTPError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, http.StatusFound, he.StatusCode)
	assert.Zero(t, otherHits.Load(), "redirect target must never be contacted")
	assert.Nil(t, leaked.Load())
}

func TestClient_DoesNotMutateCallerHTTPClient(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	hc := srv.Client()
	_, err := newClient(srv.URL, Config{APIKey: testKey, OrgID: testOrg}, hc)
	require.NoError(t, err)
	assert.Nil(t, hc.CheckRedirect)
}

func TestClient_ErrorMapping(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		header  string
		body    string
		message string
		retry   time.Duration
	}{
		{"400 no body", 400, "", "", "", 0},
		{"404 no body", 404, "", "", "", 0},
		{"401 ApiErrorResponse", 401, "", `{"message":"bad key","timestamp":"t","type":"x"}`, "bad key", 0},
		{"409 ApiErrorResponse", 409, "", `{"message":"conflict","timestamp":"t","type":"x"}`, "conflict", 0},
		{"422 Error", 422, "", `{"code":422,"status":"Unprocessable","message":"nope"}`, "nope", 0},
		{"429 retry-after", 429, "17", `{"message":"slow down"}`, "slow down", 17 * time.Second},
		{"500 html body is not echoed", 500, "", `<html>` + testKey + `</html>`, "", 0},
		{"500 long message truncated", 500, "", `{"message":"` + strings.Repeat("x", 500) + `"}`,
			strings.Repeat("x", maxErrorMessageLen) + "...", 0},
		{"retry-after 1s", 503, "1", `{}`, "", time.Second},
		{"retry-after zero ignored", 503, "0", `{}`, "", 0},
		{"retry-after huge is capped", 503, "99999999999", `{}`, "", 24 * time.Hour},
		{"retry-after wraps negative if multiplied", 503, "9223372037", `{}`, "", 24 * time.Hour},
		{"retry-after wraps tiny if multiplied", 503, "18446744074", `{}`, "", 24 * time.Hour},
		{"retry-after exactly the cap", 503, "86400", `{}`, "", 24 * time.Hour},
		{"retry-after negative ignored", 503, "-5", `{}`, "", 0},
		{"retry-after date ignored", 503, "Wed, 21 Oct 2026 07:28:00 GMT", `{}`, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			_, err := c.Plan(context.Background(), testPlan)
			var he *HTTPError
			require.ErrorAs(t, err, &he)
			assert.Equal(t, tc.status, he.StatusCode)
			assert.Equal(t, tc.message, he.Message)
			assert.Equal(t, tc.retry, he.RetryAfter)
			assert.NotContains(t, err.Error(), testKey)
		})
	}
}

func TestClient_SingleShotNoRetry(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	_, err := c.Plan(context.Background(), testPlan)
	require.Error(t, err)
	assert.EqualValues(t, 1, calls.Load())
}

func oversizePlanJSON() string {
	return strings.Replace(validPlanJSON, `"name": "p"`, `"name": "`+strings.Repeat("a", maxResponseBytes)+`"`, 1)
}

func oversizeComparisonJSON() string {
	return strings.Replace(validComparisonJSON(), `"data":`, `"padding":"`+strings.Repeat("a", maxResponseBytes)+`","data":`, 1)
}

func TestClient_BoundedBody(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/comparison") {
			_, _ = w.Write([]byte(oversizeComparisonJSON()))
			return
		}
		_, _ = w.Write([]byte(oversizePlanJSON()))
	}))
	p, err := c.Plan(context.Background(), testPlan)
	assert.ErrorIs(t, err, errResponseTooLarge)
	assert.Nil(t, p)
	cmp, err := c.Comparison(context.Background(), ComparisonRequest{PlanID: testPlan})
	assert.ErrorIs(t, err, errResponseTooLarge)
	assert.Nil(t, cmp)
}

func TestLimitsArePinned(t *testing.T) {
	assert.EqualValues(t, 8<<20, maxResponseBytes)
	assert.Equal(t, 24*time.Hour, maxRetryAfter)
}

// padTo pads a valid JSON body with trailing whitespace to exactly n bytes.
func padTo(body string, n int) string { return body + strings.Repeat(" ", n-len(body)) }

func TestClient_BodyExactlyAtAndOverTheLimit(t *testing.T) {
	var comparison bool
	var size int
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := validPlanJSON
		if comparison {
			body = validComparisonJSON()
		}
		_, _ = w.Write([]byte(padTo(body, size)))
	}))
	for _, cmp := range []bool{false, true} {
		comparison = cmp
		call := func() error {
			if cmp {
				_, err := c.Comparison(context.Background(), ComparisonRequest{PlanID: testPlan})
				return err
			}
			_, err := c.Plan(context.Background(), testPlan)
			return err
		}
		size = maxResponseBytes
		assert.NoError(t, call(), "exactly the limit, comparison=%v", cmp)
		size = maxResponseBytes + 1
		assert.ErrorIs(t, call(), errResponseTooLarge, "one byte over, comparison=%v", cmp)
	}
}

func TestBoundedReader_AllowsExactlyTheLimit(t *testing.T) {
	// OneByteReader makes the reader sit at n==1 and n==0 between reads, as
	// chunked HTTP bodies do.
	b := &boundedReader{r: iotest.OneByteReader(strings.NewReader("abcd")), n: 4}
	got, err := io.ReadAll(b)
	require.NoError(t, err)
	assert.Equal(t, "abcd", string(got))
	b = &boundedReader{r: iotest.OneByteReader(strings.NewReader("abcde")), n: 4}
	_, err = io.ReadAll(b)
	assert.ErrorIs(t, err, errResponseTooLarge)
}

// A complete JSON value under the limit followed by whitespace past it
// overflows after Decode, in decodeOne's trailing-data check.
func TestDecodeOne_TooLargeAfterValue(t *testing.T) {
	r := &boundedReader{r: strings.NewReader(`{"a":1}   `), n: 8}
	var v map[string]any
	err := decodeOne(r, &v)
	assert.ErrorIs(t, err, errResponseTooLarge)
	assert.Equal(t, map[string]any{"a": float64(1)}, v)

	r = &boundedReader{r: strings.NewReader(`{"a":1} {}`), n: 100}
	err = decodeOne(r, &v)
	require.Error(t, err)
	assert.NotErrorIs(t, err, errResponseTooLarge, "trailing data under the limit keeps the generic error")
}

type holder struct {
	Cfg  Config
	Cli  *Client
	Any  any
	List []Config
	Map  map[string]Config
}

func TestConfigAndClient_NeverFormatTheKey(t *testing.T) {
	cfg := Config{APIKey: testKey, OrgID: testOrg}
	c, err := newClient("http://127.0.0.1:1", cfg, nil)
	require.NoError(t, err)
	values := []any{cfg, &cfg, c, *c, holder{cfg, c, cfg, []Config{cfg}, map[string]Config{"k": cfg}},
		&holder{Cfg: cfg}, []any{cfg, c}, fmt.Errorf("wrapped: %v", cfg)}
	verbs := []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%b", "%c", "%e", "%f", "%g", "%o", "%t", "%U", "%10v", "%-20s"}
	for _, v := range values {
		for _, verb := range verbs {
			assert.NotContains(t, fmt.Sprintf(verb, v), testKey, "%s of %T", verb, v)
		}
		assert.NotContains(t, fmt.Sprint(v), testKey, "Sprint of %T", v)
		assert.NotContains(t, fmt.Sprintln(v), testKey, "Sprintln of %T", v)
	}
	// %p is meaningful only on pointers, where it prints an address.
	for _, v := range []any{&cfg, c, &holder{Cfg: cfg}} {
		assert.NotContains(t, fmt.Sprintf("%p", v), testKey, "%%p of %T", v)
	}
	b, err := json.Marshal(holder{Cfg: cfg})
	require.NoError(t, err)
	assert.NotContains(t, string(b), testKey)
	var buf strings.Builder
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "cfg", cfg, "client", c)
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "cfg", cfg, "client", c)
	assert.NotContains(t, buf.String(), testKey)
}

func TestNewClient_DefaultHTTPClientIsHardened(t *testing.T) {
	c, err := NewClient(Config{APIKey: testKey, OrgID: testOrg}, nil)
	require.NoError(t, err)
	assert.Equal(t, httpclient.New().Timeout, c.hc.Timeout)
	assert.Equal(t, 30*time.Second, c.hc.Timeout)
	assert.NotNil(t, c.hc.Transport, "must not fall back to http.DefaultTransport")
	assert.NotSame(t, http.DefaultTransport, c.hc.Transport)
}

func TestNewClient_RejectsBlankKey(t *testing.T) {
	_, err := NewClient(Config{APIKey: "   \t", OrgID: testOrg}, nil)
	assert.Error(t, err)
}

func TestClient_RejectsExtremeExponentFromServer(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Replace(validPlanJSON, `"fee_hourly": 0.25`, `"fee_hourly": 1e999999`, 1)))
	}))
	p, err := c.Plan(context.Background(), testPlan)
	assert.Error(t, err)
	assert.Nil(t, p)
}

func TestClient_ContextCancellation(t *testing.T) {
	// The handler gives up after 5s so a client that ignores the context fails
	// the test instead of hanging it.
	c := testClient(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Plan(ctx, testPlan)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
	assert.NotContains(t, err.Error(), testPlan, "request URL is stripped from transport errors")
}

func TestIsUUID(t *testing.T) {
	assert.True(t, isUUID(testOrg))
	assert.True(t, isUUID("ABCDEF01-2345-4678-89AB-CDEF01234567"))
	for _, s := range []string{"", "x", testOrg + "0", strings.Replace(testOrg, "-", "_", 1),
		strings.Replace(testOrg, "1", "g", 1), "1111111111111111111111111111111111111"} {
		assert.False(t, isUUID(s), s)
	}
}
