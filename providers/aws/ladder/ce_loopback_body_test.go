package ladder_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	awsladder "github.com/LeanerCloud/cloud-commitments-go/providers/aws/ladder"
)

type ceRoundTripFunc func(*http.Request) (*http.Response, error)

func (f ceRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type ceSource struct {
	io.Reader
	readErr, closeErr error
	reads, closes     int
	closed            bool
}

func (b *ceSource) Read(p []byte) (int, error) {
	b.reads++
	if b.closed {
		return 0, net.ErrClosed
	}
	if b.readErr != nil {
		n, _ := b.Reader.Read(p)
		return n, b.readErr
	}
	return b.Reader.Read(p)
}
func (b *ceSource) Close() error {
	b.closes++
	b.closed = true
	return b.closeErr
}

func TestCELoopbackBodyOwnership(t *testing.T) {
	readErr, closeErr, downstreamErr := errors.New("read"), errors.New("close"), errors.New("transport")
	for _, mode := range []string{"binary", "get-body", "nil", "no-body", "empty", "unknown", "zero", "read", "close", "both", "scheme", "host", "guard-close", "transport"} {
		t.Run(mode, func(t *testing.T) {
			payload := []byte{'a', 0, 'b'}
			source := &ceSource{Reader: bytes.NewReader(payload)}
			if mode == "read" || mode == "both" {
				source.readErr = readErr
			}
			if mode == "close" || mode == "both" || mode == "guard-close" {
				source.closeErr = closeErr
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://ce.us-east-1.amazonaws.com/path?q=value", source)
			require.NoError(t, err)
			req.ContentLength = int64(len(payload))
			req.Header.Set("X-Fixture", "preserved")
			getterCalls := 0
			if mode == "get-body" {
				req.GetBody = func() (io.ReadCloser, error) {
					getterCalls++
					return io.NopCloser(bytes.NewReader(payload)), nil
				}
			}
			if mode == "nil" {
				req.Body, payload = nil, nil
			}
			if mode == "no-body" {
				req.Body, payload = http.NoBody, nil
			}
			if mode == "empty" {
				source.Reader, payload, req.ContentLength = bytes.NewReader(nil), []byte{}, 0
			}
			if mode == "unknown" {
				req.ContentLength = -1
			}
			if mode == "zero" {
				req.ContentLength = 0
			}
			if mode == "scheme" {
				req.URL.Scheme = "http"
			}
			if mode == "host" || mode == "guard-close" {
				req.URL.Host = "rejected.invalid"
			}
			originalBody, originalURL, length := req.Body, req.URL.String(), req.ContentLength
			endpoint, err := url.Parse("http://fixture.invalid")
			require.NoError(t, err)
			response, calls := &http.Response{StatusCode: 200, Body: http.NoBody}, 0
			transport := ceRoundTripFunc(func(local *http.Request) (*http.Response, error) {
				calls++
				assert.Same(t, ctx, local.Context())
				assert.Equal(t, length, local.ContentLength)
				assert.Equal(t, req.Header, local.Header)
				assert.Equal(t, req.Method, local.Method)
				assert.Equal(t, "/path?q=value", local.URL.RequestURI())
				assert.Equal(t, "http://fixture.invalid/path?q=value", local.URL.String())
				assert.Equal(t, endpoint.Host, local.Host)
				if mode == "get-body" {
					replay, getErr := local.GetBody()
					assert.NoError(t, getErr)
					got, read := io.ReadAll(replay)
					assert.NoError(t, errors.Join(read, replay.Close()))
					assert.Equal(t, payload, got)
					assert.Equal(t, 1, getterCalls)
				} else {
					assert.Nil(t, local.GetBody)
				}
				if originalBody == nil || originalBody == http.NoBody {
					assert.Equal(t, originalBody, local.Body)
				} else {
					assert.True(t, source.closed)
					got, read := io.ReadAll(local.Body)
					assert.NoError(t, read)
					assert.Equal(t, payload, got)
					assert.NoError(t, local.Body.Close())
				}
				if mode == "transport" {
					return response, downstreamErr
				}
				return response, nil
			})
			got, err := (ceLoopback{endpoint: endpoint, transport: transport}).RoundTrip(req)
			if got != nil {
				assert.NoError(t, got.Body.Close())
			}
			badBody := source.readErr != nil || source.closeErr != nil
			guard := mode == "scheme" || mode == "host" || mode == "guard-close"
			if badBody || guard {
				require.Error(t, err)
				assert.Nil(t, got)
				assert.Zero(t, calls)
			} else {
				assert.Same(t, response, got)
				assert.Equal(t, 1, calls)
				if mode == "transport" {
					assert.ErrorIs(t, err, downstreamErr)
				} else {
					assert.NoError(t, err)
				}
			}
			if source.readErr != nil {
				assert.ErrorIs(t, err, readErr)
			}
			if source.closeErr != nil {
				assert.ErrorIs(t, err, closeErr)
			}
			if guard {
				assert.Zero(t, source.reads)
			}
			if originalBody != nil && originalBody != http.NoBody {
				assert.Equal(t, 1, source.closes)
				assert.Same(t, originalBody, req.Body)
			} else {
				assert.Equal(t, originalBody, req.Body)
			}
			assert.Equal(t, originalURL, req.URL.String())
			assert.Equal(t, length, req.ContentLength)
		})
	}
}

func ceAwait(done, abort <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-abort:
		return errors.New("schedule aborted")
	case <-time.After(5 * time.Second):
		return errors.New("schedule did not complete")
	}
}

type ceSchedule struct {
	abort, entered, sdkClosed, wrote, connClosed chan struct{}
	enterOnce, sdkOnce, writeOnce, connOnce      sync.Once
	returned, dialed, handlerStarted             atomic.Bool
	copyErr, writeErr                            error
	status                                       int
}
type ceSDKBody struct {
	io.ReadCloser
	s *ceSchedule
}

func (b *ceSDKBody) WriteTo(w io.Writer) (int64, error) { return b.ReadCloser.(io.WriterTo).WriteTo(w) }
func (b *ceSDKBody) Close() error {
	err := b.ReadCloser.Close()
	if b.s.returned.Load() {
		var pcs [16]uintptr
		frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs[:])])
		for {
			frame, more := frames.Next()
			if frame.Function == "github.com/aws/smithy-go/transport/http.ClientHandler.Handle" {
				b.s.sdkOnce.Do(func() { close(b.s.sdkClosed) })
			}
			if !more {
				break
			}
		}
	}
	return err
}

type ceTerminalBody struct {
	io.ReadCloser
	s            *ceSchedule
	read, length int64
}

func (b *ceTerminalBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.read += int64(n)
	return n, err
}
func (b *ceTerminalBody) WriteTo(w io.Writer) (int64, error) {
	if b.read != b.length {
		return 0, errors.New("terminal copy before ContentLength consumed")
	}
	b.s.enterOnce.Do(func() { close(b.s.entered) })
	if err := ceAwait(b.s.sdkClosed, b.s.abort); err != nil {
		return 0, err
	}
	n, err := b.ReadCloser.(io.WriterTo).WriteTo(w)
	b.s.copyErr = err
	return n, err
}

type ceScheduledConn struct {
	*net.TCPConn
	s *ceSchedule
}

func (c *ceScheduledConn) Close() error {
	err := c.TCPConn.Close()
	c.s.connOnce.Do(func() { close(c.s.connClosed) })
	return err
}

type ceScheduledResponse struct {
	io.ReadCloser
	s *ceSchedule
}

func (r *ceScheduledResponse) Read(p []byte) (int, error) {
	if err := ceAwait(r.s.wrote, r.s.abort); err != nil {
		return 0, err
	}
	if r.s.writeErr != nil {
		if err := ceAwait(r.s.connClosed, r.s.abort); err != nil {
			return 0, err
		}
	}
	return r.ReadCloser.Read(p)
}

type ceScheduledClient struct {
	client *http.Client
	s      *ceSchedule
}

func (c ceScheduledClient) Do(req *http.Request) (*http.Response, error) {
	req.Body = &ceSDKBody{ReadCloser: req.Body, s: c.s}
	resp, err := c.client.Do(req)
	c.s.returned.Store(true)
	return resp, err
}

func TestCELoopbackPublicBaselineAfterBodyClose(t *testing.T) {
	s := &ceSchedule{abort: make(chan struct{}), entered: make(chan struct{}), sdkClosed: make(chan struct{}), wrote: make(chan struct{}), connClosed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done, handlerDone := make(chan struct{}), make(chan struct{})
	var handlerErr, operationErr error
	var baseline ladder.UsageBaseline
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer close(handlerDone)
		s.handlerStarted.Store(true)
		var input costexplorer.GetCostAndUsageInput
		handlerErr = json.NewDecoder(req.Body).Decode(&input)
		if handlerErr != nil {
			return
		}
		if req.Header.Get("X-Amz-Target") != "AWSInsightsIndexService.GetCostAndUsage" || input.TimePeriod == nil {
			handlerErr = errors.New("unexpected CE request")
			return
		}
		start, err := time.Parse(time.DateOnly, aws.ToString(input.TimePeriod.Start))
		if err != nil {
			handlerErr = err
			return
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		handlerErr = json.NewEncoder(w).Encode(map[string]any{"ResultsByTime": baselineRows(start, "account"), "padding": strings.Repeat("x", 64<<10)})
	}))
	tr := srv.Client().Transport.(*http.Transport).Clone()
	dial := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return conn, err
		}
		s.dialed.Store(true)
		return &ceScheduledConn{TCPConn: conn.(*net.TCPConn), s: s}, nil
	}
	cleanup := sync.OnceFunc(func() {
		cancel()
		close(s.abort)
		opJoin := ceAwait(done, nil)
		var handlerJoin, writeJoin error
		if s.handlerStarted.Load() {
			handlerJoin = ceAwait(handlerDone, nil)
		}
		if s.dialed.Load() {
			writeJoin = ceAwait(s.wrote, nil)
		} else {
			t.Log("request aborted before any connection was dialed; skipping write and handler joins")
		}
		if handlerJoin == nil {
			srv.Close()
		} else {
			srv.CloseClientConnections()
			assert.NoError(t, srv.Listener.Close())
		}
		tr.CloseIdleConnections()
		assert.NoError(t, errors.Join(opJoin, handlerJoin, writeJoin))
		if handlerJoin == nil {
			assert.NoError(t, handlerErr)
		}
	})
	defer cleanup()
	endpoint, err := url.Parse(srv.URL)
	require.NoError(t, err)
	inner := ceRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.ContentLength <= 0 || req.Method != http.MethodPost || req.Header.Get("X-Amz-Target") != "AWSInsightsIndexService.GetCostAndUsage" {
			return nil, errors.New("unexpected scheduled CE request")
		}
		req.Body = &ceTerminalBody{ReadCloser: req.Body, s: s, length: req.ContentLength}
		trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
			s.writeOnce.Do(func() { s.writeErr = info.Err; close(s.wrote) })
		}}
		resp, tripErr := tr.RoundTrip(req.Clone(httptrace.WithClientTrace(req.Context(), trace)))
		if tripErr != nil {
			return resp, tripErr
		}
		if waitErr := ceAwait(s.entered, s.abort); waitErr != nil {
			resp.Body.Close()
			return nil, waitErr
		}
		s.status = resp.StatusCode
		resp.Body = &ceScheduledResponse{ReadCloser: resp.Body, s: s}
		return resp, nil
	})
	client := ceScheduledClient{client: &http.Client{Transport: ceLoopback{endpoint: endpoint, transport: inner}}, s: s}
	a, err := awsladder.NewFromConfig(aws.Config{Region: "eu-west-1", RetryMaxAttempts: 1, Credentials: credentials.NewStaticCredentialsProvider("synthetic", "synthetic", ""), HTTPClient: client}, awsladder.Config{Region: "eu-west-1", AccountID: "fixture-account"})
	require.NoError(t, err)
	go func() {
		defer close(done)
		baseline, operationErr = a.GetUsageBaseline(ctx, ladder.Scope{Provider: common.ProviderAWS, AccountID: "fixture-account"}, 30, 5)
	}()
	joined, callerErr := ceAwait(done, nil), ctx.Err()
	cleanup()
	require.NoError(t, joined)
	t.Logf("headers=%d terminalEOF=%t wroteEOF=%t", s.status, errors.Is(s.copyErr, io.EOF), errors.Is(s.writeErr, io.EOF))
	require.NoError(t, operationErr)
	require.NoError(t, callerErr)
	require.NoError(t, ceAwait(s.sdkClosed, nil))
	require.NoError(t, ceAwait(s.entered, nil))
	require.NoError(t, s.copyErr)
	require.NoError(t, s.writeErr)
	require.Equal(t, 200, s.status)
	require.NotNil(t, baseline.LowWaterUSDPerHour)
	require.InDelta(t, 2, *baseline.LowWaterUSDPerHour, 1e-9)
}
