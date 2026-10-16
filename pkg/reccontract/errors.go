package reccontract

import (
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// ErrUnavailable matches any UnavailableError via errors.Is.
var ErrUnavailable = errors.New("service unavailable for recommendations")

// UnavailableError reports a service that cannot be recommended via the provider's API.
type UnavailableError struct {
	Provider common.ProviderType
	Service  common.ServiceType
	Reason   string
}

func (e *UnavailableError) Error() string {
	return fmt.Sprintf("%s %s recommendations unavailable: %s", e.Provider, e.Service, e.Reason)
}

// Is reports whether target is ErrUnavailable.
func (e *UnavailableError) Is(target error) bool {
	return target == ErrUnavailable
}

// CheckAvailable returns an UnavailableError when s is listed in c.Unavailable.
func CheckAvailable(c Capabilities, s common.ServiceType) error {
	if s == "" {
		return nil
	}
	reason, ok := c.Unavailable[s]
	if !ok {
		return nil
	}
	return &UnavailableError{Provider: c.Provider, Service: s, Reason: reason}
}
