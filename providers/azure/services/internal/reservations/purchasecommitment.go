package reservations

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// PurchaseParams holds what differs between the Azure service clients when
// they build a reservation purchase body.
type PurchaseParams struct {
	// DisplayName returns properties.displayName for the recommendation.
	DisplayName func(common.Recommendation) string

	// ReservedResourceType is the properties.reservedResourceType value.
	ReservedResourceType string
}

// BuildPurchaseBody parses rec.Term, builds the calculatePrice/purchase body
// with the resolved billing plan, applies the purchase tags and marshals it.
// Term errors are returned raw; callers wrap them as they see fit.
func BuildPurchaseBody(p PurchaseParams, rec common.Recommendation, opts common.PurchaseOptions, region, subscriptionID string, plan armreservations.ReservationBillingPlan) ([]byte, error) {
	termYears, err := ParseTermYears(rec.Term)
	if err != nil {
		return nil, err
	}
	term, err := ReservationTermForYears(termYears)
	if err != nil {
		return nil, err
	}
	body := map[string]interface{}{
		"sku": map[string]string{
			"name": rec.ResourceType,
		},
		"location": region,
		"properties": map[string]interface{}{
			"reservedResourceType": p.ReservedResourceType,
			"billingScopeId":       fmt.Sprintf("/subscriptions/%s", subscriptionID),
			"billingPlan":          string(plan),
			"term":                 string(term),
			"quantity":             rec.Count,
			"displayName":          p.DisplayName(rec),
			"appliedScopeType":     string(armreservations.AppliedScopeTypeShared),
			"renew":                false,
		},
	}
	ApplyPurchaseTags(body, opts.Source, opts.IdempotencyToken)
	return json.Marshal(body)
}

// Submit fetches an ARM token and runs the idempotent two-step purchase. The
// error of DoIdempotentPurchaseTwoStepCurrency is returned unwrapped.
// commitmentCurrency is the recommendation's CommitmentCurrency.
func Submit(ctx context.Context, cred azcore.TokenCredential, httpc HTTPClient, body []byte, opts common.PurchaseOptions, commitmentCurrency string) (orderID string, existing bool, err error) {
	token, err := cred.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{"https://management.azure.com/.default"},
	})
	if err != nil {
		return "", false, fmt.Errorf("failed to get access token: %w", err)
	}
	return DoIdempotentPurchaseTwoStepCurrency(ctx, httpc, CalculatePriceURL(), body, token.Token, opts.IdempotencyToken, commitmentCurrency)
}
