package pricing

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRedisIdentity(t *testing.T) {
	t.Parallel()
	for _, display := range []string{"P1", "P2", "P3", "P4", "P5"} {
		want := RedisIdentity{"Azure_Redis_Cache_Premium_" + display + "_Cache", display, "Azure Redis Cache Premium", display + " Cache Instance"}
		for _, sku := range []string{"Premium_" + display, want.ArmSKUName} {
			got, err := ParseRedisIdentity(sku)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		}
	}
	for _, want := range []RedisIdentity{
		{"Azure_Redis_Cache_Enterprise_E1", "E1", "Azure Redis Cache Enterprise", "E1 Cache"},
		{"Azure_Redis_Cache_Enterprise_E10", "E10", "Azure Redis Cache Enterprise", "E10 Cache"},
		{"Azure_Redis_Cache_Enterprise_Flash_F300", "F300", "Azure Redis Cache Enterprise Flash", "F300 Cache"},
		{"Azure_Redis_Cache_Enterprise_Flash_F1500", "F1500", "Azure Redis Cache Enterprise Flash", "F1500 Cache"},
	} {
		got, err := ParseRedisIdentity(want.ArmSKUName)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

func TestParseRedisIdentity_RejectsMalformedNames(t *testing.T) {
	t.Parallel()
	for _, sku := range []string{
		"", "Premium_P0", "Premium_P6", "Premium_P10", "Premium_P01", "Premium_P1'",
		"Azure_Redis_Cache_Premium_P1", "Azure_Redis_Cache_Premium_P1_Cache_extra",
		"Azure_Redis_Cache_Enterprise_E", "Azure_Redis_Cache_Enterprise_E+1",
		"Azure_Redis_Cache_Enterprise_E1x", "Azure_Redis_Cache_Enterprise_E１",
		"Azure_Redis_Cache_Enterprise_Flash_F", "Azure_Redis_Cache_Enterprise_Flash_F3.0",
	} {
		got, err := ParseRedisIdentity(sku)
		require.Error(t, err, sku)
		assert.Equal(t, RedisIdentity{}, got)
	}
}
