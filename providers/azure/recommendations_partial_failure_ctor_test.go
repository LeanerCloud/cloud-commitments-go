package azure_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/providers/azure"
)

func TestNewPartialSubscriptionFailure(t *testing.T) {
	cause := errors.New("boom")
	failed := []azure.SubscriptionFailure{{SubscriptionID: "sub-b", Err: cause}}

	t.Run("without skips", func(t *testing.T) {
		for _, reasons := range [][]string{nil, {}} {
			err := fmt.Errorf("wrapped: %w", azure.NewPartialSubscriptionFailure(3, failed, reasons))
			got := azure.AsPartialSubscriptionFailure(err)
			require.NotNil(t, got)
			assert.Nil(t, got.Skips())
			assert.Equal(t, 3, got.Attempted)
			assert.Equal(t, 2, got.Succeeded)
			assert.Equal(t, failed, got.Failed)
			assert.ErrorIs(t, err, cause)
		}
	})

	t.Run("with skips", func(t *testing.T) {
		reasons := []string{"a: skipped", "b: skipped"}
		e := azure.NewPartialSubscriptionFailure(3, failed, reasons)
		got := azure.AsPartialSubscriptionFailure(e)
		require.NotNil(t, got)
		skips := got.Skips()
		require.NotNil(t, skips)
		assert.Equal(t, 2, skips.SkippedCount())
		assert.Equal(t, []string{"a: skipped", "b: skipped"}, skips.SkipReasons())

		reasons[0] = "mutated"
		assert.Equal(t, "a: skipped", got.Skips().SkipReasons()[0], "input slice must be copied")
		got.Skips().SkipReasons()[1] = "mutated"
		assert.Equal(t, "b: skipped", got.Skips().SkipReasons()[1], "returned slice must be a copy")
	})

	t.Run("not a partial result", func(t *testing.T) {
		e := azure.NewPartialSubscriptionFailure(3, failed, []string{"x"})
		var p interface{ Partial() bool }
		assert.False(t, errors.As(e, &p))
	})
}
