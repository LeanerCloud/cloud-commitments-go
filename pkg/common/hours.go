package common //nolint:revive // pkg/common is the module's core domain package; see breakeven.go

// HoursPerYear is the 365-day year length (24 * 365) used to convert a
// reservation term in years into billable hours.
const HoursPerYear = 8760.0

// HoursPerMonth is the average month length (HoursPerYear / 12 = 730) used to
// convert hourly rates into monthly ones.
const HoursPerMonth = HoursPerYear / 12
