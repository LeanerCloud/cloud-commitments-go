package insurance

import (
	"context"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	recID       = "9aadf00d-ffff-4aba-8ada-abcdefabcdef"
	otherItem   = "eaadf00d-ffff-4aba-8ada-abcdefabcdef"
	vendorWords = "VENDOR-INTERNAL-DETAIL"
)

type quoteServer struct {
	mu       sync.Mutex
	paths    []string
	queries  []url.Values
	items    string
	compare  string
	pages    int
	failPath string
	status   int
	hang     bool
}

func (s *quoteServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.paths = append(s.paths, r.URL.Path)
	s.queries = append(s.queries, r.URL.Query())
	s.mu.Unlock()
	if s.hang {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		return
	}
	if s.failPath != "" && strings.HasSuffix(r.URL.Path, s.failPath) {
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(`{"message":"` + vendorWords + ` ` + testKey + `"}`))
		return
	}
	if strings.HasSuffix(r.URL.Path, "/line-items") {
		w.Header().Set("X-Pagination", paginationJSON(s.pages, 1, 1))
		_, _ = w.Write([]byte(s.items))
		return
	}
	_, _ = w.Write([]byte(s.compare))
}

func (s *quoteServer) calls(suffix string) []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []url.Values
	for i, p := range s.paths {
		if strings.HasSuffix(p, suffix) {
			out = append(out, s.queries[i])
		}
	}
	return out
}

func newQuoteServer(t *testing.T) (*quoteServer, *Client) {
	t.Helper()
	items, compare := contractMatchBodies()
	s := &quoteServer{items: items, compare: compare, pages: 1}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	c, err := newClient(srv.URL, Config{APIKey: NewSecret(testKey), OrgID: testOrg}, srv.Client())
	require.NoError(t, err)
	return s, c
}

// withSecondItem appends a second line item that is the first one with a new ID and an edit.
func withSecondItem(body, region string) string {
	second := strings.Replace(strings.TrimSuffix(strings.TrimPrefix(body, "["), "]"), matchItem, otherItem, 1)
	second = strings.Replace(second, `"region":"us-east-1"`, `"region":"`+region+`"`, 1)
	return strings.TrimSuffix(body, "]") + "," + second + "]"
}

func requireQuoteError(t *testing.T, err error, kind QuoteErrorKind, stage string) *QuoteError {
	t.Helper()
	var qe *QuoteError
	require.ErrorAs(t, err, &qe)
	require.Equal(t, kind, qe.Kind)
	require.Equal(t, stage, qe.Stage)
	return qe
}

func TestQuoteRecommendationExactMatchReturnsOneFigureSet(t *testing.T) {
	s, c := newQuoteServer(t)
	got, err := QuoteRecommendation(context.Background(), c, matchPlan, recID, contractRecommendation())
	require.NoError(t, err)
	require.Equal(t, QuoteStatusQuote, got.Status)
	require.Empty(t, got.Reason)
	require.NotNil(t, got.Comparison)
	require.Equal(t, recID, got.Comparison.RecommendationID)
	require.Equal(t, ResourceAssociated, got.Comparison.Status)
	require.Equal(t, matchOffer, got.Comparison.Current.OfferID)

	require.Len(t, s.calls("/line-items"), 1)
	cmp := s.calls("/comparison")
	require.Len(t, cmp, 1)
	assert.Equal(t, []string{matchItem}, cmp[0]["line_item_ids"])
	assert.ElementsMatch(t, []string{"no_upfront", "partial_upfront", "all_upfront"}, cmp[0]["payment_options"])
	assert.Empty(t, cmp[0]["contract_terms"])
}

func TestQuoteRecommendationNoLineItemIsUnavailableWithoutComparison(t *testing.T) {
	s, c := newQuoteServer(t)
	rec := contractRecommendation()
	rec.Region, rec.TopLevelRegion = "eu-west-1", "eu-west-1"
	got, err := QuoteRecommendation(context.Background(), c, matchPlan, recID, rec)
	require.NoError(t, err)
	require.Equal(t, QuoteStatusUnavailable, got.Status)
	require.Equal(t, ReasonIdentityUnproven, got.Reason)
	require.Nil(t, got.Comparison)
	require.Empty(t, s.calls("/comparison"))
}

func TestQuoteRecommendationTwoCandidatesAreAmbiguousWithoutComparison(t *testing.T) {
	s, c := newQuoteServer(t)
	s.items, s.pages = withSecondItem(s.items, "us-east-1"), 2
	got, err := QuoteRecommendation(context.Background(), c, matchPlan, recID, contractRecommendation())
	require.NoError(t, err)
	require.Equal(t, QuoteStatusAmbiguous, got.Status)
	require.Equal(t, ReasonAmbiguousLineItems, got.Reason)
	require.Nil(t, got.Comparison)
	require.Empty(t, s.calls("/comparison"))
}

func TestQuoteRecommendationIneligibleMakesNoVendorCall(t *testing.T) {
	for name, edit := range map[string]func(*RecommendationIdentity) *RecommendationIdentity{
		"nil":         func(*RecommendationIdentity) *RecommendationIdentity { return nil },
		"other cloud": func(r *RecommendationIdentity) *RecommendationIdentity { r.Provider = "azure"; return r },
		"other plan":  func(r *RecommendationIdentity) *RecommendationIdentity { r.PlanType = "Compute"; return r },
		"other term":  func(r *RecommendationIdentity) *RecommendationIdentity { r.NativeDurationSeconds = 1; return r },
	} {
		t.Run(name, func(t *testing.T) {
			s, c := newQuoteServer(t)
			got, err := QuoteRecommendation(context.Background(), c, matchPlan, recID, edit(contractRecommendation()))
			require.NoError(t, err)
			require.Equal(t, QuoteStatusIneligible, got.Status)
			require.Equal(t, ReasonIdentityUnproven, got.Reason)
			require.Nil(t, got.Comparison)
			require.Empty(t, s.paths)
		})
	}
}

func TestQuoteRecommendationComparisonForOtherLineItemsOnlyIsUnavailable(t *testing.T) {
	s, c := newQuoteServer(t)
	s.items, s.pages = withSecondItem(s.items, "eu-west-1"), 2
	s.compare = strings.Replace(s.compare, matchItem, otherItem, 1)
	got, err := QuoteRecommendation(context.Background(), c, matchPlan, recID, contractRecommendation())
	require.NoError(t, err)
	require.Equal(t, QuoteStatusUnavailable, got.Status)
	require.Equal(t, ReasonComparisonBasisUnproven, got.Reason)
	require.Nil(t, got.Comparison)
}

func TestQuoteRecommendationEmptyComparisonIsUnavailable(t *testing.T) {
	s, c := newQuoteServer(t)
	s.compare = `{"current_totals":{"commitment_financials_monthly_rate":{},"commitment_upfront_cost":0},"hypothetical_totals":[],"data":[]}`
	got, err := QuoteRecommendation(context.Background(), c, matchPlan, recID, contractRecommendation())
	require.NoError(t, err)
	require.Equal(t, QuoteStatusUnavailable, got.Status)
	require.Nil(t, got.Comparison)
}

func TestQuoteRecommendationInexactCommitmentNeverYieldsFigures(t *testing.T) {
	s, c := newQuoteServer(t)
	s.compare = strings.Replace(s.compare, `"selected_amount":0.50000000000000000001`, `"selected_amount":0.6`, 1)
	got, err := QuoteRecommendation(context.Background(), c, matchPlan, recID, contractRecommendation())
	require.NoError(t, err)
	require.Equal(t, QuoteStatusUnavailable, got.Status)
	require.Equal(t, ReasonComparisonBasisUnproven, got.Reason)
	require.Nil(t, got.Comparison)
}

func TestQuoteRecommendationVendorFailuresAreTypedAndCarryNoVendorText(t *testing.T) {
	cases := []struct {
		status int
		kind   QuoteErrorKind
	}{
		{400, QuoteErrRejected}, {401, QuoteErrRejected}, {404, QuoteErrRejected}, {429, QuoteErrRejected},
		{500, QuoteErrVendorUnavailable}, {503, QuoteErrVendorUnavailable},
	}
	for _, stage := range []string{"line_items", "comparison"} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/%d", stage, tc.status), func(t *testing.T) {
				s, c := newQuoteServer(t)
				s.failPath, s.status = "/"+strings.ReplaceAll(stage, "_", "-"), tc.status
				got, err := QuoteRecommendation(context.Background(), c, matchPlan, recID, contractRecommendation())
				require.Nil(t, got)
				qe := requireQuoteError(t, err, tc.kind, stage)
				assert.Equal(t, tc.status, qe.Status)
				for _, text := range []string{err.Error(), fmt.Sprintf("%v|%+v|%#v|%s", err, err, qe, qe)} {
					assert.NotContains(t, text, vendorWords)
					assert.NotContains(t, text, testKey)
					assert.NotContains(t, text, testPlan)
				}
				assert.Len(t, s.calls("/line-items"), 1, "no retry")
				if stage == "comparison" {
					assert.Len(t, s.calls("/comparison"), 1, "no retry")
				} else {
					assert.Empty(t, s.calls("/comparison"))
				}
			})
		}
	}
}

func TestQuoteRecommendationKeepsRetryAfterAsDuration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
	}))
	t.Cleanup(srv.Close)
	c, err := newClient(srv.URL, Config{APIKey: NewSecret(testKey), OrgID: testOrg}, srv.Client())
	require.NoError(t, err)
	_, err = QuoteRecommendation(context.Background(), c, matchPlan, recID, contractRecommendation())
	require.Equal(t, 7*time.Second, requireQuoteError(t, err, QuoteErrRejected, "line_items").RetryAfter)
}

func TestQuoteRecommendationTimeoutsAndCancellation(t *testing.T) {
	t.Run("context deadline", func(t *testing.T) {
		s, c := newQuoteServer(t)
		s.hang = true
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		got, err := QuoteRecommendation(ctx, c, matchPlan, recID, contractRecommendation())
		require.Nil(t, got)
		requireQuoteError(t, err, QuoteErrTimeout, "line_items")
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
	t.Run("http client timeout", func(t *testing.T) {
		s, _ := newQuoteServer(t)
		s.hang = true
		srv := httptest.NewServer(s)
		t.Cleanup(srv.Close)
		hc := *srv.Client()
		hc.Timeout = 50 * time.Millisecond
		c, err := newClient(srv.URL, Config{APIKey: NewSecret(testKey), OrgID: testOrg}, &hc)
		require.NoError(t, err)
		_, err = QuoteRecommendation(context.Background(), c, matchPlan, recID, contractRecommendation())
		requireQuoteError(t, err, QuoteErrTimeout, "line_items")
	})
	t.Run("canceled before start", func(t *testing.T) {
		s, c := newQuoteServer(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := QuoteRecommendation(ctx, c, matchPlan, recID, contractRecommendation())
		requireQuoteError(t, err, QuoteErrCanceled, "line_items")
		require.ErrorIs(t, err, context.Canceled)
		require.Empty(t, s.paths)
	})
	t.Run("canceled mid flight", func(t *testing.T) {
		s, c := newQuoteServer(t)
		s.hang = true
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(50 * time.Millisecond); cancel() }()
		_, err := QuoteRecommendation(ctx, c, matchPlan, recID, contractRecommendation())
		requireQuoteError(t, err, QuoteErrCanceled, "line_items")
		require.ErrorIs(t, err, context.Canceled)
	})
}

type fakeQuoteClient struct {
	items         []LineItem
	itemsErr      error
	cmp           *Comparison
	cmpErr        error
	onLineItems   func()
	comparisons   int
	lineItemReads int
}

func (f *fakeQuoteClient) LineItems(context.Context, string) ([]LineItem, error) {
	f.lineItemReads++
	if f.onLineItems != nil {
		f.onLineItems()
	}
	return f.items, f.itemsErr
}

func (f *fakeQuoteClient) Comparison(context.Context, ComparisonRequest) (*Comparison, error) {
	f.comparisons++
	return f.cmp, f.cmpErr
}

func (f *fakeQuoteClient) Plan(context.Context, string) (*Plan, error) { return nil, nil }

func TestQuoteRecommendationCancellationBetweenReadsSkipsComparison(t *testing.T) {
	cmp, items := contractMatchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeQuoteClient{items: items, cmp: cmp, onLineItems: cancel}
	_, err := QuoteRecommendation(ctx, f, matchPlan, recID, contractRecommendation())
	requireQuoteError(t, err, QuoteErrCanceled, "comparison")
	require.Zero(t, f.comparisons)
}

func TestQuoteRecommendationRejectsUnusableInputsWithoutCalls(t *testing.T) {
	cmp, items := contractMatchFixture(t)
	f := &fakeQuoteClient{items: items, cmp: cmp}
	_, err := QuoteRecommendation(context.Background(), f, "not-a-uuid", recID, contractRecommendation())
	requireQuoteError(t, err, QuoteErrInvalidRequest, "request")
	_, err = QuoteRecommendation(context.Background(), nil, matchPlan, recID, contractRecommendation())
	requireQuoteError(t, err, QuoteErrInvalidRequest, "request")
	var typedNil *fakeQuoteClient
	_, err = QuoteRecommendation(context.Background(), typedNil, matchPlan, recID, contractRecommendation())
	requireQuoteError(t, err, QuoteErrInvalidRequest, "request")
	require.Zero(t, f.comparisons)
}

func TestQuoteRecommendationMalformedVendorDataIsATypedErrorWithoutFigures(t *testing.T) {
	cmp, items := contractMatchFixture(t)
	dup := *cmp
	dup.Rows = append(append([]ComparisonRow{}, cmp.Rows...), cmp.Rows...)
	for name, tc := range map[string]struct {
		f     *fakeQuoteClient
		stage string
	}{
		"nil comparison":       {&fakeQuoteClient{items: items}, "comparison"},
		"duplicate row":        {&fakeQuoteClient{items: items, cmp: &dup}, "build"},
		"line items other err": {&fakeQuoteClient{itemsErr: fmt.Errorf("decode %s", vendorWords)}, "line_items"},
		"comparison other err": {&fakeQuoteClient{items: items, cmpErr: fmt.Errorf("decode %s", vendorWords)}, "comparison"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := QuoteRecommendation(context.Background(), tc.f, matchPlan, recID, contractRecommendation())
			require.Nil(t, got)
			qe := requireQuoteError(t, err, QuoteErrInvalidResponse, tc.stage)
			assert.Equal(t, "archera recommendation quote failed: invalid_response at "+tc.stage, qe.Error())
			assert.NotContains(t, err.Error(), vendorWords)
		})
	}
}

func TestQuoteFailureClassifiesContextFromEitherSource(t *testing.T) {
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tc := range map[string]struct {
		ctx  context.Context
		err  error
		kind QuoteErrorKind
	}{
		"live ctx, canceled error":  {context.Background(), context.Canceled, QuoteErrCanceled},
		"live ctx, deadline error":  {context.Background(), context.DeadlineExceeded, QuoteErrTimeout},
		"canceled ctx, other error": {canceled, fmt.Errorf("boom"), QuoteErrCanceled},
		"expired ctx, other error":  {expired, fmt.Errorf("boom"), QuoteErrTimeout},
		"live ctx, net timeout":     {context.Background(), &net.DNSError{IsTimeout: true}, QuoteErrTimeout},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.kind, quoteFailure(tc.ctx, tc.err, "line_items").Kind)
		})
	}
}

func TestQuoteRecommendationChecksContextBeforeAnyVendorRead(t *testing.T) {
	cmp, items := contractMatchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeQuoteClient{items: items, cmp: cmp}
	_, err := QuoteRecommendation(ctx, f, matchPlan, recID, contractRecommendation())
	requireQuoteError(t, err, QuoteErrCanceled, "line_items")
	require.Zero(t, f.lineItemReads)
	require.Zero(t, f.comparisons)
}

func TestQuoteRecommendationTransportFailureIsVendorUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	c, err := newClient(srv.URL, Config{APIKey: NewSecret(testKey), OrgID: testOrg}, srv.Client())
	require.NoError(t, err)
	srv.Close()
	got, err := QuoteRecommendation(context.Background(), c, matchPlan, recID, contractRecommendation())
	require.Nil(t, got)
	requireQuoteError(t, err, QuoteErrVendorUnavailable, "line_items")
	assert.NotContains(t, err.Error(), srv.Listener.Addr().String())

	f := &fakeQuoteClient{itemsErr: &net.DNSError{Err: vendorWords}}
	_, err = QuoteRecommendation(context.Background(), f, matchPlan, recID, contractRecommendation())
	requireQuoteError(t, err, QuoteErrVendorUnavailable, "line_items")
	assert.NotContains(t, err.Error(), vendorWords)
}

func TestQuoteRecommendationUnrepresentableValueHasItsOwnKind(t *testing.T) {
	cmp, items := contractMatchFixture(t)
	cmp.Rows[0].Current.UpfrontCost = big.NewRat(1, 3)
	got, err := QuoteRecommendation(context.Background(), &fakeQuoteClient{items: items, cmp: cmp}, matchPlan, recID, contractRecommendation())
	require.Nil(t, got)
	qe := requireQuoteError(t, err, QuoteErrUnrepresentable, "build")
	assert.Equal(t, "archera recommendation quote failed: unrepresentable_value at build", qe.Error())
}

func TestQuoteRecommendationRequiresUUIDRecommendationID(t *testing.T) {
	for _, id := range []string{"", "rec-1", recID + "0"} {
		s, c := newQuoteServer(t)
		got, err := QuoteRecommendation(context.Background(), c, matchPlan, id, contractRecommendation())
		require.Nil(t, got, id)
		requireQuoteError(t, err, QuoteErrInvalidRequest, "request")
		require.Empty(t, s.paths)
	}
}
