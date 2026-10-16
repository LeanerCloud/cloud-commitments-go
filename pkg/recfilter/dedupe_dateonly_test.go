package recfilter

import (
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
)

func TestFilterRecentCommitments_DateOnlyStart(t *testing.T) {
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	cutoff := now.Add(-24 * time.Hour) // 2026-10-15T09:00Z
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

	tests := []struct {
		name  string
		start time.Time
		isDay bool
		state common.CommitmentState
		want  bool
	}{
		{"same date", day(2026, 10, 16), true, common.CommitmentStateActive, true},
		{"late yesterday", day(2026, 10, 15), true, common.CommitmentStateActive, true},
		{"older day", day(2026, 10, 14), true, common.CommitmentStateActive, false},
		{"day end exactly at cutoff", cutoff.Add(-24 * time.Hour), true, common.CommitmentStateActive, false},
		{"day end 1ns after cutoff", cutoff.Add(-24*time.Hour + time.Nanosecond), true, common.CommitmentStateActive, true},
		{"retired day-only", day(2026, 10, 16), true, common.CommitmentStateRetired, false},
		{"pending-return day-only", day(2026, 10, 16), true, common.CommitmentStatePendingReturn, false},
		{"expired day-only", day(2026, 10, 16), true, common.CommitmentStateExpired, false},
		{"canceled day-only", day(2026, 10, 16), true, common.CommitmentStateCanceled, false},
		{"failed day-only", day(2026, 10, 16), true, common.CommitmentStateFailed, false},
		{"unrecognized state still counts", day(2026, 10, 16), true, common.CommitmentState("weird"), true},
		{"precise recent", now.Add(-time.Hour), false, common.CommitmentStateActive, true},
		{"precise midnight not widened", day(2026, 10, 15), false, common.CommitmentStateActive, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &DuplicateChecker{LookbackHours: 24, now: func() time.Time { return now }}
			got := d.filterRecentCommitments([]common.Commitment{{
				CommitmentID: "c", StartDate: tt.start, StartDateIsDay: tt.isDay, State: tt.state,
			}})
			assert.Equal(t, tt.want, len(got) == 1)
		})
	}
}
