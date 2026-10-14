package insurance

import (
	"errors"
	"math/big"
)

func nonnegativeRat(n wireNum) (*big.Rat, error) {
	r, err := requiredRat(n)
	if err != nil {
		return nil, err
	}
	if r.Sign() < 0 {
		return nil, errors.New("must be nonnegative")
	}
	return r, nil
}
