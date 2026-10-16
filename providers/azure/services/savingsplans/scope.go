package savingsplans

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/billingbenefits/armbillingbenefits"
)

type planDisposition int

const (
	planNotOurs       planDisposition = iota // billed to another subscription
	planOwned                                // belongs in this subscription's inventory
	planSkippedSingle                        // account-billed Single plan applied to another subscription
)

const billingAccountPrefix = "/providers/Microsoft.Billing/billingAccounts/"

// planOwnedBy decides whether a plan belongs to ownSub's inventory. Plans billed
// to a subscription are owned by that subscription. Plans billed to a billing
// account are attributed only when AppliedScopeType is Single and names one
// subscription; Shared, management-group and unknown scopes fail closed.
func planOwnedBy(sp *armbillingbenefits.SavingsPlanModel, ownSub string) (planDisposition, error) {
	if sp == nil || sp.Properties == nil || sp.Properties.BillingScopeID == nil {
		return planNotOurs, fmt.Errorf("missing billing scope")
	}
	billingScope := *sp.Properties.BillingScopeID
	switch {
	case hasPrefixFold(billingScope, subscriptionScopePrefix):
		return subscriptionBilledPlan(sp, billingScope, ownSub)
	case isBillingAccountScope(billingScope):
		return accountBilledPlan(sp, ownSub)
	}
	return planNotOurs, fmt.Errorf("billing scope does not identify a subscription")
}

func subscriptionBilledPlan(sp *armbillingbenefits.SavingsPlanModel, billingScope, ownSub string) (planDisposition, error) {
	if !isSubscriptionSegment(billingScope[len(subscriptionScopePrefix):]) {
		return planNotOurs, fmt.Errorf("malformed billing subscription scope")
	}
	if !strings.EqualFold(billingScope, subscriptionScopePrefix+ownSub) {
		return planNotOurs, nil
	}
	return ownedWithID(sp)
}

func ownedWithID(sp *armbillingbenefits.SavingsPlanModel) (planDisposition, error) {
	if sp.ID == nil || strings.TrimSpace(*sp.ID) == "" {
		return planNotOurs, fmt.Errorf("owned savings plan has no ID")
	}
	return planOwned, nil
}

// accountBilledPlan attributes a Single plan to the subscription it names,
// because no subscription pays for it. Plans billed to another subscription
// but applied to this one are tracked in #466 and are not handled here.
func accountBilledPlan(sp *armbillingbenefits.SavingsPlanModel, ownSub string) (planDisposition, error) {
	if sp.Properties.AppliedScopeType == nil || *sp.Properties.AppliedScopeType != armbillingbenefits.AppliedScopeTypeSingle {
		return planNotOurs, fmt.Errorf("billing-account plan is not single-subscription scoped")
	}
	applied, err := singleScopeSubscription(sp.Properties.AppliedScopeProperties)
	if err != nil {
		return planNotOurs, err
	}
	if !strings.EqualFold(applied, ownSub) {
		return planSkippedSingle, nil
	}
	return ownedWithID(sp)
}

// singleScopeSubscription resolves the one subscription a Single scope names,
// from SubscriptionID or ResourceGroupID. Errors never echo the scope strings.
func singleScopeSubscription(props *armbillingbenefits.AppliedScopeProperties) (string, error) {
	if props == nil {
		return "", fmt.Errorf("single-scope plan has no applied scope properties")
	}
	if props.ManagementGroupID != nil && strings.TrimSpace(*props.ManagementGroupID) != "" {
		return "", fmt.Errorf("single-scope plan names a management group")
	}
	fromSub, err := appliedSubscription(props.SubscriptionID)
	if err != nil {
		return "", err
	}
	fromRG, err := appliedResourceGroupSubscription(props.ResourceGroupID)
	if err != nil {
		return "", err
	}
	return agreeingSubscription(fromSub, fromRG)
}

// agreeingSubscription picks the one subscription named by the SubscriptionID
// and ResourceGroupID fields ("" means absent); two names must agree.
func agreeingSubscription(fromSub, fromRG string) (string, error) {
	switch {
	case fromSub == "" && fromRG == "":
		return "", fmt.Errorf("single-scope plan names no subscription")
	case fromSub != "" && fromRG != "" && !strings.EqualFold(fromSub, fromRG):
		return "", fmt.Errorf("applied subscription and resource group scopes disagree")
	case fromSub == "":
		return fromRG, nil
	}
	return fromSub, nil
}

func appliedResourceGroupSubscription(id *string) (string, error) {
	if id == nil {
		return "", nil
	}
	sub, ok := resourceGroupSubscription(*id)
	if !ok {
		return "", fmt.Errorf("malformed applied resource group scope")
	}
	return sub, nil
}

// appliedSubscription parses exactly /subscriptions/<sub>. A nil or blank value
// counts as absent and yields "".
func appliedSubscription(id *string) (string, error) {
	if id == nil || strings.TrimSpace(*id) == "" {
		return "", nil
	}
	rest, ok := cutPrefixFold(*id, subscriptionScopePrefix)
	if !ok || !isSubscriptionSegment(rest) {
		return "", fmt.Errorf("malformed applied subscription scope")
	}
	return rest, nil
}

// resourceGroupSubscription parses exactly /subscriptions/<sub>/resourceGroups/<rg>.
func resourceGroupSubscription(id string) (string, bool) {
	rest, ok := cutPrefixFold(id, subscriptionScopePrefix)
	if !ok {
		return "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || !strings.EqualFold(parts[1], "resourceGroups") ||
		!isSubscriptionSegment(parts[0]) || !isOpaqueID(parts[2]) {
		return "", false
	}
	return parts[0], true
}

// isBillingAccountScope accepts exactly the EA shape (billingAccounts/{id}) and
// the MCA shape (billingAccounts/{id}/billingProfiles/{id}). MCA ids contain
// ':' and '_', so ids are opaque rather than alphanumeric-dash.
func isBillingAccountScope(scope string) bool {
	rest, ok := cutPrefixFold(scope, billingAccountPrefix)
	if !ok {
		return false
	}
	parts := strings.Split(rest, "/")
	switch len(parts) {
	case 1:
		return isOpaqueID(parts[0])
	case 3:
		return isOpaqueID(parts[0]) && strings.EqualFold(parts[1], "billingProfiles") && isOpaqueID(parts[2])
	}
	return false
}

func isOpaqueID(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool {
		return r == '/' || unicode.IsSpace(r) || unicode.IsControl(r)
	}) < 0
}

func isSubscriptionSegment(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-", r)
	}) < 0
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if !hasPrefixFold(s, prefix) {
		return "", false
	}
	return s[len(prefix):], true
}
