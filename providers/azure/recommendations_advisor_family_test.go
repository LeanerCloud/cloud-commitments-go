package azure

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// A family that spans two ISF groups would turn one need into two purchasable
// rows (a double-commit risk); one group spanning families only loses advice.
func TestVMFamily_NeverSplitsAnISFGroup(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/isf_vm_groups.txt")
	require.NoError(t, err)
	groups, skus := 0, 0
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		group, list, ok := strings.Cut(line, "|")
		require.True(t, ok, line)
		groups++
		families := map[string]bool{}
		for _, sku := range strings.Split(list, ",") {
			skus++
			families[vmFamily(sku)] = true
		}
		assert.LessOrEqual(t, len(families), 1, "ISF group %q maps to several families %v", group, families)
	}
	assert.Equal(t, 222, groups)
	assert.Equal(t, 1550, skus)
}
