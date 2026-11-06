package redshift

import (
	"bytes"
	"context"
	"log"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/redshift/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// go#700: the window dedupe skips nodes another token recorded as purchased.
// All tests are fixture-based: mocks and an in-test journal, no AWS calls.

const jOtherToken = "other-token"

// seedOther records rn-1 as purchased under a different token.
func (j *fakeJournal) seedOther() {
	j.rows = append(j.rows, common.AttemptRecord{IdempotencyToken: jOtherToken, ExecutionID: "exec-other", State: common.AttemptPurchased, ReservationID: "rn-1", OfferingID: jOffer})
}

func purchasedStates(j *fakeJournal) []common.AttemptState {
	recs := j.recordCalls()
	out := make([]common.AttemptState, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.State)
	}
	return out
}

func TestClaims_WindowSkipsOtherClaim(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.seedOther()
	c, m := newDedupe(jWindow("rn-1"))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.False(t, result.ExistingCommitment)
	assert.Equal(t, "rn-new", result.CommitmentID)
	m.AssertNumberOfCalls(t, "PurchaseReservedNodeOffering", 1)
	assert.Equal(t, []common.AttemptState{common.AttemptSending, common.AttemptPurchased}, purchasedStates(j))
}

func TestClaims_SingleRawMatchNoClaimNoPriorsAdopts(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	c, m := newDedupe(jWindow("rn-1"))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.NoError(t, err)
	assert.True(t, result.ExistingCommitment)
	assert.Equal(t, "rn-1", result.CommitmentID)
	jNoBuy(t, m)
	assert.Equal(t, "rn-1", j.row(t, jExecNew).ReservationID)
}

func TestClaims_ExclusionToOneNeedsOwnSendEvidence(t *testing.T) {
	t.Parallel()
	t.Run("no priors refuses as ambiguous", func(t *testing.T) {
		t.Parallel()
		j := newFakeJournal()
		j.seedOther()
		c, m := newDedupe(jWindow("rn-1", "rn-2"))
		result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
		require.Error(t, err)
		assert.False(t, result.Success)
		assert.Empty(t, result.CommitmentID)
		assert.Contains(t, err.Error(), "ambiguous")
		assert.Contains(t, err.Error(), "rn-2")
		assert.Contains(t, err.Error(), "recorded by another purchase and excluded")
		assert.Contains(t, err.Error(), "no earlier attempt of this token shows a send")
		assert.Contains(t, err.Error(), "rn-1")
		assert.Empty(t, j.recordCalls())
		jNoBuy(t, m)
	})
	t.Run("own prior under another offering refuses as ambiguous", func(t *testing.T) {
		t.Parallel()
		j := newFakeJournal()
		j.seedOther()
		j.seed(jExecOld, common.AttemptSending, "", "offering-0")
		c, m := newDedupe(jWindow("rn-1", "rn-2"))
		result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
		require.Error(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, err.Error(), "ambiguous")
		assert.Contains(t, err.Error(), "no earlier attempt of this token shows a send")
		assert.Empty(t, j.recordCalls())
		jNoBuy(t, m)
	})
	for _, state := range []common.AttemptState{common.AttemptSending, common.AttemptOutcomeUnknown} {
		t.Run("own "+string(state)+" prior adopts", func(t *testing.T) {
			t.Parallel()
			j := newFakeJournal()
			j.seedOther()
			j.seed(jExecOld, state, "", jOffer)
			c, m := newDedupe(jWindow("rn-1", "rn-2"))
			result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
			require.NoError(t, err)
			assert.True(t, result.ExistingCommitment)
			assert.Equal(t, "rn-2", result.CommitmentID)
			jNoBuy(t, m)
			assert.Equal(t, "rn-2", j.row(t, jExecNew).ReservationID)
		})
	}
}

func TestClaims_ThreeNodesOneClaimedStillAmbiguous(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.seedOther()
	c, m := newDedupe(jWindow("rn-1", "rn-2", "rn-3"))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.Error(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, err.Error(), "ambiguous")
	assert.Contains(t, err.Error(), "rn-2")
	assert.Contains(t, err.Error(), "rn-3")
	assert.Contains(t, err.Error(), "recorded by another purchase and excluded")
	assert.NotContains(t, err.Error(), "shows a send")
	assert.Contains(t, err.Error(), "rn-1")
	assert.Empty(t, j.recordCalls())
	jNoBuy(t, m)
}

func TestClaims_ClaimErrorRefusesNoSend(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.claimErr = assert.AnError
	c, m := newDedupe(jWindow("rn-1"))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.ErrorIs(t, err, common.ErrRefusedBeforePurchase)
	assert.False(t, result.Success)
	assert.Empty(t, j.recordCalls())
	jNoBuy(t, m)
}

func TestClaims_ZeroIDsSkipsRead(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.claimErr = assert.AnError
	c, m := newDedupe(nil)
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Empty(t, j.claimCalls())
	m.AssertNumberOfCalls(t, "PurchaseReservedNodeOffering", 1)
}

func TestClaims_PostSendSkipsOtherClaim(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.seedOther()
	c, m := recoverMock(buyFails(transportErr()), dnode("rn-1", "payment-pending", dedupeReqAt.Add(time.Second)))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.ErrorIs(t, err, common.ErrOutcomeUnknown)
	assert.False(t, result.Success)
	assert.Empty(t, result.CommitmentID)
	m.AssertNumberOfCalls(t, "PurchaseReservedNodeOffering", 1)
	assert.Equal(t, common.AttemptOutcomeUnknown, j.row(t, jExecNew).State)
	require.Len(t, j.claimCalls(), 1)
	assert.Equal(t, jToken, j.claimCalls()[0].Token)
}

func TestClaims_PostSendSingleUnclaimedAdopts(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.seedOther()
	c, _ := recoverMock(buyFails(transportErr()),
		dnode("rn-1", "payment-pending", dedupeReqAt.Add(time.Second)),
		dnode("rn-2", "payment-pending", dedupeReqAt.Add(time.Second)))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "rn-2", result.CommitmentID)
	assert.Equal(t, "rn-2", j.row(t, jExecNew).ReservationID)
}

func TestClaims_PostSendClaimErrorNoAdopt(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.claimErr = assert.AnError
	c, _ := recoverMock(buyFails(transportErr()), dnode("rn-1", "payment-pending", dedupeReqAt.Add(time.Second)))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.ErrorIs(t, err, common.ErrOutcomeUnknown)
	assert.False(t, result.Success)
	assert.Empty(t, result.CommitmentID)
	assert.Equal(t, common.AttemptOutcomeUnknown, j.row(t, jExecNew).State)
}

func TestClaims_SameTokenClaimPassesToken(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.seedOther()
	c, m := newDedupe(jWindow("rn-1"))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.NoError(t, err)
	assert.Equal(t, "rn-new", result.CommitmentID)
	m.AssertNumberOfCalls(t, "PurchaseReservedNodeOffering", 1)
	require.Len(t, j.claimCalls(), 1)
	assert.Equal(t, jToken, j.claimCalls()[0].Token)
	assert.Equal(t, []string{"rn-1"}, j.claimCalls()[0].IDs)
}

// A purchased row for the node appears under another execution of this token
// after PriorAttempts was read; the unique-index rule must fail the write.
func TestClaims_SameTokenRaceFailsClosed(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.onPrior = func(f *fakeJournal) {
		f.rows = append(f.rows, common.AttemptRecord{IdempotencyToken: jToken, ExecutionID: "exec-race", State: common.AttemptPurchased, ReservationID: "rn-1", OfferingID: jOffer})
		f.onPrior = nil
	}
	c, m := newDedupe(jWindow("rn-1"))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.ErrorIs(t, err, common.ErrOutcomeUnknown)
	assert.False(t, result.Success)
	jNoBuy(t, m)
}

// hiddenReader wraps a journal so only the AttemptJournal methods show.
type hiddenReader struct{ common.AttemptJournal }

func TestClaims_ReaderOptional(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	j := newFakeJournal()
	c, m := newDedupe(jWindow("rn-1"))
	opts := jOpts(j, jExecNew)
	opts.AttemptJournal = hiddenReader{j}
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), opts)
	require.NoError(t, err)
	assert.True(t, result.ExistingCommitment)
	assert.Equal(t, "rn-1", result.CommitmentID)
	jNoBuy(t, m)
	assert.Empty(t, j.claimCalls())
	assert.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("does not implement common.ClaimReader")))
	assert.Contains(t, buf.String(), common.MaskToken(jToken))
	assert.NotContains(t, buf.String(), jToken)
}

func TestClaims_TokenNoJournalUnchanged(t *testing.T) {
	t.Parallel()
	opts := dedupeOpts()
	c, m := newDedupe([]types.ReservedNode{dnode("rn-1", "active", dedupeReqAt)})
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), opts)
	require.NoError(t, err)
	assert.Equal(t, "rn-1", result.CommitmentID)
	jNoBuy(t, m)
	c, m = newDedupe([]types.ReservedNode{dnode("rn-1", "active", dedupeReqAt), dnode("rn-2", "active", dedupeReqAt)})
	_, err = c.PurchaseCommitment(context.Background(), rsIdemRec(), opts)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "recorded by another purchase")
	jNoBuy(t, m)
}
