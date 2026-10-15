package ladder_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type loopEvent struct {
	Seq, Page, Conn, N        int
	Kind, Err, Context, Cause string
	Deadline                  bool
	Remaining                 time.Duration
	Frames                    []string
}
type loopSnapshot struct {
	Incomplete  string
	Events      []loopEvent
	Overflow    int
	Unsupported bool
}
type loopDiagnostic struct {
	mu            sync.Mutex
	events        []loopEvent
	seq, overflow int
	unsupported   bool
	snapshot      *loopSnapshot
	pages, conns  atomic.Int32
	http          *http.Transport
	readStarted   chan struct{}
}

func newLoopDiagnostic(t *testing.T) *loopDiagnostic {
	d := &loopDiagnostic{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("loopback diagnostic: %+v", d.freeze())
		}
	})
	return d
}
func loopError(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, net.ErrClosed):
		return "closed"
	default:
		return "other"
	}
}
func (d *loopDiagnostic) record(e loopEvent, ctx context.Context, err error) {
	e.Err = loopError(err)
	if ctx != nil {
		e.Context, e.Cause = loopError(ctx.Err()), loopError(context.Cause(ctx))
		if deadline, ok := ctx.Deadline(); ok {
			e.Deadline, e.Remaining = true, time.Until(deadline)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.snapshot != nil {
		return
	}
	d.seq++
	e.Seq = d.seq
	if len(d.events) == 128 {
		d.overflow++
		return
	}
	d.events = append(d.events, e)
}
func (d *loopDiagnostic) freeze() loopSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.snapshot == nil {
		d.snapshot = &loopSnapshot{"INCOMPLETE: callbacks not joined; post-cutoff/future events may be omitted", append([]loopEvent(nil), d.events...), d.overflow, d.unsupported}
	}
	return *d.snapshot
}
func loopFrames() []string {
	var pcs [8]uintptr
	frames := runtime.CallersFrames(pcs[:runtime.Callers(3, pcs[:])])
	var out []string
	for {
		frame, more := frames.Next()
		out = append(out, fmt.Sprintf("%s %s:%d", frame.Function, filepath.Base(frame.File), frame.Line))
		if !more {
			return out
		}
	}
}

func (d *loopDiagnostic) transport(base http.RoundTripper) *loopDiagnostic {
	tr := base.(*http.Transport).Clone()
	dial := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		tcp, ok := conn.(*net.TCPConn)
		if err != nil {
			return conn, err
		}
		if !ok {
			d.mu.Lock()
			d.unsupported = true
			d.mu.Unlock()
			return conn, err
		}
		id := int(d.conns.Add(1))
		return &loopConn{TCPConn: tcp, d: d, id: id}, err
	}
	d.http = tr
	return d
}
func (d *loopDiagnostic) RoundTrip(req *http.Request) (*http.Response, error) {
	page := int(d.pages.Add(1))
	d.record(loopEvent{Kind: "request", Page: page}, req.Context(), nil)
	connID := 0
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			if conn, ok := info.Conn.(*loopConn); ok {
				connID = conn.id
			}
			d.record(loopEvent{Kind: "got_conn", Page: page, Conn: connID}, req.Context(), nil)
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			d.record(loopEvent{Kind: "wrote_request", Page: page}, req.Context(), info.Err)
		},
	}
	resp, err := d.http.RoundTrip(req.Clone(httptrace.WithClientTrace(req.Context(), trace)))
	if resp != nil {
		d.record(loopEvent{Kind: "headers", Page: page, Conn: connID, N: resp.StatusCode}, req.Context(), err)
		resp.Body = &loopBody{ReadCloser: resp.Body, d: d, ctx: req.Context(), page: page, conn: connID, readStarted: d.readStarted}
	}
	return resp, err
}

type loopConn struct {
	*net.TCPConn
	d  *loopDiagnostic
	id int
}

func (c *loopConn) Close() error {
	c.d.record(loopEvent{Kind: "connection_close_begin", Conn: c.id, Frames: loopFrames()}, nil, nil)
	err := c.TCPConn.Close()
	c.d.record(loopEvent{Kind: "connection_close_end", Conn: c.id}, nil, err)
	return err
}

type loopBody struct {
	io.ReadCloser
	d           *loopDiagnostic
	ctx         context.Context
	page, conn  int
	readStarted chan struct{}
	readOnce    sync.Once
}

func (b *loopBody) Read(p []byte) (int, error) {
	if b.readStarted != nil {
		b.readOnce.Do(func() { close(b.readStarted) })
	}
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.d.record(loopEvent{Kind: "body_read", Page: b.page, Conn: b.conn, N: n}, b.ctx, err)
	}
	return n, err
}
func (b *loopBody) Close() error {
	b.d.record(loopEvent{Kind: "body_close_begin", Page: b.page, Conn: b.conn}, b.ctx, nil)
	err := b.ReadCloser.Close()
	b.d.record(loopEvent{Kind: "body_close_end", Page: b.page, Conn: b.conn}, b.ctx, err)
	return err
}
