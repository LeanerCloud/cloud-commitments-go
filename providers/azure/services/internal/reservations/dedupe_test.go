package reservations

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func TestDedupeCommitmentsByID_CollapsesRepeatedID(t *testing.T) {
	commitments := []common.Commitment{
		{CommitmentID: "res-1", ResourceType: "day-1"},
		{CommitmentID: "res-1", ResourceType: "day-2"},
		{CommitmentID: "res-1", ResourceType: "day-3"},
	}

	got := DedupeCommitmentsByID(commitments)

	assert.Len(t, got, 1)
	assert.Equal(t, "res-1", got[0].CommitmentID)
	// First occurrence is kept.
	assert.Equal(t, "day-1", got[0].ResourceType)
}

func TestDedupeCommitmentsByID_KeepsDistinctIDs(t *testing.T) {
	commitments := []common.Commitment{
		{CommitmentID: "res-1"},
		{CommitmentID: "res-2"},
		{CommitmentID: "res-1"},
		{CommitmentID: "res-3"},
	}

	got := DedupeCommitmentsByID(commitments)

	require := assert.New(t)
	require.Len(got, 3)
	ids := make([]string, 0, len(got))
	for _, c := range got {
		ids = append(ids, c.CommitmentID)
	}
	require.Equal([]string{"res-1", "res-2", "res-3"}, ids)
}

// TestDedupeCommitmentsByID_KeepsEmptyIDs verifies that commitments with an
// empty CommitmentID (no unique identity to dedupe on) are all kept, rather
// than being collapsed onto each other via an empty-string collision.
func TestDedupeCommitmentsByID_KeepsEmptyIDs(t *testing.T) {
	commitments := []common.Commitment{
		{CommitmentID: "", ResourceType: "a"},
		{CommitmentID: "", ResourceType: "b"},
	}

	got := DedupeCommitmentsByID(commitments)

	assert.Len(t, got, 2)
}

func TestDedupeCommitmentsByID_Empty(t *testing.T) {
	got := DedupeCommitmentsByID(nil)
	assert.Empty(t, got)
}
