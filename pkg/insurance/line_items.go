package insurance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// LineItem retains purchase identity without inferring missing account or offer attributes.
// ID, PlanID and OfferID keep the vendor's case; compare UUIDs case-insensitively
// (strings.EqualFold) when joining to comparison line_item_id values.
type LineItem struct {
	Offer               LineItemOffer
	ContractSpec        LineItemContractSpec
	AccountID           *string
	SelectedCommitment  *big.Rat
	ID, PlanID, OfferID string
	ContractTerm        string
	PaymentOption       PaymentOption
	SelectedQuantity    int64
	IsSelected          bool
}

// LineItemContractSpec preserves the vendor's explicit terms for consistency checks.
type LineItemContractSpec struct {
	Term           *string        `json:"term"`
	PaymentOption  *PaymentOption `json:"payment_option"`
	CommitmentType string         `json:"commitment_type"`
}

// LineItemOffer keeps provider and catalog offering identifiers distinct.
type LineItemOffer struct {
	OfferingID         *string
	Region             *string
	AZ                 *string
	InstanceType       *string
	InstanceFamily     *string
	ProductDescription *string
	Tenancy            *string
	PlanType           *string
	IsMultiAZ          *bool
	IsFlexible         *bool
	DurationSeconds    *big.Rat
	OfferingClass      *string
	PaymentOption      *PaymentOption
	ID                 string
	ProviderOfferingID string
	Type               string
	Provider           common.ProviderType
}

type jsonField struct {
	out      any
	name     string
	nullable bool
}

func readFields(m map[string]json.RawMessage, fields ...jsonField) error {
	for _, f := range fields {
		b, ok := m[f.name]
		if !ok || (!f.nullable && bytes.Equal(bytes.TrimSpace(b), []byte("null"))) {
			return fmt.Errorf("%s: %w", f.name, errMissing)
		}
		if err := json.Unmarshal(b, f.out); err != nil {
			return fmt.Errorf("%s: invalid JSON field", f.name)
		}
	}
	return nil
}

func capitalizedPayment(p *string) (*PaymentOption, error) {
	if p == nil {
		return nil, nil
	}
	values := map[string]PaymentOption{"No Upfront": PaymentNoUpfront, "Partial Upfront": PaymentPartialUpfront, "All Upfront": PaymentAllUpfront}
	v, ok := values[*p]
	if !ok {
		return nil, errors.New("unsupported value")
	}
	return &v, nil
}

// DecodeLineItems rejects incomplete identity and preserves explicit null attributes.
func DecodeLineItems(r io.Reader, planID string) ([]LineItem, error) {
	var raw *[]map[string]json.RawMessage
	if err := decodeOne(r, &raw); err != nil {
		return nil, fmt.Errorf("decoding line items: %w", err)
	}
	if raw == nil {
		return nil, errMissing
	}
	rows := make([]LineItem, 0, len(*raw))
	seen := make(map[string]bool, len(*raw))
	for i, m := range *raw {
		li, err := decodeLineItem(m, planID)
		if err != nil {
			return nil, fmt.Errorf("line_items[%d]: %w", i, err)
		}
		id := strings.ToLower(li.ID)
		if seen[id] {
			return nil, errors.New("duplicate line item id")
		}
		seen[id] = true
		rows = append(rows, li)
	}
	return rows, nil
}

func decodeLineItem(m map[string]json.RawMessage, planID string) (LineItem, error) {
	var li LineItem
	var amount wireNum
	var payment string
	var offer, contract map[string]json.RawMessage
	err := readFields(m,
		jsonField{out: &li.ID, name: "id", nullable: false}, jsonField{out: &li.PlanID, name: "plan_id", nullable: false}, jsonField{out: &li.OfferID, name: "offer_id", nullable: false},
		jsonField{out: &li.AccountID, name: "account_id", nullable: true}, jsonField{out: &li.SelectedQuantity, name: "selected_quantity", nullable: false},
		jsonField{out: &amount, name: "selected_commitment", nullable: false}, jsonField{out: &li.IsSelected, name: "is_selected", nullable: false},
		jsonField{out: &li.ContractTerm, name: "contract_term", nullable: false}, jsonField{out: &payment, name: "payment_option", nullable: false},
		jsonField{out: &offer, name: "offer", nullable: false}, jsonField{out: &contract, name: "contract_spec", nullable: false})
	if err != nil {
		return li, err
	}
	for name, id := range map[string]string{"id": li.ID, "plan_id": li.PlanID, "offer_id": li.OfferID} {
		if !isUUID(id) {
			return li, fmt.Errorf("%s: must be a UUID", name)
		}
	}
	if !strings.EqualFold(li.PlanID, planID) {
		return li, errors.New("plan_id: does not match requested plan")
	}
	if li.SelectedQuantity < 0 {
		return li, errors.New("selected_quantity: must be nonnegative")
	}
	if li.SelectedCommitment, err = nonnegativeRat(amount); err != nil {
		return li, fmt.Errorf("selected_commitment: %w", err)
	}
	if termsErr := decodeLineItemTerms(&li, payment, contract); termsErr != nil {
		return li, termsErr
	}
	li.Offer, err = decodeLineItemOffer(offer, li.OfferID)
	return li, err
}

func decodeLineItemTerms(li *LineItem, payment string, contract map[string]json.RawMessage) error {
	if _, err := nullableTerm(&li.ContractTerm); err != nil {
		return errors.New("contract_term: unsupported value")
	}
	p, err := capitalizedPayment(&payment)
	if err != nil {
		return fmt.Errorf("payment_option: %w", err)
	}
	li.PaymentOption = *p
	return decodeContractSpec(contract, &li.ContractSpec)
}

func decodeContractSpec(contract map[string]json.RawMessage, spec *LineItemContractSpec) error {
	if err := readFields(contract, jsonField{out: &spec.CommitmentType, name: "commitment_type", nullable: false}); err != nil {
		return fmt.Errorf("contract_spec: %w", err)
	}
	if spec.CommitmentType == "" {
		return fmt.Errorf("contract_spec.commitment_type: %w", errMissing)
	}
	for _, f := range []jsonField{{out: &spec.Term, name: "term", nullable: true}, {out: &spec.PaymentOption, name: "payment_option", nullable: true}} {
		if _, ok := contract[f.name]; ok {
			if err := readFields(contract, f); err != nil {
				return fmt.Errorf("contract_spec: %w", err)
			}
		}
	}
	if _, err := nullableTerm(spec.Term); err != nil {
		return errors.New("contract_spec.term: unsupported value")
	}
	if spec.PaymentOption != nil {
		if _, err := parsePayment(string(*spec.PaymentOption)); err != nil {
			return errors.New("contract_spec.payment_option: unsupported value")
		}
	}
	return nil
}

func decodeLineItemOffer(m map[string]json.RawMessage, offerID string) (LineItemOffer, error) {
	var o LineItemOffer
	var duration wireNum
	var provider string
	err := readFields(m, jsonField{out: &o.ID, name: "id", nullable: false}, jsonField{out: &provider, name: "provider", nullable: false},
		jsonField{out: &o.Type, name: "type", nullable: false},
		jsonField{out: &o.ProviderOfferingID, name: "provider_offering_id", nullable: false}, jsonField{out: &duration, name: "duration_seconds", nullable: false},
		jsonField{out: &o.OfferingID, name: "offering_id", nullable: true}, jsonField{out: &o.Region, name: "region", nullable: true}, jsonField{out: &o.AZ, name: "az", nullable: true},
		jsonField{out: &o.InstanceType, name: "instance_type", nullable: true}, jsonField{out: &o.InstanceFamily, name: "instance_family", nullable: true},
		jsonField{out: &o.ProductDescription, name: "product_description", nullable: true}, jsonField{out: &o.Tenancy, name: "tenancy", nullable: true},
		jsonField{out: &o.PlanType, name: "plan_type", nullable: true}, jsonField{out: &o.IsMultiAZ, name: "is_multi_az", nullable: true}, jsonField{out: &o.IsFlexible, name: "is_flexible", nullable: true})
	if err != nil {
		return o, fmt.Errorf("offer: %w", err)
	}
	if o.Type == "" {
		return o, fmt.Errorf("offer.type: %w", errMissing)
	}
	if o.ProviderOfferingID == "" {
		return o, fmt.Errorf("offer.provider_offering_id: %w", errMissing)
	}
	if !isUUID(o.ID) || !strings.EqualFold(o.ID, offerID) {
		return o, errors.New("offer.id: invalid UUID or offer_id mismatch")
	}
	if o.Provider, err = parseProvider(&provider); err != nil {
		return o, errors.New("offer.provider: unsupported value")
	}
	if o.DurationSeconds, err = requiredRat(duration); err != nil {
		return o, fmt.Errorf("offer.duration_seconds: %w", err)
	}
	if o.DurationSeconds.Sign() <= 0 {
		return o, errors.New("offer.duration_seconds: must be positive")
	}
	err = decodeOfferEnums(m, &o)
	return o, err
}

func decodeOfferEnums(m map[string]json.RawMessage, o *LineItemOffer) error {
	var payment *string
	for _, f := range []jsonField{{out: &o.OfferingClass, name: "offering_class", nullable: true}, {out: &payment, name: "payment_option", nullable: true}} {
		if _, ok := m[f.name]; ok {
			if err := readFields(m, f); err != nil {
				return fmt.Errorf("offer: %w", err)
			}
		}
	}
	if o.OfferingClass != nil && *o.OfferingClass != "standard" && *o.OfferingClass != "convertible" {
		return errors.New("offer.offering_class: unsupported value")
	}
	p, err := capitalizedPayment(payment)
	if err != nil {
		return fmt.Errorf("offer.payment_option: %w", err)
	}
	o.PaymentOption = p
	return nil
}
