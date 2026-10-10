package insurance

import (
	"maps"
	"slices"
)

// contractTerms is the documented contract_term enum, identical across
// OfferComparisonEntry.contract_term, HypotheticalTotal.contract_term and
// HypotheticalLineItem.actual_term.
var contractTerms = func() map[string]struct{} {
	m := map[string]struct{}{}
	for _, t := range []string{
		"one_year_gris", "thirty_day_gris", "two_month_gris",
		"three_month_gris", "four_month_gris", "five_month_gris",
		"six_month_gris", "seven_month_gris", "eight_month_gris",
		"nine_month_gris", "ten_month_gris", "eleven_month_gris",
		"twelve_month_gris", "thirteen_month_gris", "fourteen_month_gris",
		"fifteen_month_gris", "sixteen_month_gris", "seventeen_month_gris",
		"eighteen_month_gris", "nineteen_month_gris", "twenty_month_gris",
		"twenty_one_month_gris", "twenty_two_month_gris",
		"twenty_three_month_gris", "twenty_four_month_gris",
		"twenty_five_month_gris", "twenty_six_month_gris",
		"twenty_seven_month_gris", "twenty_eight_month_gris",
		"twenty_nine_month_gris", "thirty_month_gris", "thirty_one_month_gris",
		"thirty_two_month_gris", "thirty_three_month_gris",
		"thirty_four_month_gris", "thirty_five_month_gris", "one_year",
		"two_year", "three_year", "five_year", "zero_day", "thirty_day",
		"two_month", "three_month", "four_month", "five_month", "six_month",
		"seven_month", "eight_month", "nine_month", "ten_month",
		"eleven_month", "thirteen_month", "fourteen_month", "fifteen_month",
		"sixteen_month", "seventeen_month", "eighteen_month", "nineteen_month",
		"twenty_month", "twenty_one_month", "twenty_two_month",
		"twenty_three_month", "twenty_five_month", "twenty_six_month",
		"twenty_seven_month", "twenty_eight_month", "twenty_nine_month",
		"thirty_month", "thirty_one_month", "thirty_two_month",
		"thirty_three_month", "thirty_four_month", "thirty_five_month",
	} {
		m[t] = struct{}{}
	}
	return m
}()

// ContractTerms returns the documented Archera contract_term values accepted
// in ComparisonRequest.ContractTerms and returned in the ContractTerm fields,
// as a new slice on every call. The order is lexicographic, which is NOT a
// duration order ("eleven_month" sorts before "five_month"); a consumer that
// shows the terms to people must impose its own ordering.
func ContractTerms() []string {
	return slices.Sorted(maps.Keys(contractTerms))
}
