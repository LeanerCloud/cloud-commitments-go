// Package reccontract defines the recommendation request contract shared by
// providers. Capabilities declares which request values a provider can honor;
// an empty slice means the field is unsupported and any non-empty request for
// it fails loud. A service listed in Unavailable cannot be recommended via the
// provider's API, and requests for it fail with UnavailableError before any
// cloud call.
package reccontract

import "github.com/LeanerCloud/cloud-commitments-go/pkg/common"

// Capabilities declares the request values a provider can honor.
type Capabilities struct {
	Unavailable      map[common.ServiceType]string // service -> reason it cannot be recommended via API
	Provider         common.ProviderType
	DefaultLookback  string   // filled into an empty request lookback; empty when the provider has none
	Terms            []string // "1yr","3yr"
	PaymentOptions   []string // canonical payment tokens
	Lookbacks        []string // "7d","30d","60d"; empty when the provider has no lookback input
	AccountScopes    []string // "linked","payer"
	OfferingClasses  []string // "standard","convertible"
	SavingsPlanTypes bool     // IncludeSPTypes/ExcludeSPTypes are meaningful
}
