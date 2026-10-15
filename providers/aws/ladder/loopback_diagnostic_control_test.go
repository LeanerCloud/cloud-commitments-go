package ladder_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runLoopbackFixture(t *testing.T, fixture func(*testing.T)) {
	iterations := 1
	if runtime.GOOS == "linux" {
		iterations = 33
	}
	for i := range iterations {
		if !t.Run(fmt.Sprint(i), fixture) {
			return
		}
	}
}
func waitLoopControl(t *testing.T, done <-chan struct{}) bool {
	t.Helper()
	select {
	case <-done:
		return true
	case <-time.After(5 * time.Second):
		t.Error("control did not complete")
		return false
	}
}

func TestLoopbackDiagnosticLifecycle(t *testing.T) {
	for _, cancelRequest := range []bool{true, false} {
		t.Run(fmt.Sprint(cancelRequest), func(t *testing.T) {
			d := newLoopDiagnostic(t)
			deadline, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			ctx, cancel := context.WithCancelCause(deadline)
			release, done, handlerDone, wrote := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			var operationErr, writeErr, flushErr error
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handlerDone)
				d.record(loopEvent{Kind: "handler_begin"}, r.Context(), nil)
				defer d.record(loopEvent{Kind: "handler_end"}, r.Context(), nil)
				w.Header().Set("Content-Type", "application/x-amz-json-1.1")
				_, writeErr = w.Write([]byte("{\"ResultsByTime\":["))
				flushErr = http.NewResponseController(w).Flush()
				<-release
			}))
			tr := d.transport(srv.Client().Transport)
			tr.readStarted = make(chan struct{})
			cleanup := sync.OnceFunc(func() {
				defer tr.http.CloseIdleConnections()
				cancel(nil)
				close(release)
				waitLoopControl(t, done)
				handlerJoined := waitLoopControl(t, handlerDone)
				waitLoopControl(t, wrote)
				d.record(loopEvent{Kind: "server_close_begin"}, nil, nil)
				if !handlerJoined {
					srv.CloseClientConnections()
					d.record(loopEvent{Kind: "server_close_unjoined"}, nil, srv.Listener.Close())
					return
				}
				srv.Close()
				d.record(loopEvent{Kind: "server_close_end"}, nil, nil)
				assert.NoError(t, errors.Join(writeErr, flushErr))
			})
			defer cleanup()
			endpoint, err := url.Parse(srv.URL)
			require.NoError(t, err)
			gotConn := make(chan *loopConn, 1)
			ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
				GotConn:      func(info httptrace.GotConnInfo) { gotConn <- info.Conn.(*loopConn) },
				WroteRequest: func(httptrace.WroteRequestInfo) { close(wrote) },
			})
			client := costexplorer.NewFromConfig(aws.Config{Region: "us-east-1", RetryMaxAttempts: 1, Credentials: credentials.NewStaticCredentialsProvider("synthetic", "synthetic", ""), HTTPClient: &http.Client{Transport: ceLoopback{endpoint: endpoint, transport: tr}}})
			go func() {
				defer close(done)
				_, operationErr = client.GetCostAndUsage(ctx, &costexplorer.GetCostAndUsageInput{Granularity: types.GranularityDaily, Metrics: []string{"UnblendedCost"}, TimePeriod: &types.DateInterval{Start: aws.String("2026-10-01"), End: aws.String("2026-10-02")}})
			}()
			require.True(t, waitLoopControl(t, tr.readStarted))
			var conn *loopConn
			select {
			case conn = <-gotConn:
			case <-deadline.Done():
				t.Fatal("connection not observed")
			}
			var _ io.ReaderFrom = conn
			var _ io.WriterTo = conn
			var _ syscall.Conn = conn
			var _ interface {
				CloseRead() error
				CloseWrite() error
			} = conn
			if cancelRequest {
				cancel(errors.New("control cancellation cause"))
			} else {
				require.NoError(t, conn.Close())
			}
			require.True(t, waitLoopControl(t, done))
			require.Error(t, operationErr)
			if !cancelRequest {
				require.NoError(t, ctx.Err())
			}
			cleanup()
			kinds := map[string]loopEvent{}
			explicitClose := false
			for _, e := range d.freeze().Events {
				kinds[e.Kind] = e
				if e.Kind == "connection_close_begin" {
					require.Equal(t, [2]string{}, [2]string{e.Context, e.Cause})
					require.LessOrEqual(t, len(e.Frames), 8)
					for _, frame := range e.Frames {
						explicitClose = explicitClose || strings.Contains(frame, "TestLoopbackDiagnosticLifecycle")
						require.Regexp(t, `^\S+ [^/ ]+:\d+$`, frame)
					}
				}
			}
			require.Equal(t, 200, kinds["headers"].N)
			require.Contains(t, []string{"canceled", "closed", "other"}, kinds["body_read"].Err)
			require.Positive(t, kinds["got_conn"].Conn)
			require.Equal(t, kinds["got_conn"].Conn, kinds["body_read"].Conn)
			require.Equal(t, "none", kinds["wrote_request"].Err)
			require.IsIncreasing(t, []int{kinds["headers"].Seq, kinds["body_read"].Seq, kinds["body_close_end"].Seq})
			require.IsIncreasing(t, []int{0, kinds["handler_end"].Seq, kinds["server_close_end"].Seq})
			require.True(t, kinds["body_read"].Deadline)
			require.Equal(t, map[bool]string{true: "canceled", false: "none"}[cancelRequest], kinds["body_read"].Context)
			require.Equal(t, map[bool]string{true: "other", false: "none"}[cancelRequest], kinds["body_read"].Cause)
			require.True(t, cancelRequest || explicitClose)
		})
	}
}

func TestLoopbackDiagnosticCutoff(t *testing.T) {
	d := newLoopDiagnostic(t)
	start, recorded, cut := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var raced loopSnapshot
	joinWorkers := func() {
		require.NotContains(t, []bool{waitLoopControl(t, recorded), waitLoopControl(t, cut)}, false)
	}
	defer joinWorkers()
	go func() { defer close(recorded); <-start; d.record(loopEvent{Kind: "racing"}, nil, nil) }()
	go func() { defer close(cut); <-start; raced = d.freeze() }()
	close(start)
	joinWorkers()
	require.True(t, len(raced.Events) <= 1 && strings.Contains(raced.Incomplete, "INCOMPLETE"), "%+v", raced)
	bounded := newLoopDiagnostic(t)
	for range 130 {
		bounded.record(loopEvent{Kind: "error"}, nil, errors.New("sensitive-token/path"))
	}
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()
	tr := bounded.transport(&http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return local, nil }})
	conn, err := tr.http.DialContext(context.Background(), "tcp", "unused")
	require.NoError(t, err)
	require.Same(t, local, conn)
	snapshot := bounded.freeze()
	require.Len(t, snapshot.Events, 128)
	require.Equal(t, 2, snapshot.Overflow)
	require.True(t, snapshot.Unsupported)
	require.NotContains(t, fmt.Sprint(snapshot), "sensitive-token/path")
	bounded.record(loopEvent{Kind: "after_overflow_cutoff"}, nil, nil)
	require.Equal(t, snapshot, bounded.freeze())
}
