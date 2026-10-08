package insurance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/httpclient"
)

// BaseURL is the fixed Archera API origin. Callers cannot override it; tests
// use the unexported newClient with an httptest origin.
const BaseURL = "https://api.archera.ai"

// maxResponseBytes bounds every response body read.
const maxResponseBytes = 8 << 20

// maxRetryAfter caps a vendor Retry-After so a hostile value cannot overflow
// time.Duration or park a caller for days.
const maxRetryAfter = 24 * time.Hour

// maxErrorMessageLen bounds the vendor message copied into an HTTPError.
const maxErrorMessageLen = 200

// Config identifies the vendor organization and credential. APIKey is sent
// only as the x-api-key header. Format redacts it for every fmt verb except %p
// and %T on a non-pointer Config, which fmt handles before consulting Format;
// never pass a Config value to %p. fmt also cannot call methods on unexported
// struct fields, so a consumer must hold a *Client, or a Config only in an
// exported field, and never log a Config stored in an unexported field.
type Config struct {
	APIKey string `json:"-"`
	OrgID  string
}

// Format redacts the key for fmt verbs, so %v, %+v, %#v and %s of a Config (or
// a pointer to one) never print it.
func (c Config) Format(s fmt.State, _ rune) {
	_, _ = fmt.Fprintf(s, "insurance.Config{OrgID:%q, APIKey:[redacted]}", c.OrgID)
}

// HTTPError is a non-2xx vendor response. Only the status, the Retry-After
// header and a truncated message from the documented error shapes are kept;
// the raw body is never echoed.
type HTTPError struct {
	StatusCode int
	// RetryAfter is the parsed Retry-After header, zero when absent or not a
	// number of seconds. The client never retries on its own.
	RetryAfter time.Duration
	Message    string
}

func (e *HTTPError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("archera API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("archera API returned HTTP %d: %s", e.StatusCode, e.Message)
}

// Client is the real read-only QuoteClient. It issues single-shot GETs and
// never follows redirects, so the API key cannot be forwarded to another host.
type Client struct {
	baseURL string
	cfg     Config
	hc      *http.Client
}

var _ QuoteClient = (*Client)(nil)

// Format keeps the embedded configuration, and with it the key, out of any
// fmt output of a Client (same %p/%T limit as Config).
func (c Client) Format(s fmt.State, _ rune) {
	_, _ = fmt.Fprintf(s, "insurance.Client{OrgID:%q, APIKey:[redacted]}", c.cfg.OrgID)
}

// NewClient returns a Client for the fixed Archera origin. hc is optional; nil
// uses the shared SSRF-hardened client with a 30-second timeout.
func NewClient(cfg Config, hc *http.Client) (*Client, error) {
	return newClient(BaseURL, cfg, hc)
}

func newClient(baseURL string, cfg Config, hc *http.Client) (*Client, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("archera API key is not configured")
	}
	if !isUUID(cfg.OrgID) {
		return nil, errors.New("archera org ID must be a UUID")
	}
	if hc == nil {
		hc = httpclient.New()
	}
	noRedirect := *hc
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: baseURL, cfg: cfg, hc: &noRedirect}, nil
}

// Comparison reads the documented comparison operation. Empty request slices
// follow the documented defaults.
func (c *Client) Comparison(ctx context.Context, req ComparisonRequest) (*Comparison, error) {
	if !isUUID(req.PlanID) {
		return nil, errors.New("archera plan ID must be a UUID")
	}
	q := url.Values{}
	for _, id := range req.LineItemIDs {
		if !isUUID(id) {
			return nil, fmt.Errorf("archera line item ID %q must be a UUID", id)
		}
		q.Add("line_item_ids", id)
	}
	for _, t := range req.ContractTerms {
		if _, ok := contractTerms[t]; !ok {
			return nil, fmt.Errorf("unsupported contract term %q", t)
		}
		q.Add("contract_terms", t)
	}
	for _, p := range req.PaymentOptions {
		if _, err := parsePayment(string(p)); err != nil {
			return nil, fmt.Errorf("payment option: %w", err)
		}
		q.Add("payment_options", string(p))
	}
	body, fetchedAt, err := c.get(ctx, c.planPath(req.PlanID)+"/comparison", q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	return DecodeComparison(&boundedReader{r: body, n: maxResponseBytes}, c.cfg.OrgID, req.PlanID, fetchedAt)
}

// Plan reads the documented CommitmentPlan operation.
func (c *Client) Plan(ctx context.Context, planID string) (*Plan, error) {
	if !isUUID(planID) {
		return nil, errors.New("archera plan ID must be a UUID")
	}
	body, _, err := c.get(ctx, c.planPath(planID), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	return DecodePlan(&boundedReader{r: body, n: maxResponseBytes})
}

func (c *Client) planPath(planID string) string {
	return "/v1/org/" + c.cfg.OrgID + "/commitment-plans/" + planID
}

var errResponseTooLarge = fmt.Errorf("response exceeds %d bytes", maxResponseBytes)

// boundedReader reads at most n bytes and fails with errResponseTooLarge, not a
// silent truncation, if the body has more.
type boundedReader struct {
	r io.Reader
	n int64
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.n <= 0 {
		var probe [1]byte
		if n, _ := b.r.Read(probe[:]); n > 0 {
			return 0, errResponseTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > b.n {
		p = p[:b.n]
	}
	n, err := b.r.Read(p)
	b.n -= int64(n)
	return n, err
}

// get performs one GET and returns the body of a 2xx response. Any other
// status, including a 3xx that was not followed, becomes an *HTTPError.
func (c *Client) get(ctx context.Context, path string, q url.Values) (io.ReadCloser, time.Time, error) {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("building archera request: %w", err)
	}
	req.Header.Set("x-api-key", c.cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("archera request failed: %w", sanitizeTransportError(err))
	}
	fetchedAt := time.Now().UTC()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer func() { _ = resp.Body.Close() }()
		return nil, fetchedAt, newHTTPError(resp)
	}
	return resp.Body, fetchedAt, nil
}

// sanitizeTransportError drops the *url.Error wrapper so the request URL (org
// and plan IDs) stays out of logs; the cause is kept.
func sanitizeTransportError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func newHTTPError(resp *http.Response) *HTTPError {
	e := &HTTPError{StatusCode: resp.StatusCode}
	if s, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && s > 0 {
		// Compare before multiplying: s*time.Second overflows int64 for large s.
		if s > int(maxRetryAfter/time.Second) {
			e.RetryAfter = maxRetryAfter
		} else {
			e.RetryAfter = time.Duration(s) * time.Second
		}
	}
	// ApiErrorResponse and Error both carry a string "message"; 400/404 may
	// have no body at all.
	var shape struct {
		Message string `json:"message"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err == nil && json.Unmarshal(raw, &shape) == nil {
		e.Message = truncate(shape.Message, maxErrorMessageLen)
	}
	return e
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "..."
}

// isUUID reports whether s is a canonical 36-character UUID. Path and query
// IDs are validated before use so they can never inject path or query syntax.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", rune(c)) {
				return false
			}
		}
	}
	return true
}
