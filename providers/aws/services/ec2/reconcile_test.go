package ec2

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/purchasecfg"
)

var reconcileT0 = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

func reconcileRec() common.Recommendation {
	return common.Recommendation{
		Region: "us-east-1", ResourceType: "m5.large", Count: 3, PaymentOption: "no-upfront", Term: "1yr",
		Details: &common.ComputeDetails{Platform: "Linux/UNIX", Tenancy: "default", Scope: "Region"},
	}
}

func reconcileReq(mut ...func(*common.ReconcileRequest)) common.ReconcileRequest {
	req := common.ReconcileRequest{
		Recommendation: reconcileRec(),
		Options:        common.PurchaseOptions{RequestedAt: reconcileT0, IdempotencyToken: "tok-1", OfferingClass: "standard"},
		AttemptEndedAt: reconcileT0.Add(time.Minute),
	}
	for _, m := range mut {
		m(&req)
	}
	return req
}

func reconcileRI(id string, mut ...func(*types.ReservedInstances)) types.ReservedInstances {
	ri := types.ReservedInstances{
		ReservedInstancesId: aws.String(id), InstanceType: types.InstanceTypeM5Large, InstanceCount: aws.Int32(3),
		ProductDescription: types.RIProductDescriptionLinuxUnix, InstanceTenancy: types.TenancyDefault,
		Scope: types.ScopeRegional, Duration: aws.Int64(OneYearSeconds), OfferingType: types.OfferingTypeValuesNoUpfront,
		OfferingClass: types.OfferingClassTypeStandard, Start: aws.Time(reconcileT0.Add(30 * time.Second)),
		State: types.ReservedInstanceStateActive,
	}
	for _, m := range mut {
		m(&ri)
	}
	return ri
}

func riTag(v string) func(*types.ReservedInstances) {
	return func(ri *types.ReservedInstances) {
		ri.Tags = append(ri.Tags, types.Tag{Key: aws.String(common.IdempotencyTagKey), Value: aws.String(v)})
	}
}

func reconcileClient(ris []types.ReservedInstances, descErr error) (*Client, *MockEC2Client) {
	m := &MockEC2Client{}
	if ris != nil || descErr != nil {
		call := m.On("DescribeReservedInstances", mock.Anything, mock.Anything)
		if descErr != nil {
			call.Return(nil, descErr)
		} else {
			call.Return(&ec2.DescribeReservedInstancesOutput{ReservedInstances: ris}, nil)
		}
	}
	return &Client{client: m, region: "us-east-1"}, m
}

func TestReconcilePurchase(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	future := reconcileRI("ri-q", func(ri *types.ReservedInstances) {
		ri.State = types.ReservedInstanceStateQueued
		ri.Start = aws.Time(reconcileT0.Add(24 * time.Hour))
	})

	tests := []struct {
		name         string
		ctx          context.Context
		ris          []types.ReservedInstances
		descErr      error
		req          common.ReconcileRequest
		wantDescribe bool
		verdict      common.ReconcileVerdict
		id, reason   string
		tagged       bool
		candidates   []string
	}{
		{name: "one untagged match", ris: []types.ReservedInstances{reconcileRI("ri-1")}, req: reconcileReq(),
			wantDescribe: true, verdict: common.ReconcileFound, id: "ri-1"},
		{name: "one same-token tagged match", ris: []types.ReservedInstances{reconcileRI("ri-1", riTag("tok-1"))}, req: reconcileReq(),
			wantDescribe: true, verdict: common.ReconcileFound, id: "ri-1", tagged: true},
		{name: "default class is convertible", ris: []types.ReservedInstances{reconcileRI("ri-1", func(ri *types.ReservedInstances) { ri.OfferingClass = types.OfferingClassTypeConvertible })},
			req:          reconcileReq(func(r *common.ReconcileRequest) { r.Options.OfferingClass = "" }),
			wantDescribe: true, verdict: common.ReconcileFound, id: "ri-1"},
		{name: "convertible RI does not match standard request", ris: []types.ReservedInstances{reconcileRI("ri-1", func(ri *types.ReservedInstances) { ri.OfferingClass = types.OfferingClassTypeConvertible })},
			req: reconcileReq(), wantDescribe: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonNoCandidate},
		{name: "two untagged matches", ris: []types.ReservedInstances{reconcileRI("ri-1"), reconcileRI("ri-2")}, req: reconcileReq(),
			wantDescribe: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonAmbiguous, candidates: []string{"ri-1", "ri-2"}},
		{name: "match plus other-lineage tagged same-spec RI", ris: []types.ReservedInstances{reconcileRI("ri-1"), reconcileRI("ri-2", riTag("other"))}, req: reconcileReq(),
			wantDescribe: true, verdict: common.ReconcileFound, id: "ri-1"},
		{name: "match plus retired same-spec RI", ris: []types.ReservedInstances{reconcileRI("ri-1"), reconcileRI("ri-2", func(ri *types.ReservedInstances) { ri.State = types.ReservedInstanceStateRetired })}, req: reconcileReq(),
			wantDescribe: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonUncertainCandidate, candidates: []string{"ri-1", "ri-2"}},
		{name: "empty list", ris: []types.ReservedInstances{}, req: reconcileReq(),
			wantDescribe: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonNoCandidate},
		{name: "queued RI with future start", ris: []types.ReservedInstances{future}, req: reconcileReq(),
			wantDescribe: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonNoCandidate},
		{name: "describe error", descErr: errors.New("boom"), req: reconcileReq(),
			wantDescribe: true, verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonLookupFailed},
		{name: "canceled context", ctx: canceled, ris: []types.ReservedInstances{reconcileRI("ri-1")}, req: reconcileReq(),
			verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonLookupFailed},
		{name: "bad offering class", req: reconcileReq(func(r *common.ReconcileRequest) { r.Options.OfferingClass = "bogus" }),
			verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonInvalidRequest},
		{name: "region mismatch", req: reconcileReq(func(r *common.ReconcileRequest) { r.Recommendation.Region = "eu-west-1" }),
			verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonInvalidRequest},
		{name: "zero count", req: reconcileReq(func(r *common.ReconcileRequest) { r.Recommendation.Count = 0 }),
			verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonInvalidRequest},
		{name: "unparsable payment option", req: reconcileReq(func(r *common.ReconcileRequest) { r.Recommendation.PaymentOption = "bogus" }),
			verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonInvalidRequest},
		{name: "zero RequestedAt", req: reconcileReq(func(r *common.ReconcileRequest) { r.Options.RequestedAt = time.Time{} }),
			verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonInvalidWindow},
		{name: "zero AttemptEndedAt", req: reconcileReq(func(r *common.ReconcileRequest) { r.AttemptEndedAt = time.Time{} }),
			verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonInvalidWindow},
		{name: "end before start", req: reconcileReq(func(r *common.ReconcileRequest) { r.AttemptEndedAt = reconcileT0.Add(-time.Hour) }),
			verdict: common.ReconcileUnknown, reason: purchasecfg.ReasonInvalidWindow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, m := reconcileClient(tt.ris, tt.descErr)
			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			got := c.ReconcilePurchase(ctx, tt.req)

			assert.Equal(t, tt.verdict, got.Verdict)
			assert.Equal(t, tt.id, got.CommitmentID)
			assert.Equal(t, tt.reason, got.Reason)
			assert.Equal(t, tt.tagged, got.Tagged)
			assert.Equal(t, tt.candidates, got.CandidateIDs)
			if tt.verdict == common.ReconcileFound {
				assert.NotEmpty(t, got.Criteria)
			}
			if tt.wantDescribe {
				m.AssertNumberOfCalls(t, "DescribeReservedInstances", 1)
			} else {
				m.AssertNotCalled(t, "DescribeReservedInstances", mock.Anything, mock.Anything)
			}
			m.AssertNotCalled(t, "PurchaseReservedInstancesOffering", mock.Anything, mock.Anything)
			m.AssertNotCalled(t, "CreateTags", mock.Anything, mock.Anything)
		})
	}
}

// The purchase returns the empty-200 outcome-unknown and the RI was never tagged: the token lookup
// cannot see it, reconcile can.
func TestReconcilePurchase_LostResponseUntaggedRI(t *testing.T) {
	t.Parallel()
	m := &MockEC2Client{}
	c := &Client{client: m, region: "us-east-1"}
	untagged := reconcileRI("ri-lost")
	m.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{ReservedInstancesOfferings: []types.ReservedInstancesOffering{{
			ReservedInstancesOfferingId: aws.String("offering-1"), InstanceType: types.InstanceTypeM5Large,
			Duration: aws.Int64(OneYearSeconds), OfferingType: types.OfferingTypeValuesNoUpfront,
			ProductDescription: types.RIProductDescriptionLinuxUnix, InstanceTenancy: types.TenancyDefault,
			OfferingClass: types.OfferingClassTypeStandard,
		}}}, nil)
	m.On("PurchaseReservedInstancesOffering", mock.Anything, mock.Anything).
		Return(&ec2.PurchaseReservedInstancesOfferingOutput{}, nil)
	byToken := func(in *ec2.DescribeReservedInstancesInput) bool { return len(in.Filters) > 0 }
	m.On("DescribeReservedInstances", mock.Anything, mock.MatchedBy(byToken)).
		Return(&ec2.DescribeReservedInstancesOutput{}, nil)
	m.On("DescribeReservedInstances", mock.Anything, mock.MatchedBy(func(in *ec2.DescribeReservedInstancesInput) bool { return !byToken(in) })).
		Return(&ec2.DescribeReservedInstancesOutput{ReservedInstances: []types.ReservedInstances{untagged}}, nil)

	req := reconcileReq()
	_, err := c.PurchaseCommitment(context.Background(), req.Recommendation, req.Options)
	require.ErrorIs(t, err, common.ErrOutcomeUnknown)

	_, found, err := c.findRIByIdempotencyToken(context.Background(), req.Options.IdempotencyToken)
	require.NoError(t, err)
	assert.False(t, found, "the token lookup alone misses an untagged RI")

	got := c.ReconcilePurchase(context.Background(), req)
	assert.True(t, got.IsFound())
	assert.Equal(t, "ri-lost", got.CommitmentID)
	assert.False(t, got.Tagged)
}
