package purchasecfg

import "fmt"

// Canonical RI/CUD term domain shared by every AWS service client: "1yr"/"1"
// and "3yr"/"3". The parsers fail loud on any other input (ARCH-04): an
// unrecognized or empty term must surface an error at the API boundary rather
// than silently matching - and buying - a 1-year offering when another
// commitment length was intended. An empty term is what a 0/NULL Term DB row
// produces on the scheduler purchase path.
const (
	OneYearSeconds   = 31536000 // 365 days in seconds
	ThreeYearSeconds = 94608000 // 3 * 365 days in seconds
)

// ParseTermMonths converts a reservation term string to the offering duration
// in months. The service name is used only in the error message.
func ParseTermMonths(term, service string) (int, error) {
	switch term {
	case "3yr", "3":
		return 36, nil
	case "1yr", "1":
		return 12, nil
	default:
		return 0, fmt.Errorf("unsupported %s reservation term %q: must be one of 1yr, 1, 3yr, 3", service, term)
	}
}

// DurationSeconds converts a reservation term string to the duration in
// seconds the EC2 API expects.
func DurationSeconds(term, service string) (int64, error) {
	months, err := ParseTermMonths(term, service)
	if err != nil {
		return 0, err
	}
	if months == 36 {
		return ThreeYearSeconds, nil
	}
	return OneYearSeconds, nil
}

// DurationSecondsString converts a reservation term string to the duration
// string, in seconds, that the ElastiCache and RDS APIs expect.
func DurationSecondsString(term, service string) (string, error) {
	seconds, err := DurationSeconds(term, service)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", seconds), nil
}

// DurationYearString converts a reservation term string to the "1yr"/"3yr"
// form the MemoryDB API accepts.
func DurationYearString(term, service string) (string, error) {
	months, err := ParseTermMonths(term, service)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%dyr", months/12), nil
}
