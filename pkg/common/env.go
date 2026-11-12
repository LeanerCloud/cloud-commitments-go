package common //nolint:revive // pkg/common is the module's core domain package, imported by name (common.X) throughout this repo

import (
	"os"
	"strconv"
)

// PositiveIntFromEnv returns the positive integer held in the env var name,
// or def when it is unset, not an integer, or not positive.
func PositiveIntFromEnv(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
