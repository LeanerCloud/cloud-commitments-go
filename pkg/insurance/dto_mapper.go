package insurance

import "math/big"

const currencyNote = "currency not provided by Archera"

// FinancialsDTO carries the 730-hour monthly rate block.
type FinancialsDTO struct {
	CommitmentCostTotal *string `json:"monthly_730h_commitment_cost_total"`
	CloudProviderCost   *string `json:"monthly_730h_cloud_provider_cost"`
	Premium             *string `json:"monthly_730h_archera_premium"`
	GrossSavings        *string `json:"monthly_730h_gross_savings"`
	NetSavings          *string `json:"monthly_730h_net_savings"`
	CoveredOnDemandCost *string `json:"monthly_730h_covered_on_demand_cost"`
}

type mapper struct{ err error }

func (m *mapper) dec(r *big.Rat) *string {
	s, err := Decimal(r)
	if err != nil && m.err == nil {
		m.err = err
	}
	return s
}

func cleanPtr(s *string) *string {
	if s == nil {
		return nil
	}
	c := CleanString(*s)
	return &c
}

func (m *mapper) financials(f Financials) FinancialsDTO {
	return FinancialsDTO{
		CommitmentCostTotal: m.dec(f.CommitmentCostTotal), CloudProviderCost: m.dec(f.CloudProviderCost),
		Premium: m.dec(f.Premium), GrossSavings: m.dec(f.GrossSavings), NetSavings: m.dec(f.NetSavings),
		CoveredOnDemandCost: m.dec(f.CoveredOnDemandCost),
	}
}
func payment(p *PaymentOption) *string {
	if p == nil {
		return nil
	}
	s := CleanString(string(*p))
	return &s
}
