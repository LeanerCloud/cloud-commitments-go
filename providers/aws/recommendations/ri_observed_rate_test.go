package recommendations

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pagedUtilCE sequences GetReservationUtilization pages by NextPageToken and
// records each request.
type pagedUtilCE struct {
	mockCostExplorerAPI
	pages  []*costexplorer.GetReservationUtilizationOutput
	inputs []*costexplorer.GetReservationUtilizationInput
}

func (m *pagedUtilCE) GetReservationUtilization(_ context.Context, in *costexplorer.GetReservationUtilizationInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetReservationUtilizationOutput, error) {
	cp := *in // the client reuses one input across pages
	m.inputs = append(m.inputs, &cp)
	if len(m.inputs) > len(m.pages) {
		return nil, fmt.Errorf("unexpected call %d", len(m.inputs))
	}
	return m.pages[len(m.inputs)-1], nil
}

func feeGroup(id string, fee, od, purchased, actual *string) types.ReservationUtilizationGroup {
	return types.ReservationUtilizationGroup{
		Key: aws.String(id),
		Utilization: &types.ReservationAggregates{
			TotalAmortizedFee: fee, OnDemandCostOfRIHoursUsed: od,
			PurchasedHours: purchased, TotalActualHours: actual,
		},
	}
}

func feePage(next *string, groups ...types.ReservationUtilizationGroup) *costexplorer.GetReservationUtilizationOutput {
	return &costexplorer.GetReservationUtilizationOutput{
		UtilizationsByTime: []types.UtilizationByTime{{Groups: groups}},
		NextPageToken:      next,
	}
}

func TestGetRIFeeUtilization_FilterAndPagination(t *testing.T) {
	t.Parallel()
	mock := &pagedUtilCE{pages: []*costexplorer.GetReservationUtilizationOutput{
		feePage(aws.String("tok"), feeGroup("ri-1", aws.String("10"), aws.String("12"), aws.String("100"), aws.String("50"))),
		feePage(nil, feeGroup("ri-1", aws.String("5"), aws.String("6"), aws.String("100"), aws.String("40")),
			feeGroup("ri-2", aws.String("1"), aws.String("2"), aws.String("24"), aws.String("12"))),
	}}
	client := NewClientWithAPI(mock, "us-east-1")
	got, err := client.GetRIFeeUtilization(context.Background(), "123456789012", "eu-west-1", 30)
	require.NoError(t, err)
	require.Len(t, mock.inputs, 2)
	assert.Nil(t, mock.inputs[0].NextPageToken)
	assert.Equal(t, "tok", aws.ToString(mock.inputs[1].NextPageToken), "second call must carry the token")

	f := mock.inputs[0].Filter
	require.NotNil(t, f)
	assert.Nil(t, f.Dimensions)
	require.Len(t, f.And, 3)
	assert.Equal(t, types.DimensionService, f.And[0].Dimensions.Key)
	assert.Equal(t, types.DimensionRegion, f.And[1].Dimensions.Key)
	assert.Equal(t, []string{"eu-west-1"}, f.And[1].Dimensions.Values)
	assert.Equal(t, types.DimensionLinkedAccount, f.And[2].Dimensions.Key)
	assert.Equal(t, []string{"123456789012"}, f.And[2].Dimensions.Values)

	byID := map[string]RIFeeUtilization{}
	for _, u := range got {
		byID[u.ReservedInstanceID] = u
	}
	require.Len(t, byID, 2)
	assert.Equal(t, RIFeeUtilization{ReservedInstanceID: "ri-1", TotalAmortizedFee: 15, OnDemandCostOfRIHoursUsed: 18, PurchasedHours: 200, TotalActualHours: 90}, byID["ri-1"])
	assert.Equal(t, 24.0, byID["ri-2"].PurchasedHours)
}

func TestGetRIFeeUtilization_EmptyRegionStillAnd(t *testing.T) {
	t.Parallel()
	mock := &pagedUtilCE{pages: []*costexplorer.GetReservationUtilizationOutput{feePage(nil)}}
	_, err := NewClientWithAPI(mock, "us-east-1").GetRIFeeUtilization(context.Background(), "123456789012", "", 30)
	require.NoError(t, err)
	f := mock.inputs[0].Filter
	assert.Nil(t, f.Dimensions)
	require.Len(t, f.And, 2)
	assert.Equal(t, types.DimensionLinkedAccount, f.And[1].Dimensions.Key)
}

func TestGetRIFeeUtilization_Errors(t *testing.T) {
	t.Parallel()
	s := aws.String
	for _, tc := range []struct {
		name  string
		acct  string
		days  int
		group types.ReservationUtilizationGroup
	}{
		{"empty account", "", 30, feeGroup("r", s("1"), s("1"), s("1"), s("1"))},
		{"lookback zero", "a", 0, feeGroup("r", s("1"), s("1"), s("1"), s("1"))},
		{"missing fee", "a", 30, feeGroup("r", nil, s("1"), s("1"), s("1"))},
		{"missing od", "a", 30, feeGroup("r", s("1"), nil, s("1"), s("1"))},
		{"missing purchased", "a", 30, feeGroup("r", s("1"), s("1"), nil, s("1"))},
		{"missing actual", "a", 30, feeGroup("r", s("1"), s("1"), s("1"), nil)},
		{"negative fee", "a", 30, feeGroup("r", s("-1"), s("1"), s("1"), s("1"))},
		{"nan od", "a", 30, feeGroup("r", s("1"), s("NaN"), s("1"), s("1"))},
		{"negative hours", "a", 30, feeGroup("r", s("1"), s("1"), s("-1"), s("1"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mock := &pagedUtilCE{pages: []*costexplorer.GetReservationUtilizationOutput{feePage(nil, tc.group)}}
			got, err := NewClientWithAPI(mock, "us-east-1").GetRIFeeUtilization(context.Background(), tc.acct, "r", tc.days)
			require.Error(t, err)
			assert.Nil(t, got)
		})
	}
}

func TestGetRIFeeUtilization_AllAbsentGroupSkipped(t *testing.T) {
	t.Parallel()
	mock := &pagedUtilCE{pages: []*costexplorer.GetReservationUtilizationOutput{
		feePage(nil, feeGroup("empty", nil, nil, nil, nil),
			feeGroup("ok", aws.String("1"), aws.String("2"), aws.String("3"), aws.String("1"))),
	}}
	got, err := NewClientWithAPI(mock, "us-east-1").GetRIFeeUtilization(context.Background(), "a", "r", 30)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "ok", got[0].ReservedInstanceID)
}
