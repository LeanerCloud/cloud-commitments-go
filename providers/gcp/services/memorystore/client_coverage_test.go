package memorystore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

func TestExtractPriceFromSKU_ValidPrice(t *testing.T) {
	sku := &cloudbilling.Sku{
		PricingInfo: []*cloudbilling.PricingInfo{
			{
				PricingExpression: &cloudbilling.PricingExpression{
					TieredRates: []*cloudbilling.TierRate{
						{
							UnitPrice: &cloudbilling.Money{
								Units:        1,
								Nanos:        500000000,
								CurrencyCode: "USD",
							},
						},
					},
				},
			},
		},
	}

	price, currency := extractPriceFromSKU(sku)
	assert.Equal(t, 1.5, price)
	assert.Equal(t, "USD", currency)
}

func TestExtractPriceFromSKU_NoPricingInfo(t *testing.T) {
	sku := &cloudbilling.Sku{
		PricingInfo: []*cloudbilling.PricingInfo{},
	}

	price, currency := extractPriceFromSKU(sku)
	assert.Equal(t, 0.0, price)
	assert.Equal(t, "", currency)
}

func TestExtractPriceFromSKU_NoPricingExpression(t *testing.T) {
	sku := &cloudbilling.Sku{
		PricingInfo: []*cloudbilling.PricingInfo{
			{
				PricingExpression: nil,
			},
		},
	}

	price, currency := extractPriceFromSKU(sku)
	assert.Equal(t, 0.0, price)
	assert.Equal(t, "", currency)
}

func TestExtractPriceFromSKU_NoTieredRates(t *testing.T) {
	sku := &cloudbilling.Sku{
		PricingInfo: []*cloudbilling.PricingInfo{
			{
				PricingExpression: &cloudbilling.PricingExpression{
					TieredRates: []*cloudbilling.TierRate{},
				},
			},
		},
	}

	price, currency := extractPriceFromSKU(sku)
	assert.Equal(t, 0.0, price)
	assert.Equal(t, "", currency)
}

func TestExtractPriceFromSKU_NoUnitPrice(t *testing.T) {
	sku := &cloudbilling.Sku{
		PricingInfo: []*cloudbilling.PricingInfo{
			{
				PricingExpression: &cloudbilling.PricingExpression{
					TieredRates: []*cloudbilling.TierRate{
						{
							UnitPrice: nil,
						},
					},
				},
			},
		},
	}

	price, currency := extractPriceFromSKU(sku)
	assert.Equal(t, 0.0, price)
	assert.Equal(t, "", currency)
}

func TestExtractPricingFromSKUs_ValidPricing(t *testing.T) {
	skus := []*cloudbilling.Sku{
		{
			Description: "Memorystore for Redis: M1 in us-central1",
			PricingInfo: []*cloudbilling.PricingInfo{
				{
					PricingExpression: &cloudbilling.PricingExpression{
						TieredRates: []*cloudbilling.TierRate{
							{
								UnitPrice: &cloudbilling.Money{
									Units:        2,
									Nanos:        0,
									CurrencyCode: "USD",
								},
							},
						},
					},
				},
			},
			ServiceRegions: []string{"us-central1"},
			Category: &cloudbilling.Category{
				ResourceGroup: "Memorystore",
				UsageType:     "OnDemand",
			},
		},
		{
			Description: "Memorystore for Redis: M1 Commitment in us-central1",
			PricingInfo: []*cloudbilling.PricingInfo{
				{
					PricingExpression: &cloudbilling.PricingExpression{
						TieredRates: []*cloudbilling.TierRate{
							{
								UnitPrice: &cloudbilling.Money{
									Units:        1,
									Nanos:        500000000,
									CurrencyCode: "USD",
								},
							},
						},
					},
				},
			},
			ServiceRegions: []string{"us-central1"},
			Category: &cloudbilling.Category{
				ResourceGroup: "Memorystore",
				UsageType:     "Commit1Yr",
			},
		},
	}

	onDemand, commitment, currency, err := extractPricingFromSKUs(skus, "M1", "us-central1")
	require.NoError(t, err)
	assert.Greater(t, onDemand, 0.0)
	assert.Greater(t, commitment, 0.0)
	assert.Equal(t, "USD", currency)
}

func TestExtractPricingFromSKUs_NoMatchingSKUs(t *testing.T) {
	skus := []*cloudbilling.Sku{
		{
			Description: "Memorystore for Redis: M2 in us-east1",
			PricingInfo: []*cloudbilling.PricingInfo{
				{
					PricingExpression: &cloudbilling.PricingExpression{
						TieredRates: []*cloudbilling.TierRate{
							{
								UnitPrice: &cloudbilling.Money{
									Units:        2,
									Nanos:        0,
									CurrencyCode: "USD",
								},
							},
						},
					},
				},
			},
			Category: &cloudbilling.Category{
				ResourceGroup: "Memorystore",
				UsageType:     "OnDemand",
			},
		},
	}

	onDemand, commitment, currency, err := extractPricingFromSKUs(skus, "M1", "us-central1")
	require.NoError(t, err)
	assert.Equal(t, 0.0, onDemand)
	assert.Equal(t, 0.0, commitment)
	assert.Empty(t, currency)
}
