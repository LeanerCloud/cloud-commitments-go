package savingsplans

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/billingbenefits/armbillingbenefits"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func TestPurchaseCommitment_BillingPlanOnAliasBody(t *testing.T) {
	t.Parallel()
	p1m := armbillingbenefits.BillingPlanP1M
	cases := []struct {
		option string
		want   *armbillingbenefits.BillingPlan
	}{
		{"monthly", &p1m},
		{"no-upfront", &p1m},
		{"upfront", nil},
		{"all-upfront", nil},
	}
	for _, tc := range cases {
		t.Run(tc.option, func(t *testing.T) {
			t.Parallel()
			rec := makeRec("1yr")
			rec.PaymentOption = common.PaymentOption(tc.option)
			alias := okAlias()
			c := NewClient(nil, "sub", "eastus")
			c.SetOrderAliasClient(alias)

			_, err := c.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})
			require.NoError(t, err)
			require.NotNil(t, alias.capturedBody.Properties)
			assert.Equal(t, tc.want, alias.capturedBody.Properties.BillingPlan)
		})
	}
}

func TestPurchaseCommitment_UnsupportedPaymentOptionFailsBeforePUT(t *testing.T) {
	t.Parallel()
	for _, option := range []string{"", "partial-upfront", "weekly"} {
		t.Run("option="+option, func(t *testing.T) {
			t.Parallel()
			rec := makeRec("1yr")
			rec.PaymentOption = common.PaymentOption(option)
			alias := okAlias()
			c := NewClient(nil, "sub", "eastus")
			c.SetOrderAliasClient(alias)

			res, err := c.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})
			require.Error(t, err)
			assert.False(t, res.Success)
			assert.Contains(t, err.Error(), "payment option")
			assert.Zero(t, alias.calls)
		})
	}
}
