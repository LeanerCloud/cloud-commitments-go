package recskip

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordWithoutLogDoesNotPanic(t *testing.T) {
	t.Parallel()
	Record(context.Background(), "computeengine us-east1", errors.New("x"))
}

func TestRecordAppendsEntries(t *testing.T) {
	t.Parallel()
	ctx, l := WithLog(context.Background())
	Record(ctx, "computeengine us-east1", errors.New("non-USD"))
	assert.Equal(t, []string{"computeengine us-east1: non-USD"}, l.Entries())
}

func TestPartialResultError(t *testing.T) {
	t.Parallel()
	var err error = &PartialResultError{Skipped: []string{"a: one", "b: two"}}
	var p interface{ Partial() bool }
	require.ErrorAs(t, err, &p)
	assert.True(t, p.Partial())
	assert.Contains(t, err.Error(), "2 recommendation(s) skipped")
	assert.Contains(t, err.Error(), "a: one; b: two")
}
