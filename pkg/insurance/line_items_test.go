package insurance

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lineItemJSON() string {
	return `{"id":"22222222-2222-4222-8222-222222222222","plan_id":"` + testPlan + `","offer_id":"11111111-1111-4111-8111-111111111111","account_id":"opaque-account","selected_quantity":2,"selected_commitment":0.1234567890123456789,"is_selected":true,"contract_term":"one_year","payment_option":"No Upfront","contract_spec":{"commitment_type":"aws/AmazonEC2","term":"one_year","payment_option":"no_upfront"},"offer":{"id":"11111111-1111-4111-8111-111111111111","type":"VendorOfferType","provider":"aws","provider_offering_id":"provider-offer","offering_id":"catalog-offer","duration_seconds":31536000.25,"region":"us-east-1","az":null,"instance_type":"m5.large","instance_family":"m5","product_description":"Linux/UNIX","tenancy":"default","plan_type":null,"is_multi_az":false,"is_flexible":null,"offering_class":"standard","payment_option":"No Upfront"}}`
}

func editLineItem(t *testing.T, edit func(map[string]any)) string {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(lineItemJSON()), &m))
	edit(m)
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return string(b)
}

func TestDecodeLineItemsIdentity(t *testing.T) {
	t.Parallel()
	rows, err := DecodeLineItems(strings.NewReader("["+lineItemJSON()+"]"), testPlan)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	r := rows[0]
	assert.Equal(t, "opaque-account", *r.AccountID)
	assert.Equal(t, "provider-offer", r.Offer.ProviderOfferingID)
	assert.Equal(t, "VendorOfferType", r.Offer.Type)
	assert.Equal(t, "aws/AmazonEC2", r.ContractSpec.CommitmentType)
	assert.Equal(t, "catalog-offer", *r.Offer.OfferingID)
	assert.Equal(t, rat(t, "31536000.25"), r.Offer.DurationSeconds)
	assert.Equal(t, rat(t, "0.1234567890123456789"), r.SelectedCommitment)
	assert.EqualValues(t, 2, r.SelectedQuantity)
	assert.Equal(t, PaymentNoUpfront, r.PaymentOption)
	assert.Equal(t, PaymentNoUpfront, *r.Offer.PaymentOption)
	assert.Equal(t, "one_year", *r.ContractSpec.Term)
	assert.False(t, *r.Offer.IsMultiAZ)
	assert.Nil(t, r.Offer.IsFlexible)
	assert.Nil(t, r.Offer.AZ)
	assert.Equal(t, "m5.large", *r.Offer.InstanceType)
	assert.Equal(t, "m5", *r.Offer.InstanceFamily)
	assert.Equal(t, "default", *r.Offer.Tenancy)
	assert.Equal(t, "Linux/UNIX", *r.Offer.ProductDescription)
	assert.Equal(t, "us-east-1", *r.Offer.Region)
	assert.Nil(t, r.Offer.PlanType)
	assert.True(t, r.IsSelected)
}

func TestDecodeLineItemsPlanTypeIdentity(t *testing.T) {
	t.Parallel()
	body := editLineItem(t, func(m map[string]any) { obj(m["offer"])["plan_type"] = "compute" })
	rows, err := DecodeLineItems(strings.NewReader("["+body+"]"), testPlan)
	require.NoError(t, err)
	assert.Equal(t, "compute", *rows[0].Offer.PlanType)
}

func TestDecodeLineItemsRequiredFields(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"id", "plan_id", "offer_id", "account_id", "selected_quantity", "selected_commitment", "is_selected", "contract_term", "payment_option", "offer", "contract_spec"} {
		t.Run(key, func(t *testing.T) {
			body := editLineItem(t, func(m map[string]any) { delete(m, key) })
			_, err := DecodeLineItems(strings.NewReader("["+body+"]"), testPlan)
			require.ErrorContains(t, err, key)
		})
	}
	for _, key := range []string{"id", "provider", "provider_offering_id", "offering_id", "duration_seconds", "region", "az", "instance_type", "instance_family", "product_description", "tenancy", "plan_type", "is_multi_az", "is_flexible"} {
		t.Run("offer."+key, func(t *testing.T) {
			body := editLineItem(t, func(m map[string]any) { delete(obj(m["offer"]), key) })
			_, err := DecodeLineItems(strings.NewReader("["+body+"]"), testPlan)
			require.ErrorContains(t, err, key)
		})
	}
}

func TestDecodeLineItemsContractBreaks(t *testing.T) {
	t.Parallel()
	cases := map[string]func(map[string]any){
		"null id":                  func(m map[string]any) { m["id"] = nil },
		"wrong plan":               func(m map[string]any) { m["plan_id"] = testOrg },
		"invalid UUID":             func(m map[string]any) { m["offer_id"] = "opaque-secret" },
		"offer mismatch":           func(m map[string]any) { obj(m["offer"])["id"] = testPlan },
		"null provider":            func(m map[string]any) { obj(m["offer"])["provider"] = nil },
		"unknown provider":         func(m map[string]any) { obj(m["offer"])["provider"] = "opaque-secret" },
		"unknown term":             func(m map[string]any) { m["contract_term"] = "opaque-secret" },
		"unknown payment":          func(m map[string]any) { m["payment_option"] = "no_upfront" },
		"unknown class":            func(m map[string]any) { obj(m["offer"])["offering_class"] = "opaque-secret" },
		"unknown offer payment":    func(m map[string]any) { obj(m["offer"])["payment_option"] = "opaque-secret" },
		"negative quantity":        func(m map[string]any) { m["selected_quantity"] = -1 },
		"fractional quantity":      func(m map[string]any) { m["selected_quantity"] = 0.5 },
		"negative commitment":      func(m map[string]any) { m["selected_commitment"] = -0.1 },
		"null commitment":          func(m map[string]any) { m["selected_commitment"] = nil },
		"empty provider offering":  func(m map[string]any) { obj(m["offer"])["provider_offering_id"] = "" },
		"zero duration":            func(m map[string]any) { obj(m["offer"])["duration_seconds"] = 0 },
		"negative duration":        func(m map[string]any) { obj(m["offer"])["duration_seconds"] = -1 },
		"null duration":            func(m map[string]any) { obj(m["offer"])["duration_seconds"] = nil },
		"null is_selected":         func(m map[string]any) { m["is_selected"] = nil },
		"quoted duration":          func(m map[string]any) { obj(m["offer"])["duration_seconds"] = "123" },
		"null contract":            func(m map[string]any) { m["contract_spec"] = nil },
		"empty commitment type":    func(m map[string]any) { obj(m["contract_spec"])["commitment_type"] = "" },
		"invalid contract term":    func(m map[string]any) { obj(m["contract_spec"])["term"] = "opaque-secret" },
		"invalid contract payment": func(m map[string]any) { obj(m["contract_spec"])["payment_option"] = "opaque-secret" },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeLineItems(strings.NewReader("["+editLineItem(t, edit)+"]"), testPlan)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "opaque-secret")
		})
	}
	for _, body := range []string{"null", "{}", "[null]", "[" + lineItemJSON() + "," + lineItemJSON() + "]", "[] {}"} {
		_, err := DecodeLineItems(strings.NewReader(body), testPlan)
		require.Error(t, err)
	}
}

func TestDecodeLineItemsNullableIdentity(t *testing.T) {
	t.Parallel()
	for _, flag := range []any{nil, false, true} {
		body := editLineItem(t, func(m map[string]any) {
			m["account_id"] = nil
			m["contract_spec"] = map[string]any{"commitment_type": "opaque-type"}
			for _, k := range []string{"offering_id", "region", "az", "instance_type", "instance_family", "product_description", "tenancy", "plan_type", "offering_class", "payment_option"} {
				obj(m["offer"])[k] = nil
			}
			obj(m["offer"])["is_multi_az"] = flag
			obj(m["offer"])["is_flexible"] = flag
		})
		rows, err := DecodeLineItems(strings.NewReader("["+body+"]"), testPlan)
		require.NoError(t, err)
		assert.Nil(t, rows[0].AccountID)
		assert.Nil(t, rows[0].Offer.OfferingID)
		if flag == nil {
			assert.Nil(t, rows[0].Offer.IsFlexible)
		} else {
			assert.Equal(t, flag, *rows[0].Offer.IsFlexible)
		}
	}
}

func TestDecodeLineItemsRequiresOfferType(t *testing.T) {
	t.Parallel()
	for _, edit := range []func(map[string]any){
		func(m map[string]any) { delete(obj(m["offer"]), "type") },
		func(m map[string]any) { obj(m["offer"])["type"] = nil },
		func(m map[string]any) { obj(m["offer"])["type"] = "" },
	} {
		_, err := DecodeLineItems(strings.NewReader("["+editLineItem(t, edit)+"]"), testPlan)
		require.ErrorContains(t, err, "type")
	}
}

func TestDecodeLineItemsUUIDCaseIdentity(t *testing.T) {
	t.Parallel()
	const plan = "abcdef01-2345-4678-89ab-cdef01234567"
	const offer = "abcdef02-2345-4678-89ab-cdef01234567"
	body := editLineItem(t, func(m map[string]any) {
		m["plan_id"] = strings.ToUpper(plan)
		m["offer_id"] = strings.ToUpper(offer)
		obj(m["offer"])["id"] = offer
		m["account_id"] = "AccountAbC"
		obj(m["offer"])["provider_offering_id"] = "ProviderOfferAbC"
		obj(m["offer"])["offering_id"] = "CatalogOfferAbC"
	})
	rows, err := DecodeLineItems(strings.NewReader("["+body+"]"), plan)
	require.NoError(t, err)
	assert.Equal(t, strings.ToUpper(plan), rows[0].PlanID)
	assert.Equal(t, strings.ToUpper(offer), rows[0].OfferID)
	assert.Equal(t, offer, rows[0].Offer.ID)
	assert.Equal(t, "AccountAbC", *rows[0].AccountID)
	assert.Equal(t, "ProviderOfferAbC", rows[0].Offer.ProviderOfferingID)
	assert.Equal(t, "CatalogOfferAbC", *rows[0].Offer.OfferingID)
}

func TestDecodeLineItemsRejectsCaseEquivalentDuplicates(t *testing.T) {
	t.Parallel()
	const id = "abcdef03-2345-4678-89ab-cdef01234567"
	lower := strings.Replace(lineItemJSON(), testLine, id, 1)
	upper := strings.Replace(lineItemJSON(), testLine, strings.ToUpper(id), 1)
	rows, err := DecodeLineItems(strings.NewReader("["+lower+","+upper+"]"), testPlan)
	require.ErrorContains(t, err, "duplicate")
	assert.Nil(t, rows)
}
