package skumatch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/api/cloudbilling/v1"
)

func tier(units, nanos int64) *cloudbilling.TierRate {
	return &cloudbilling.TierRate{UnitPrice: &cloudbilling.Money{CurrencyCode: "USD", Units: units, Nanos: nanos}}
}

func TestPaidRate(t *testing.T) {
	t.Parallel()
	free, paid, whole := tier(0, 0), tier(0, 50000000), tier(2, 0)
	tests := []struct {
		name string
		expr *cloudbilling.PricingExpression
		want *cloudbilling.TierRate
	}{
		{"nil expression", nil, nil},
		{"no tiers", &cloudbilling.PricingExpression{}, nil},
		{"single paid tier", &cloudbilling.PricingExpression{TieredRates: []*cloudbilling.TierRate{paid}}, paid},
		{"free first tier then paid", &cloudbilling.PricingExpression{TieredRates: []*cloudbilling.TierRate{free, paid}}, paid},
		{"first paid tier wins over later ones", &cloudbilling.PricingExpression{TieredRates: []*cloudbilling.TierRate{free, paid, whole}}, paid},
		{"whole-unit price counts as paid", &cloudbilling.PricingExpression{TieredRates: []*cloudbilling.TierRate{free, whole}}, whole},
		{"nil tier and nil price are skipped", &cloudbilling.PricingExpression{TieredRates: []*cloudbilling.TierRate{nil, {}, paid}}, paid},
		{"all tiers free", &cloudbilling.PricingExpression{TieredRates: []*cloudbilling.TierRate{free, tier(0, 0)}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Same(t, tt.want, PaidRate(tt.expr))
		})
	}
}
