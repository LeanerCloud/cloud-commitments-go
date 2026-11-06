package azure

import (
	"context"
	"fmt"
	"slices"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	azrecs "github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/recommendations"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/compute"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/database"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/managedredis"
)

// reservationUnitPricer is the price lookup the compute, database and Redis
// clients export for pricing Advisor rows.
type reservationUnitPricer interface {
	ReservationUnitPrice(ctx context.Context, sku, region string, termYears int) (azrecs.ReservationPrice, error)
}

var (
	_ reservationUnitPricer = (*compute.Client)(nil)
	_ reservationUnitPricer = (*database.Client)(nil)
	_ reservationUnitPricer = (*managedredis.Client)(nil)
)

// priceAdvisor prices the Advisor reservation rows with the service clients'
// lookups. A client that cannot price (a test fake) has no lookup, so its rows
// are skipped and counted. Rows of a failed collector are left alone: the merge
// drops them, so pricing them would only issue lookups and record skips.
func (r *RecommendationsClientAdapter) priceAdvisor(ctx context.Context, rows []common.Recommendation, collectErr error) []common.Recommendation {
	if collectErr != nil || !slices.ContainsFunc(rows, func(rec common.Recommendation) bool { return rec.CommitmentType == common.CommitmentReservedInstance }) {
		return rows
	}
	clients := map[common.ServiceType]serviceRecsGetter{
		common.ServiceCompute:      newComputeClientFn(r.cred, r.subscriptionID, ""),
		common.ServiceRelationalDB: newDatabaseClientFn(r.cred, r.subscriptionID, ""),
		common.ServiceMemoryDB:     newManagedRedisClientFn(r.cred, r.subscriptionID, ""),
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
// term, VM family), only the one with the smallest total (Count x unit price) is
// kept, the first on a tie, so the result never commits more than any single
// Advisor suggestion per family. Collapsed rows are logged, not skips.
func priceAdvisorReservations(ctx context.Context, rows []common.Recommendation, lookups map[common.ServiceType]azrecs.PriceFunc) []common.Recommendation {
	pricers := advisorPricers(lookups)
	legs := make([][]common.Recommendation, len(rows))
	groups := make(map[advisorNeed][]int)
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
		legs[i] = rowLegs
		coarse := advisorNeedOf(&row).coarse()
		groups[coarse] = append(groups[coarse], i)
	}
	for _, idx := range groups {
		collapsed += keepCheapest(legs, rows, idx)
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

// keepCheapest clears the legs of every row in idx (ascending, one coarse
// need) except the cheapest, and returns how many it cleared. Rows are split by
// VM family only when every family is known; one unknown family makes the
// whole need a single group, so it never yields two purchasable rows. The
// first row wins a tie, whatever the order the groups are visited in.
func keepCheapest(legs [][]common.Recommendation, rows []common.Recommendation, idx []int) int {
	byFamily := make(map[string][]int)
	for _, i := range idx {
		f := advisorNeedOf(&rows[i]).family
		if f == "" {
			byFamily = map[string][]int{"": idx}
			break
		}
		byFamily[f] = append(byFamily[f], i)
	}
	cleared := 0
	for _, g := range byFamily {
		best := g[0]
		for _, i := range g[1:] {
			if legs[i][0].CommitmentCost < legs[best][0].CommitmentCost {
				best = i
			}
		}
		for _, i := range g {
			if i != best {
				legs[i] = nil
				cleared++
			}
		}
	}
	return cleared
}

func logAdvisorPricing(priced, skipped, collapsed int) {
	if priced+skipped+collapsed > 0 {
		logging.Infof("azure advisor: priced %d, skipped %d, collapsed %d reservation row(s)", priced, skipped, collapsed)
	}
}
