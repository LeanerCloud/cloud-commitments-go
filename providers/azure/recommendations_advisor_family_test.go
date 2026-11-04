package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVMFamily(t *testing.T) {
	t.Parallel()
	for sku, want := range map[string]string{
		"Standard_D2s_v3":       "d",
		"Standard_E4-2s_v3":     "e",
		"Standard_DS1_v2":       "ds",
		"Standard_B1ls":         "b",
		"standard_d2s_v3":       "d",
		"Standard_NC4as_T4_v3":  "nc",
		"Standard_Ebdsv5":       "ebdsv",
		"Standard_":             "",
		"D2s_v3":                "",
		"garbage":               "",
		"":                      "",
		"Premium":               "",
		"Azure_Managed_Redis_1": "",
	} {
		assert.Equal(t, want, vmFamily(sku), sku)
	}
}
