// Package recskip records the recommendations a GCP collection dropped so the
// provider can report them as a partial result instead of only logging them.
package recskip

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
)

// Log collects the recommendations a collection dropped. It is attached to the
// collection context by WithLog and is safe for concurrent use.
type Log struct {
	entries []string
	mu      sync.Mutex
}

type logKey struct{}

// WithLog returns a context carrying a fresh Log and that log.
func WithLog(ctx context.Context) (context.Context, *Log) {
	l := &Log{}
	return context.WithValue(ctx, logKey{}, l), l
}

// Record logs a dropped recommendation and records it in the context's Log, if
// any. scope names where it was dropped (for example "computeengine us-east1").
func Record(ctx context.Context, scope string, err error) {
	logging.Warnf("gcp %s: skipping recommendation: %v", scope, err)
	if l, ok := ctx.Value(logKey{}).(*Log); ok {
		l.mu.Lock()
		l.entries = append(l.entries, fmt.Sprintf("%s: %v", scope, err))
		l.mu.Unlock()
	}
}

// Entries returns a copy of the recorded skip reasons.
func (l *Log) Entries() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.entries...)
}

// PartialResultError reports that a collection dropped recommendations it
// could not convert (non-USD currency, malformed amount, unrecognized term).
// The recommendations that did succeed are returned alongside it.
type PartialResultError struct {
	Skipped []string
}

// Partial marks the error as one that accompanies usable recommendations; the
// cross-provider partial-result check matches on this method.
func (e *PartialResultError) Partial() bool { return true }

func (e *PartialResultError) Error() string {
	return fmt.Sprintf("gcp recommendations: %d recommendation(s) skipped: %s", len(e.Skipped), strings.Join(e.Skipped, "; "))
}
