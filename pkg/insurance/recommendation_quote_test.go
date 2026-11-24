package insurance

import (
	"context"
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

func TestQuoteRecommendationExactMatchReturnsOneFigureSet(t *testing.T) {
	s, c := newQuoteServer(t)
	got, err := QuoteRecommendation(context.Background(), c, matchPlan, "rec-1", contractRecommendation())
	require.NoError(t, err)
	require.Equal(t, QuoteStatusQuote, got.Status)
	require.Empty(t, got.Reason)
	require.NotNil(t, got.Comparison)
	require.Equal(t, "rec-1", got.Comparison.RecommendationID)
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
	got, err := QuoteRecommendation(context.Background(), c, matchPlan, "rec-1", rec)
	require.NoError(t, err)
	require.Equal(t, QuoteStatusUnavailable, got.Status)
	require.Equal(t, ReasonIdentityUnproven, got.Reason)
	require.Nil(t, got.Comparison)
	require.Empty(t, s.calls("/comparison"))
}

func TestQuoteRecommendationTwoCandidatesAreAmbiguousWithoutComparison(t *testing.T) {
	s, c := newQuoteServer(t)
	s.items, s.pages = withSecondItem(s.items, "us-east-1"), 2
	got, err := QuoteRecommendation(context.Background(), c, matchPlan, "rec-1", contractRecommendation())
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
			got, err := QuoteRecommendation(context.Background(), c, matchPlan, "rec-1", edit(contractRecommendation()))
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
	got, err := QuoteRecommendation(context.Background(), c, matchPlan, "rec-1", contractRecommendation())
	require.NoError(t, err)
	require.Equal(t, QuoteStatusUnavailable, got.Status)
	require.Equal(t, ReasonComparisonBasisUnproven, got.Reason)
	require.Nil(t, got.Comparison)
}

func TestQuoteRecommendationEmptyComparisonIsUnavailable(t *testing.T) {
	s, c := newQuoteServer(t)
	s.compare = `{"current_totals":{"commitment_financials_monthly_rate":{},"commitment_upfront_cost":0},"hypothetical_totals":[],"data":[]}`
	got, err := QuoteRecommendation(context.Background(), c, matchPlan, "rec-1", contractRecommendation())
	require.NoError(t, err)
	require.Equal(t, QuoteStatusUnavailable, got.Status)
	require.Nil(t, got.Comparison)
}

func TestQuoteRecommendationInexactCommitmentNeverYieldsFigures(t *testing.T) {
	s, c := newQuoteServer(t)
	s.compare = strings.Replace(s.compare, `"selected_amount":0.50000000000000000001`, `"selected_amount":0.6`, 1)
	got, err := QuoteRecommendation(context.Background(), c, matchPlan, "rec-1", contractRecommendation())
	require.NoError(t, err)
	require.Equal(t, QuoteStatusUnavailable, got.Status)
	require.Equal(t, ReasonComparisonBasisUnproven, got.Reason)
	require.Nil(t, got.Comparison)
}
