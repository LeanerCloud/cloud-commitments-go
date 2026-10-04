package reservationexpiry

import "time"

// EndDate returns zero when the reservation's expiry cannot be determined.
func EndDate(start time.Time, seconds int32) time.Time {
	if start.IsZero() || seconds <= 0 {
		return time.Time{}
	}
	return start.Add(time.Duration(seconds) * time.Second)
}
