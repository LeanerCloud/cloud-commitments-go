package recommendations

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetSPUtilizationForAccount_FilterShape(t *testing.T) {
	t.Parallel()
	const acct = "123456789012"
	for _, tc := range []struct {
		name    string
		plan    types.SupportedSavingsPlansType
		region  string
		wantLen int
		keys    []types.Dimension
	}{
		{"compute no region", types.SupportedSavingsPlansTypeComputeSp, "", 2,
			[]types.Dimension{types.DimensionSavingsPlansType, types.DimensionLinkedAccount}},
		{"ec2 with region", types.SupportedSavingsPlansTypeEc2InstanceSp, "us-west-2", 3,
			[]types.Dimension{types.DimensionSavingsPlansType, types.DimensionRegion, types.DimensionLinkedAccount}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mock := &mockSPCE{}
			client := NewClientWithAPI(mock, "us-east-1")
			_, err := client.GetSPUtilizationForAccount(context.Background(), tc.plan, acct, tc.region, 30)
			require.NoError(t, err)
			require.Len(t, mock.utilInputs, 1)
			f := mock.utilInputs[0].Filter
			require.NotNil(t, f)
			assert.Nil(t, f.Dimensions, "account scope must be an And, never a bare dimension")
			require.Len(t, f.And, tc.wantLen)
			for i, k := range tc.keys {
				require.NotNil(t, f.And[i].Dimensions)
				assert.Equal(t, k, f.And[i].Dimensions.Key)
			}
			last := f.And[len(f.And)-1].Dimensions
			assert.Equal(t, []string{acct}, last.Values)
		})
	}
}

func TestGetSPUtilizationForAccount_RejectsEmptyAccount(t *testing.T) {
	t.Parallel()
	mock := &mockSPCE{}
	client := NewClientWithAPI(mock, "us-east-1")
	_, err := client.GetSPUtilizationForAccount(context.Background(), types.SupportedSavingsPlansTypeComputeSp, "", "", 30)
	require.Error(t, err)
	assert.Empty(t, mock.utilInputs)
}

func TestSPUtilizationFilterForAccount_EmptyAccountKeepsLegacyShape(t *testing.T) {
	t.Parallel()
	got, err := spUtilizationFilterForAccount(types.SupportedSavingsPlansTypeEc2InstanceSp, "", "us-west-2")
	require.NoError(t, err)
	want, err := spUtilizationFilter(types.SupportedSavingsPlansTypeEc2InstanceSp, "us-west-2")
	require.NoError(t, err)
	assert.Equal(t, want, got)
}
