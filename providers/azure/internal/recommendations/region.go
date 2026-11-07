package recommendations

import "strings"

// ARMRegion converts a display region ("East US") or ARM name to the
// lowercase, whitespace-free ARM form ("eastus") that Retail Prices and the
// purchase body expect.
func ARMRegion(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), ""))
}
