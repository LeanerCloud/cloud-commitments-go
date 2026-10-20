package purchasecfg

import (
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
)

type opts struct{ n int }

func TestTokenlessHelpers(t *testing.T) {
	t.Parallel()
	set := func(o *opts) { o.n = 1 }
	assert.Len(t, SingleAttemptIf("", set), 1)
	assert.Nil(t, SingleAttemptIf("tok", set))

	plain := errors.New("boom")
	assert.ErrorIs(t, ClassifyUnlessToken("", plain), common.ErrOutcomeUnknown)
	assert.Equal(t, plain, ClassifyUnlessToken("tok", plain))
	assert.ErrorIs(t, UnknownUnlessToken("", plain), common.ErrOutcomeUnknown)
	assert.Equal(t, plain, UnknownUnlessToken("tok", plain))
}
