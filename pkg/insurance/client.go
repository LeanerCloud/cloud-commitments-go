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
	"unicode"

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
// only as the x-api-key header. Secret redacts itself under every printer, so
// a Config is safe to print or hold in any struct field.
type Config struct {
	APIKey Secret
	OrgID  string
}

// HTTPError is a non-2xx vendor response. Only the status, the Retry-After
// header and a truncated message from the documented error shapes are kept;
// the raw body is never echoed.
type HTTPError struct {
	Message    string
	StatusCode int

	// RetryAfter is the parsed Retry-After header, zero when absent or not a
	// number of seconds. The client never retries on its own.
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("archera API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("archera API returned HTTP %d: %s", e.StatusCode, e.Message)
}

// Client is the real read-only QuoteClient. It issues single-shot GETs and
// never follows redirects, so the API key cannot be forwarded to another host.
//
// The API key is held only inside the setAuth and redact closures, never in a
// field fmt or reflection can read, so no fmt verb prints it wherever the
// Client (or a pointer to it) is stored.
type Client struct {
	hc *http.Client

	// setAuth adds the credential to one outgoing request.
	setAuth func(*http.Request)

	// redact masks the credential in a vendor-supplied message.
	redact func(string) string

	baseURL string
	orgID   string
}

var _ QuoteClient = (*Client)(nil)

// NewClient returns a Client for the fixed Archera origin. hc is optional; nil
// uses the shared SSRF-hardened client with a 30-second timeout.
func NewClient(cfg Config, hc *http.Client) (*Client, error) {
	return newClient(BaseURL, cfg, hc)
}

func newClient(baseURL string, cfg Config, hc *http.Client) (*Client, error) {
	key := cfg.APIKey.Reveal()
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("archera API key is not configured")
	}
	// The key travels in an HTTP header and is redacted verbatim from vendor
	// text, so only printable ASCII is accepted: an invisible or look-alike
	// character in the key would let a vendor echo defeat the redaction once
	// cleanMessage strips it. The error names the position, never the key.
	for i := 0; i < len(key); i++ {
		if key[i] < 0x21 || key[i] > 0x7e {
			return nil, fmt.Errorf("archera API key contains a non-printable or non-ASCII character at byte %d", i)
		}
	}
	if !isUUID(cfg.OrgID) {
		return nil, errors.New("archera org ID must be a UUID")
	}
	if hc == nil {
		hc = httpclient.New()
	}
	noRedirect := *hc
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		baseURL: baseURL,
		orgID:   cfg.OrgID,
		hc:      &noRedirect,
		setAuth: func(req *http.Request) { req.Header.Set("x-api-key", key) },
		redact:  func(msg string) string { return strings.ReplaceAll(msg, key, "[redacted]") },
	}, nil
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
	cmp, err := DecodeComparison(&boundedReader{r: body, n: maxResponseBytes}, c.orgID, req.PlanID, fetchedAt)
	if err != nil {
		return nil, sanitizeDecodeError(err, c.redact)
	}
	return cmp, nil
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
	plan, err := DecodePlan(&boundedReader{r: body, n: maxResponseBytes})
	if err != nil {
		return nil, sanitizeDecodeError(err, c.redact)
	}
	return plan, nil
}

func (c *Client) planPath(planID string) string {
	return "/v1/org/" + c.orgID + "/commitment-plans/" + planID
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
		n, err := b.r.Read(probe[:])
		if n > 0 {
			return 0, errResponseTooLarge
		}
		if err != nil && err != io.EOF {
			return 0, err
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
	body, _, fetchedAt, err := c.getWithHeaders(ctx, path, q)
	return body, fetchedAt, err
}

func (c *Client) getWithHeaders(ctx context.Context, path string, q url.Values) (io.ReadCloser, http.Header, time.Time, error) {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("building archera request: %w", err)
	}
	c.setAuth(req)
	req.Header.Set("Accept", "application/json")
	// baseURL is the fixed BaseURL constant; only in-package tests override it.
	resp, err := c.hc.Do(req) //nolint:gosec // G704: see comment above
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("archera request failed: %w", sanitizeTransportError(err))
	}
	fetchedAt := time.Now().UTC()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer func() { _ = resp.Body.Close() }()
		return nil, nil, fetchedAt, newHTTPError(resp, c.redact)
	}
	return resp.Body, resp.Header, fetchedAt, nil
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

func newHTTPError(resp *http.Response, redact func(string) string) *HTTPError {
	e := &HTTPError{StatusCode: resp.StatusCode}
	// A value past the int range makes Atoi return ErrRange with the clamped
	// maximum, which the cap below handles; a negative overflow stays ignored.
	s, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
	if (err == nil || errors.Is(err, strconv.ErrRange)) && s > 0 {
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
		e.Message = sanitizeVendorText(shape.Message, redact)
	}
	return e
}

// sanitizeVendorText is the one pipeline for vendor-influenced text: strip
// invisible characters first so a split key is reassembled, then redact the
// key, then truncate (redacting after the cut would leave a key prefix).
func sanitizeVendorText(msg string, redact func(string) string) string {
	return truncate(redact(cleanMessage(msg)), maxErrorMessageLen)
}

// sanitizedError carries the sanitized text of a decode error while keeping
// the original in the Unwrap chain, so errors.Is/As (errMissing, read-size
// and transport errors) still work.
type sanitizedError struct {
	err error
	msg string
}

func (e *sanitizedError) Error() string { return e.msg }
func (e *sanitizedError) Unwrap() error { return e.err }

// sanitizeDecodeError runs a decode error through the same pipeline as
// HTTPError.Message. Decode errors are built in quote.go, which cannot reach
// the client's redactor.
func sanitizeDecodeError(err error, redact func(string) string) error {
	return &sanitizedError{msg: sanitizeVendorText(err.Error(), redact), err: err}
}

// cleanMessage drops control characters (log forging, ANSI injection) and the
// invisible format, line and paragraph separators (Cf, Zl, Zp) that spoof
// bidi text, forge line breaks or split an echoed key.
func cleanMessage(msg string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return -1
		}
		return r
	}, msg)
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
