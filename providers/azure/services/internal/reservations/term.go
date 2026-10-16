package reservations

import (
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
)

// ReservationTermForYears maps a term in years to the SDK enum Azure expects in
// properties.term. An explicit switch keeps an unsupported term an error
// instead of a formatted string Azure would reject after the request is sent.
func ReservationTermForYears(years int) (armreservations.ReservationTerm, error) {
	switch years {
	case 1:
		return armreservations.ReservationTermP1Y, nil
	case 3:
		return armreservations.ReservationTermP3Y, nil
	default:
		return "", fmt.Errorf("unsupported reservation term: %d years", years)
	}
}
