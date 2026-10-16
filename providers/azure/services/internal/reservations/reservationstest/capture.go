// Package reservationstest provides a fake ARM transport for asserting the
// purchase request bodies the Azure service clients send.
package reservationstest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Capture is an HTTP client that records the calculatePrice request body and
// answers every call with a minimal success response. It performs no network IO.
type Capture struct {
	mu        sync.Mutex
	Calls     int
	BodyBytes []byte
}

// Do implements the service clients' HTTPClient interface.
func (c *Capture) Do(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls++
	payload := `{}`
	switch {
	case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/providers/Microsoft.Capacity"):
		payload = `{"registrationState":"Registered"}`
	case strings.HasSuffix(req.URL.Path, "/calculatePrice"):
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		c.BodyBytes = b
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
