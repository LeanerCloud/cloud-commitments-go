package redshift

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/redshift/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/purchasecfg"
)

// go#711: reconcile never reports Found on a node another token recorded as purchased.

func claimReq(j common.AttemptJournal) common.ReconcileRequest {
	return reconcileReq(func(r *common.ReconcileRequest) {
		r.Options.IdempotencyToken = jToken
		r.Options.AttemptJournal = j
	})
}

func reconcileWith(t *testing.T, j common.AttemptJournal, nodes ...types.ReservedNode) common.ReconcileResult {
	t.Helper()
	c, _ := reconcileFake{pages: [][]types.ReservedNode{nodes}}.client(t)
	return c.ReconcilePurchase(context.Background(), claimReq(j))
}

func TestReconcileClaims_ClaimedWindowNodeIsUnknown(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.seedOther()
	got := reconcileWith(t, j, reconcileNode("rn-1"))
	assert.Equal(t, common.ReconcileUnknown, got.Verdict)
	assert.Equal(t, purchasecfg.ReasonUncertainCandidate, got.Reason)
	assert.Equal(t, []string{"rn-1"}, got.CandidateIDs)
	require.Len(t, j.claimCalls(), 1)
}

func TestReconcileClaims_ClaimedPlusUnclaimedIsUnknown(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.seedOther()
	got := reconcileWith(t, j, reconcileNode("rn-1"), reconcileNode("rn-2"))
	assert.Equal(t, common.ReconcileUnknown, got.Verdict)
	assert.Equal(t, purchasecfg.ReasonUncertainCandidate, got.Reason)
	assert.ElementsMatch(t, []string{"rn-1", "rn-2"}, got.CandidateIDs)
}

func TestReconcileClaims_ReadErrorIsUnknown(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.claimErr = assert.AnError
	got := reconcileWith(t, j, reconcileNode("rn-1"))
	assert.Equal(t, common.ReconcileUnknown, got.Verdict)
	assert.Equal(t, purchasecfg.ReasonClaimLookupFailed, got.Reason)
}

func TestReconcileClaims_NoReaderFound(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.seedOther()
	got := reconcileWith(t, hiddenReader{j}, reconcileNode("rn-1"))
	assert.Equal(t, common.ReconcileFound, got.Verdict)
	assert.Equal(t, "rn-1", got.CommitmentID)
	assert.Empty(t, j.claimCalls())
}

func TestReconcileClaims_NoJournalOrTokenFound(t *testing.T) {
	t.Parallel()
	c, _ := reconcileFake{pages: [][]types.ReservedNode{{reconcileNode("rn-1")}}}.client(t)
	assert.Equal(t, common.ReconcileFound, c.ReconcilePurchase(context.Background(), reconcileReq()).Verdict)
	j := newFakeJournal()
	j.seedOther()
	c, _ = reconcileFake{pages: [][]types.ReservedNode{{reconcileNode("rn-1")}}}.client(t)
	req := claimReq(j)
	req.Options.IdempotencyToken = ""
	assert.Equal(t, common.ReconcileFound, c.ReconcilePurchase(context.Background(), req).Verdict)
	assert.Empty(t, j.claimCalls())
}

func TestReconcileClaims_SameTokenClaimIsNotOther(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.seed(jExecOld, common.AttemptPurchased, "rn-1", jOffer)
	got := reconcileWith(t, j, reconcileNode("rn-1"))
	assert.Equal(t, common.ReconcileFound, got.Verdict)
	assert.Equal(t, "rn-1", got.CommitmentID)
	require.Len(t, j.claimCalls(), 1)
	assert.Equal(t, jToken, j.claimCalls()[0].Token)
}
