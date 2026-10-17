package purchaseguard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeFault struct{}

func (*fakeFault) Error() string { return "fake already exists" }

type otherFault struct{}

func (*otherFault) Error() string { return "other fault" }

const (
	token  = "tok-0123456789abcdef"
	masked = "tok-0123..."
)

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

type finder struct {
	calls int
	id    string
	found bool
	err   error
}

func (f *finder) find(context.Context) (string, bool, error) {
	f.calls++
	return f.id, f.found, f.err
}

func TestGuard_EmptyTokenSkipsLookup(t *testing.T) {
	f := &finder{id: "x", found: true}
	id, found, err := Guard(context.Background(), "", "RDS RI", f.find)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, id)
	assert.Zero(t, f.calls)
}

func TestGuard_LookupErrorFailsLoud(t *testing.T) {
	cause := errors.New("describe boom")
	f := &finder{err: cause}
	id, found, err := Guard(context.Background(), token, "ElastiCache reservation", f.find)
	require.Error(t, err)
	assert.False(t, found)
	assert.Empty(t, id)
	assert.ErrorIs(t, err, cause)
	assert.Equal(t, "idempotency lookup failed before ElastiCache reservation purchase (refusing to purchase to avoid a possible double-buy): describe boom", err.Error())
}

func TestGuard_Found(t *testing.T) {
	buf := captureLog(t)
	f := &finder{id: "res-1", found: true}
	id, found, err := Guard(context.Background(), token, "RDS RI", f.find)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "res-1", id)
	assert.Equal(t, 1, f.calls)
	assert.Contains(t, buf.String(), "RDS RI for idempotency token "+masked+" already exists (res-1); skipping purchase (issue #641 re-drive)\n")
	assert.NotContains(t, buf.String(), token)
}

func TestGuard_NotFound(t *testing.T) {
	buf := captureLog(t)
	f := &finder{}
	id, found, err := Guard(context.Background(), token, "RDS RI", f.find)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, id)
	assert.Equal(t, 1, f.calls)
	assert.Empty(t, buf.String())
}

func TestRecover_EmptyTokenSkipsLookup(t *testing.T) {
	f := &finder{id: "x", found: true}
	id, ok := Recover[*fakeFault](context.Background(), "", "RDS RI", &fakeFault{}, f.find)
	assert.False(t, ok)
	assert.Empty(t, id)
	assert.Zero(t, f.calls)
}

func TestRecover_NonMatchingErrorSkipsLookup(t *testing.T) {
	f := &finder{id: "x", found: true}
	for _, e := range []error{&otherFault{}, errors.New("plain"), nil} {
		id, ok := Recover[*fakeFault](context.Background(), token, "RDS RI", e, f.find)
		assert.False(t, ok)
		assert.Empty(t, id)
	}
	assert.Zero(t, f.calls)
}

func TestRecover_AdoptsOnAlreadyExists(t *testing.T) {
	for name, perr := range map[string]error{
		"direct":  &fakeFault{},
		"wrapped": fmt.Errorf("purchase failed: %w", &fakeFault{}),
	} {
		t.Run(name, func(t *testing.T) {
			buf := captureLog(t)
			f := &finder{id: "res-2", found: true}
			id, ok := Recover[*fakeFault](context.Background(), token, "MemoryDB reservation", perr, f.find)
			assert.True(t, ok)
			assert.Equal(t, "res-2", id)
			assert.Equal(t, 1, f.calls)
			assert.Contains(t, buf.String(), "MemoryDB reservation res-2 already existed at purchase time; treating as idempotent re-drive (issue #641)\n")
		})
	}
}

func TestRecover_LookupErrorOrMissReturnsNotRecovered(t *testing.T) {
	for name, f := range map[string]*finder{
		"lookup error": {id: "res-3", found: true, err: errors.New("describe boom")},
		"not found":    {},
	} {
		t.Run(name, func(t *testing.T) {
			buf := captureLog(t)
			id, ok := Recover[*fakeFault](context.Background(), token, "RDS RI", &fakeFault{}, f.find)
			assert.False(t, ok)
			assert.Empty(t, id)
			assert.Equal(t, 1, f.calls)
			assert.False(t, strings.Contains(buf.String(), "already existed"))
		})
	}
}
