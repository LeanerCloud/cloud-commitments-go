package provider

import (
	"context"
	"reflect"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func TestServiceClientExcludesOfferingMethods(t *testing.T) {
	st := reflect.TypeOf((*ServiceClient)(nil)).Elem()
	for _, name := range []string{"ValidateOffering", "GetOfferingDetails", "GetValidResourceTypes"} {
		if _, ok := st.MethodByName(name); ok {
			t.Errorf("ServiceClient must not declare %s", name)
		}
	}
}

func TestOfferingPricerSignature(t *testing.T) {
	pt := reflect.TypeOf((*OfferingPricer)(nil)).Elem()
	if pt.NumMethod() != 1 {
		t.Fatalf("OfferingPricer has %d methods, want 1", pt.NumMethod())
	}
	m, ok := pt.MethodByName("GetOfferingDetails")
	if !ok {
		t.Fatal("OfferingPricer missing GetOfferingDetails")
	}
	want := reflect.TypeOf(func(context.Context, common.Recommendation) (*common.OfferingDetails, error) { return nil, nil })
	if m.Type != want {
		t.Errorf("GetOfferingDetails signature = %v, want %v", m.Type, want)
	}
}
