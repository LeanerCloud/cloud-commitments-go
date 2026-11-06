package redshift

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/redshift"
	"github.com/aws/aws-sdk-go-v2/service/redshift/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// All tests here are fixture-based: mocks and an in-test journal, no AWS calls.

const (
	jExecNew = "exec-new"
	jExecOld = "exec-old"
	jOffer   = "offering-1"
)

var jToken = common.DeriveIdempotencyToken("journal", 0)

type journalCall struct {
	Method string
	Rec    common.AttemptRecord
}

// fakeJournal enforces the transition table and keeps the first OfferingID of
// each (token, execution) row, as a store must.
type fakeJournal struct {
	mu       sync.Mutex
	rows     []common.AttemptRecord
	calls    []journalCall
	priorErr error
	failOn   map[common.AttemptState]error
}

func newFakeJournal() *fakeJournal {
	return &fakeJournal{failOn: map[common.AttemptState]error{}}
}

func (f *fakeJournal) find(token, exec string) int {
	return slices.IndexFunc(f.rows, func(r common.AttemptRecord) bool { return r.IdempotencyToken == token && r.ExecutionID == exec })
}

func (f *fakeJournal) seed(exec string, state common.AttemptState, id, offering string) {
	f.rows = append(f.rows, common.AttemptRecord{IdempotencyToken: jToken, ExecutionID: exec, State: state, ReservationID: id, OfferingID: offering})
}

func (f *fakeJournal) PriorAttempts(_ context.Context, token string) ([]common.AttemptRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, journalCall{Method: "PriorAttempts"})
	if f.priorErr != nil {
		return nil, f.priorErr
	}
	var out []common.AttemptRecord
	for _, r := range f.rows {
		if r.IdempotencyToken == token {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeJournal) RecordAttempt(ctx context.Context, rec common.AttemptRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, journalCall{Method: "RecordAttempt", Rec: rec})
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.failOn[rec.State]; err != nil {
		return err
	}
	if err := rec.Validate(); err != nil {
		return err
	}
	i := f.find(rec.IdempotencyToken, rec.ExecutionID)
	var from *common.AttemptRecord
	if i >= 0 {
		from = &f.rows[i]
	}
	if !validTransition(from, rec) {
		return errors.New("fake journal: invalid transition")
	}
	if i < 0 {
		f.rows = append(f.rows, rec)
		return nil
	}
	rec.OfferingID = f.rows[i].OfferingID
	f.rows[i] = rec
	return nil
}

func (f *fakeJournal) recordCalls() []common.AttemptRecord {
	var out []common.AttemptRecord
	for _, c := range f.calls {
		if c.Method == "RecordAttempt" {
			out = append(out, c.Rec)
		}
	}
	return out
}

func (f *fakeJournal) row(t *testing.T, exec string) common.AttemptRecord {
	t.Helper()
	i := f.find(jToken, exec)
	require.GreaterOrEqual(t, i, 0, "no record for %s", exec)
	return f.rows[i]
}

// validTransition mirrors the table the AttemptJournal contract documents;
// a store implements the same table.
func validTransition(from *common.AttemptRecord, to common.AttemptRecord) bool {
	if from == nil {
		return to.State == common.AttemptSending || to.State == common.AttemptPurchased
	}
	switch from.State {
	case common.AttemptSending:
		return to.State == common.AttemptPurchased || to.State == common.AttemptOutcomeUnknown
	case common.AttemptOutcomeUnknown:
		return to.State == common.AttemptPurchased
	case common.AttemptPurchased:
		return to.State == common.AttemptPurchased && to.ReservationID == from.ReservationID
	}
	return false
}

func TestValidTransition(t *testing.T) {
	t.Parallel()
	S, U, P := common.AttemptSending, common.AttemptOutcomeUnknown, common.AttemptPurchased
	rec := func(s common.AttemptState, id string) common.AttemptRecord {
		return common.AttemptRecord{State: s, ReservationID: id}
	}
	none, sending, purchased, unknown := (*common.AttemptRecord)(nil), ptr(rec(S, "")), ptr(rec(P, "rn-1")), ptr(rec(U, ""))
	for _, tc := range []struct {
		name string
		from *common.AttemptRecord
		to   common.AttemptRecord
		want bool
	}{
		{"none to sending", none, rec(S, ""), true},
		{"none to unknown", none, rec(U, ""), false},
		{"none to purchased", none, rec(P, "rn-1"), true},
		{"none to purchased other id", none, rec(P, "rn-2"), true},
		{"sending to sending", sending, rec(S, ""), false},
		{"sending to unknown", sending, rec(U, ""), true},
		{"sending to purchased", sending, rec(P, "rn-1"), true},
		{"sending to purchased other id", sending, rec(P, "rn-2"), true},
		{"purchased to sending", purchased, rec(S, ""), false},
		{"purchased to unknown", purchased, rec(U, ""), false},
		{"purchased to purchased same id", purchased, rec(P, "rn-1"), true},
		{"purchased to purchased other id", purchased, rec(P, "rn-2"), false},
		{"unknown to sending", unknown, rec(S, ""), false},
		{"unknown to unknown", unknown, rec(U, ""), false},
		{"unknown to purchased", unknown, rec(P, "rn-1"), true},
		{"unknown to purchased other id", unknown, rec(P, "rn-2"), true},
	} {
		assert.Equal(t, tc.want, validTransition(tc.from, tc.to), tc.name)
	}
}

func ptr[T any](v T) *T { return &v }

func jOpts(j *fakeJournal, exec string) common.PurchaseOptions {
	return common.PurchaseOptions{IdempotencyToken: jToken, RequestedAt: dedupeReqAt, ExecutionID: exec, AttemptJournal: j}
}

func jWindow(ids ...string) []types.ReservedNode {
	nodes := make([]types.ReservedNode, 0, len(ids))
	for _, id := range ids {
		nodes = append(nodes, dnode(id, "active", dedupeReqAt.Add(time.Minute)))
	}
	return nodes
}

func jNoBuy(t *testing.T, m *MockRedshiftClient) {
	t.Helper()
	m.AssertNotCalled(t, "PurchaseReservedNodeOffering", mock.Anything, mock.Anything)
}

func transportErr() error {
	return &smithy.OperationError{ServiceID: "Redshift", OperationName: "PurchaseReservedNodeOffering", Err: errors.New("read: connection reset")}
}

func TestJournal_PriorErrorRefusesNoSend(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.priorErr = errors.New("db down")
	c, m := newDedupe(nil)
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.ErrorIs(t, err, common.ErrRefusedBeforePurchase)
	assert.False(t, result.Success)
	assert.Empty(t, j.recordCalls())
	jNoBuy(t, m)
}

func TestJournal_SendingWriteErrorRefusesNoSend(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.failOn[common.AttemptSending] = errors.New("db down")
	c, m := newDedupe(nil)
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.ErrorIs(t, err, common.ErrRefusedBeforePurchase)
	assert.False(t, result.Success)
	jNoBuy(t, m)
}

func TestJournal_PurchasedWriteErrorIsOutcomeUnknownWithID(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.failOn[common.AttemptPurchased] = errors.New("db down")
	c, m := newDedupe(nil)
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.Error(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "rn-new", result.CommitmentID)
	assert.Contains(t, err.Error(), "rn-new")
	assert.ErrorIs(t, err, common.ErrOutcomeUnknown)
	m.AssertNumberOfCalls(t, "PurchaseReservedNodeOffering", 1)
}

func TestJournal_BuyRecordsSendingThenPurchased(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	c, m := newDedupe(nil)
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.NoError(t, err)
	assert.True(t, result.Success)
	m.AssertNumberOfCalls(t, "PurchaseReservedNodeOffering", 1)
	assert.Equal(t, []common.AttemptRecord{
		{IdempotencyToken: jToken, ExecutionID: jExecNew, State: common.AttemptSending, OfferingID: jOffer},
		{IdempotencyToken: jToken, ExecutionID: jExecNew, State: common.AttemptPurchased, ReservationID: "rn-new", OfferingID: jOffer},
	}, j.recordCalls())
}

func TestJournal_RecordedIDAdoptsDespiteAmbiguousWindow(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.seed(jExecOld, common.AttemptPurchased, "rn-1", jOffer)
	c, m := newDedupe(jWindow("rn-1", "rn-2"))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.True(t, result.ExistingCommitment)
	assert.Equal(t, "rn-1", result.CommitmentID)
	assert.Empty(t, j.recordCalls())
	jNoBuy(t, m)
}

func TestJournal_RecordedIDNotMatchedRefuses(t *testing.T) {
	t.Parallel()
	for name, window := range map[string][]types.ReservedNode{"other node only": jWindow("rn-1"), "empty window": nil} {
		t.Run(name, func(t *testing.T) {
			j := newFakeJournal()
			j.seed(jExecOld, common.AttemptPurchased, "rn-9", jOffer)
			c, m := newDedupe(window)
			result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
			require.ErrorIs(t, err, common.ErrRefusedBeforePurchase)
			assert.False(t, result.Success)
			assert.NotEqual(t, "rn-1", result.CommitmentID)
			assert.Empty(t, j.recordCalls())
			jNoBuy(t, m)
		})
	}
}

func TestJournal_ConflictingRecordedIDsRefuses(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.seed("exec-a", common.AttemptPurchased, "rn-1", jOffer)
	j.seed("exec-b", common.AttemptPurchased, "rn-2", jOffer)
	c, m := newDedupe(jWindow("rn-1", "rn-2"))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.ErrorIs(t, err, common.ErrRefusedBeforePurchase)
	assert.Empty(t, result.CommitmentID)
	assert.Empty(t, j.recordCalls())
	jNoBuy(t, m)
}

func TestJournal_SameExecUnresolvedPriorRefusesNoSend(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.seed("exec-1", common.AttemptSending, "", jOffer)
	c, m := newDedupe(nil)
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, "exec-1"))
	require.ErrorIs(t, err, common.ErrOutcomeUnknown)
	assert.NotErrorIs(t, err, common.ErrRefusedBeforePurchase)
	assert.False(t, result.Success)
	assert.Empty(t, j.recordCalls())
	jNoBuy(t, m)
}

func TestJournal_OtherExecUnresolvedPriorBuys(t *testing.T) {
	t.Parallel()
	for _, state := range []common.AttemptState{common.AttemptSending, common.AttemptOutcomeUnknown} {
		t.Run(string(state), func(t *testing.T) {
			j := newFakeJournal()
			j.seed(jExecOld, state, "", jOffer)
			c, m := newDedupe(nil)
			result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
			require.NoError(t, err)
			assert.True(t, result.Success)
			m.AssertNumberOfCalls(t, "PurchaseReservedNodeOffering", 1)
			assert.Equal(t, common.AttemptPurchased, j.row(t, jExecNew).State)
			assert.Equal(t, "rn-new", j.row(t, jExecNew).ReservationID)
		})
	}
}

func TestJournal_OfferingChangedUnresolvedPriorRefuses(t *testing.T) {
	t.Parallel()
	for name, prior := range map[string]string{"different prior offering": "offering-0", "empty prior offering": ""} {
		t.Run(name, func(t *testing.T) {
			j := newFakeJournal()
			j.seed(jExecOld, common.AttemptSending, "", prior)
			c, m := newDedupe(nil)
			result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
			require.ErrorIs(t, err, common.ErrOutcomeUnknown)
			assert.NotErrorIs(t, err, common.ErrRefusedBeforePurchase)
			assert.False(t, result.Success)
			assert.Empty(t, j.recordCalls())
			jNoBuy(t, m)
		})
	}
}

func TestJournal_CrashAfterSendResolvedByWindow(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.seed(jExecOld, common.AttemptSending, "", jOffer)
	c, m := newDedupe(jWindow("rn-1"))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.NoError(t, err)
	assert.True(t, result.ExistingCommitment)
	assert.Equal(t, "rn-1", result.CommitmentID)
	jNoBuy(t, m)
	assert.Equal(t, common.AttemptPurchased, j.row(t, jExecNew).State)
	assert.Equal(t, "rn-1", j.row(t, jExecNew).ReservationID)
}

func TestJournal_UnknownThenAdoptRecordsPurchased(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.seed("exec-1", common.AttemptOutcomeUnknown, "", jOffer)
	c, m := newDedupe(jWindow("rn-1"))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, "exec-1"))
	require.NoError(t, err)
	assert.Equal(t, "rn-1", result.CommitmentID)
	jNoBuy(t, m)
	assert.Equal(t, common.AttemptPurchased, j.row(t, "exec-1").State)
	assert.Equal(t, "rn-1", j.row(t, "exec-1").ReservationID)
}

func TestJournal_AdoptWriteErrorNotSuccess(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.failOn[common.AttemptPurchased] = errors.New("db down")
	c, m := newDedupe(jWindow("rn-1"))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.Error(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "rn-1", result.CommitmentID)
	assert.False(t, result.ExistingCommitment)
	assert.Contains(t, err.Error(), "rn-1")
	assert.ErrorIs(t, err, common.ErrOutcomeUnknown)
	jNoBuy(t, m)
}

func TestJournal_PostSendAdoptWriteErrorNotSuccess(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.failOn[common.AttemptPurchased] = errors.New("db down")
	c, _ := recoverMock(buyFails(transportErr()), dnode("rn-1", "payment-pending", dedupeReqAt.Add(time.Second)))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.ErrorIs(t, err, common.ErrOutcomeUnknown)
	assert.False(t, result.Success)
	assert.False(t, result.ExistingCommitment)
	assert.Equal(t, "rn-1", result.CommitmentID)
	assert.Contains(t, err.Error(), "rn-1")
}

func TestJournal_AdoptKeepsFirstOfferingID(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.seed("exec-1", common.AttemptOutcomeUnknown, "", "offering-0")
	c, _ := newDedupe(jWindow("rn-1"))
	_, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, "exec-1"))
	require.NoError(t, err)
	row := j.row(t, "exec-1")
	assert.Equal(t, common.AttemptPurchased, row.State)
	assert.Equal(t, "offering-0", row.OfferingID)
}

func TestFakeJournalEnforcesTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rec := func(s common.AttemptState, id string) common.AttemptRecord {
		return common.AttemptRecord{IdempotencyToken: jToken, ExecutionID: "e", State: s, ReservationID: id}
	}
	j := newFakeJournal()
	require.NoError(t, j.RecordAttempt(ctx, rec(common.AttemptSending, "")))
	require.Error(t, j.RecordAttempt(ctx, rec(common.AttemptSending, "")))
	j = newFakeJournal()
	require.NoError(t, j.RecordAttempt(ctx, rec(common.AttemptPurchased, "rn-1")))
	require.Error(t, j.RecordAttempt(ctx, rec(common.AttemptPurchased, "rn-2")))
}

func TestJournal_CancelledCtxAfterSendStillRecords(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &MockRedshiftClient{}
	expectRSOffering(m)
	expectNodePages(m, nil)
	m.On("PurchaseReservedNodeOffering", mock.Anything, mock.Anything).Run(func(mock.Arguments) { cancel() }).
		Return(&redshift.PurchaseReservedNodeOfferingOutput{ReservedNode: &types.ReservedNode{ReservedNodeId: aws.String("rn-new")}}, nil)
	c := &Client{client: m, region: "eu-west-1"}
	result, err := c.PurchaseCommitment(ctx, rsIdemRec(), jOpts(j, jExecNew))
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, common.AttemptPurchased, j.row(t, jExecNew).State)
}

func TestJournal_UnknownOutcomeRecorded(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	c, m := recoverMock(buyFails(transportErr()))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.ErrorIs(t, err, common.ErrOutcomeUnknown)
	assert.False(t, result.Success)
	m.AssertNumberOfCalls(t, "PurchaseReservedNodeOffering", 1)
	assert.Equal(t, common.AttemptOutcomeUnknown, j.row(t, jExecNew).State)
}

func TestJournal_PostSendAdoptRecordsPurchased(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	c, m := recoverMock(buyFails(transportErr()), dnode("rn-1", "payment-pending", dedupeReqAt.Add(time.Second)))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "rn-1", result.CommitmentID)
	m.AssertNumberOfCalls(t, "DescribeReservedNodes", 2)
	recs := j.recordCalls()
	require.Len(t, recs, 2)
	assert.Equal(t, common.AttemptSending, recs[0].State)
	assert.Equal(t, common.AttemptPurchased, recs[1].State)
	assert.Equal(t, "rn-1", recs[1].ReservationID)
}

func TestJournal_AmbiguousResponseRecordedUnknown(t *testing.T) {
	t.Parallel()
	t.Run("empty 200", func(t *testing.T) {
		j := newFakeJournal()
		c, _ := recoverMock(buyEmpty)
		_, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
		require.ErrorIs(t, err, common.ErrOutcomeUnknown)
		assert.Equal(t, common.AttemptOutcomeUnknown, j.row(t, jExecNew).State)
	})
	t.Run("definite 4xx", func(t *testing.T) {
		j := newFakeJournal()
		c, _ := recoverMock(buyFails(clean4xx()))
		_, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
		require.Error(t, err)
		assert.NotErrorIs(t, err, common.ErrOutcomeUnknown)
		assert.Equal(t, common.AttemptOutcomeUnknown, j.row(t, jExecNew).State)
	})
}

func TestJournal_UnknownWriteErrorStillOutcomeUnknown(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.failOn[common.AttemptOutcomeUnknown] = errors.New("db down")
	c, _ := recoverMock(buyFails(transportErr()))
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, jExecNew))
	require.ErrorIs(t, err, common.ErrOutcomeUnknown)
	assert.Contains(t, err.Error(), "db down")
	assert.False(t, result.Success)
}

func TestJournal_UpfrontRefusalWritesNothing(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	m := guardMock(1000, "")
	expectNodePages(m, nil)
	c := &Client{client: m, region: "eu-west-1"}
	rec := rsIdemRec()
	rec.CommitmentCost = 10
	_, err := c.PurchaseCommitment(context.Background(), rec, jOpts(j, jExecNew))
	require.ErrorIs(t, err, common.ErrRefusedBeforePurchase)
	assert.Empty(t, j.recordCalls())
	jNoBuy(t, m)
}

func TestJournal_ZeroRequestedAtRefusesBeforeJournal(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.seed(jExecOld, common.AttemptPurchased, "rn-1", jOffer)
	c, m := newDedupe(jWindow("rn-1"))
	opts := jOpts(j, jExecNew)
	opts.RequestedAt = time.Time{}
	_, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), opts)
	require.ErrorIs(t, err, errRequestedAtRequired)
	assert.Empty(t, j.calls)
	jNoBuy(t, m)
}

func TestJournal_EmptyTokenSkipsJournal(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	c, m := newDedupe(nil)
	result, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), common.PurchaseOptions{AttemptJournal: j})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Empty(t, j.calls)
	m.AssertNumberOfCalls(t, "PurchaseReservedNodeOffering", 1)
}

func TestJournal_RequiresExecutionID(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	c, m := newDedupe(nil)
	_, err := c.PurchaseCommitment(context.Background(), rsIdemRec(), jOpts(j, ""))
	require.ErrorIs(t, err, common.ErrRefusedBeforePurchase)
	assert.Empty(t, j.calls)
	jNoBuy(t, m)
}
