// Package reservationstest provides a fake ARM transport for asserting the
// purchase request bodies the Azure service clients send.
package reservationstest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// Capture is an HTTP client that records the calculatePrice request body and
// answers every call with a minimal success response. It performs no network IO.
type Capture struct {
	// FailWhen, when set, is consulted after the request is recorded. A
	// non-nil return is returned from Do as the transport error.
	FailWhen func(*Request) error

	BodyBytes []byte

	// Requests lists every request received, in order.
	Requests []Request

	Calls int
	mu    sync.Mutex
}

// Request is one recorded HTTP request.
type Request struct {
	Method string
	URL    string
	Body   []byte
}

// Sequence returns "METHOD URL" for every recorded request, in order.
func (c *Capture) Sequence() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.Requests))
	for _, r := range c.Requests {
		out = append(out, r.Method+" "+r.URL)
	}
	return out
}

// Do implements the service clients' HTTPClient interface.
func (c *Capture) Do(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls++
	var reqBody []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		reqBody = b
	}
	rec := Request{Method: req.Method, URL: req.URL.String(), Body: reqBody}
	c.Requests = append(c.Requests, rec)
	if c.FailWhen != nil {
		if err := c.FailWhen(&rec); err != nil {
			return nil, err
		}
	}
	payload := `{}`
	switch {
	case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/providers/Microsoft.Capacity"):
		payload = `{"registrationState":"Registered"}`
	case strings.HasSuffix(req.URL.Path, "/calculatePrice"):
		c.BodyBytes = reqBody
		payload = `{"properties":{"reservationOrderId":"captured-order"}}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewBufferString(payload)),
		Header:     make(http.Header),
	}, nil
}

// BodyWithoutDisplayName returns the captured body re-encoded with
// properties.displayName removed (it embeds the wall-clock time). Map keys
// marshal sorted, so the result is deterministic.
func (c *Capture) BodyWithoutDisplayName() ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal(c.BodyBytes, &body); err != nil {
		return nil, err
	}
	if props, ok := body["properties"].(map[string]any); ok {
		delete(props, "displayName")
	}
	return json.Marshal(body)
}

// CountingCred is an azcore.TokenCredential that counts GetToken calls and can
// fail on demand.
type CountingCred struct {
	// Err, when non-nil, is returned from every GetToken call.
	Err error

	Calls int
	mu    sync.Mutex
}

var _ azcore.TokenCredential = (*CountingCred)(nil)

// GetToken implements azcore.TokenCredential.
func (c *CountingCred) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls++
	if c.Err != nil {
		return azcore.AccessToken{}, c.Err
	}
	return azcore.AccessToken{Token: "test-token"}, nil
}
