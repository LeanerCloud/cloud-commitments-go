package azure

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	azrecs "github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/recommendations"
)

// priceAdvisorReservations prices Advisor reservation rows through lookups
// (one per service) and returns them as upfront and monthly legs in USD.
// Rows that are not reservations pass through untouched.
//
// Precondition: each reservation row is a base row, one per recommendation
// with an empty PaymentOption, as the legs are created here.
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
		need := advisorNeedOf(&row)
		best, ok := winner[need]
		switch {
		case !ok:
			winner[need], legs[i] = i, rowLegs
		case rowLegs[0].CommitmentCost < legs[best][0].CommitmentCost:
			legs[best], winner[need], legs[i] = nil, i, rowLegs
			collapsed++
		default:
			collapsed++
		}
	}
	out := make([]common.Recommendation, 0, len(rows))
	for _, l := range legs {
		out = append(out, l...)
	}
	logging.Infof("azure advisor: priced %d, skipped %d, collapsed %d reservation row(s)", priced, skipped, collapsed)
	return out
}

func advisorPricers(lookups map[common.ServiceType]azrecs.PriceFunc) map[common.ServiceType]*azrecs.Pricer {
	pricers := make(map[common.ServiceType]*azrecs.Pricer, len(lookups))
	for service, lookup := range lookups {
		pricers[service] = azrecs.NewPricer("advisor "+string(service), lookup)
	}
	return pricers
}
