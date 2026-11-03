package azure

import (
	"context"
	"fmt"
	"slices"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	azrecs "github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/recommendations"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/cache"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/compute"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/database"
)

// reservationUnitPricer is the price lookup the compute, database and cache
// clients export for pricing Advisor rows.
type reservationUnitPricer interface {
	ReservationUnitPrice(ctx context.Context, sku, region string, termYears int) (azrecs.ReservationPrice, error)
}

var (
	_ reservationUnitPricer = (*compute.Client)(nil)
	_ reservationUnitPricer = (*database.Client)(nil)
	_ reservationUnitPricer = (*cache.Client)(nil)
)

// priceAdvisor prices the Advisor reservation rows with the service clients'
// lookups. A client that cannot price (a test fake) has no lookup, so its rows
// are skipped and counted.
func (r *RecommendationsClientAdapter) priceAdvisor(ctx context.Context, rows []common.Recommendation) []common.Recommendation {
	if !slices.ContainsFunc(rows, func(rec common.Recommendation) bool { return rec.CommitmentType == common.CommitmentReservedInstance }) {
		return rows
	}
	clients := map[common.ServiceType]serviceRecsGetter{
		common.ServiceCompute:      newComputeClientFn(r.cred, r.subscriptionID, ""),
		common.ServiceRelationalDB: newDatabaseClientFn(r.cred, r.subscriptionID, ""),
		common.ServiceCache:        newCacheClientFn(r.cred, r.subscriptionID, ""),
	}
	lookups := make(map[common.ServiceType]azrecs.PriceFunc, len(clients))
	for service, client := range clients {
		if pricer, ok := client.(reservationUnitPricer); ok {
			lookups[service] = pricer.ReservationUnitPrice
		}
	}
	return priceAdvisorReservations(ctx, rows, lookups)
}

// priceAdvisorReservations prices Advisor reservation rows through lookups
// (one per service) and returns them as upfront and monthly legs in USD.
// Rows that are not reservations pass through untouched.
//
// Precondition: each reservation row is a base row, one per recommendation
// with an empty PaymentOption, as the legs are created here; any other row
// is skipped.
//
// A row that cannot be priced is dropped and recorded with RecordSkip. A
// failure to reach the price catalog makes the rest of the call skip without
// further lookups. Of the priced rows sharing (account, service, region,
// term), only the one with the smallest total (Count x unit price) is kept,
// the first on a tie, so the result never commits more than any single Advisor
// suggestion. Collapsed rows are logged, not skips.
func priceAdvisorReservations(ctx context.Context, rows []common.Recommendation, lookups map[common.ServiceType]azrecs.PriceFunc) []common.Recommendation {
	pricers := advisorPricers(lookups)
	legs := make([][]common.Recommendation, len(rows))
	winner := make(map[advisorNeed]int)
	var unavailable error
	var priced, skipped, collapsed int
	skip := func(err error) {
		azrecs.RecordSkip(ctx, "advisor", err)
		skipped++
	}
	for i := range rows {
		row := rows[i]
		if row.CommitmentType != common.CommitmentReservedInstance {
			legs[i] = []common.Recommendation{row}
			continue
		}
		pricer := pricers[row.Service]
		switch {
		case unavailable != nil:
			skip(fmt.Errorf("price lookup unavailable: %w", unavailable))
			continue
		case row.PaymentOption != "":
			skip(fmt.Errorf("advisor row for %q in %q is not a base row (payment option %q)", row.ResourceType, row.Region, row.PaymentOption))
			continue
		case pricer == nil:
			skip(fmt.Errorf("no reservation pricer for service %s", row.Service))
			continue
		}
		rowLegs, err := azrecs.AppendConsumptionVariants(ctx, "advisor", nil, row, pricer)
		if err != nil {
			unavailable = err
			skip(err)
			continue
		}
		if len(rowLegs) == 0 {
			skipped++ // already recorded by AppendConsumptionVariants
			continue
		}
		priced++
		if keepCheapest(legs, winner, i, advisorNeedOf(&row), rowLegs) {
			collapsed++
		}
	}
	out := make([]common.Recommendation, 0, len(rows))
	for _, l := range legs {
		out = append(out, l...)
	}
	logAdvisorPricing(priced, skipped, collapsed)
	return out
}

func advisorPricers(lookups map[common.ServiceType]azrecs.PriceFunc) map[common.ServiceType]*azrecs.Pricer {
	pricers := make(map[common.ServiceType]*azrecs.Pricer, len(lookups))
	for service, lookup := range lookups {
		pricers[service] = azrecs.NewPricer("advisor "+string(service), lookup)
	}
	return pricers
}

// keepCheapest stores rowLegs at legs[i] if its total beats the current winner
// for need (or there is none), clearing the loser. It reports whether a row was
// collapsed away, which is true unless row i is the first for its need. The
// first row wins a tie.
func keepCheapest(legs [][]common.Recommendation, winner map[advisorNeed]int, i int, need advisorNeed, rowLegs []common.Recommendation) bool {
	best, ok := winner[need]
	if !ok {
		winner[need], legs[i] = i, rowLegs
		return false
	}
	if rowLegs[0].CommitmentCost < legs[best][0].CommitmentCost {
		legs[best], winner[need], legs[i] = nil, i, rowLegs
	}
	return true
}

func logAdvisorPricing(priced, skipped, collapsed int) {
	if priced+skipped+collapsed > 0 {
		logging.Infof("azure advisor: priced %d, skipped %d, collapsed %d reservation row(s)", priced, skipped, collapsed)
	}
}
