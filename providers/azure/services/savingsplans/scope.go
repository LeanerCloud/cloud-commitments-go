package savingsplans

import (
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/billingbenefits/armbillingbenefits"
)

func planOwnedBy(sp *armbillingbenefits.SavingsPlanModel, ownScope string) (bool, error) {
	if sp == nil || sp.Properties == nil || sp.Properties.BillingScopeID == nil {
		return false, fmt.Errorf("missing billing scope")
	}
	billingScope := *sp.Properties.BillingScopeID
	if !hasPrefixFold(billingScope, subscriptionScopePrefix) {
		return false, fmt.Errorf("billing scope does not identify a subscription")
	}
	subscription := billingScope[len(subscriptionScopePrefix):]
	if subscription == "" || strings.IndexFunc(subscription, func(r rune) bool {
		return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-", r)
	}) >= 0 {
		return false, fmt.Errorf("malformed billing subscription scope")
	}
	if !strings.EqualFold(billingScope, ownScope) {
		return false, nil
	}
	if sp.ID == nil || strings.TrimSpace(*sp.ID) == "" {
		return false, fmt.Errorf("owned savings plan has no ID")
	}
	return true, nil
}
