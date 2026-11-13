package ladder

import (
	"fmt"
	"math/big"
)

// ValidateRunCaps checks an assembled plan against the per-run caps in cfg.
// Only the immediate purchases of this run count toward the hourly cap, since
// later tranches are scheduled rows, not spend. Purchases plus reshapes must
// not exceed MaxActionsPerRun; holds are audit rows and are not counted, as in
// truncateToMaxActions. Allocate enforces both caps on its own output, so this
// check guards plans built or mutated outside that path before they execute.
func (p *LadderPlan) ValidateRunCaps(cfg *LadderConfig) error {
	if p == nil {
		return fmt.Errorf("plan must not be nil")
	}
	if cfg == nil {
		return fmt.Errorf("config must not be nil")
	}
	if err := p.Validate(); err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	if err := cfg.validateRunLimits(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := p.checkHourlyCap(cfg.MaxHourlyCommitPerRun); err != nil {
		return err
	}
	return p.checkActionCap(cfg.MaxActionsPerRun)
}

func (p *LadderPlan) checkHourlyCap(maxPerRun *float64) error {
	if maxPerRun == nil {
		return nil
	}
	limit, err := ratFromFloat("max_hourly_commit_per_run", *maxPerRun)
	if err != nil {
		return err
	}
	total := new(big.Rat)
	for _, a := range p.Actions {
		if a.Action == ActionPurchase {
			total.Add(total, a.AmountUSDPerHour)
		}
	}
	if total.Cmp(limit) > 0 {
		return fmt.Errorf("purchases total %s exceed max_hourly_commit_per_run %s",
			fmtRatUSD(total), fmtRatUSD(limit))
	}
	return nil
}

func (p *LadderPlan) checkActionCap(maxActions int) error {
	money := 0
	for _, a := range p.Actions {
		if a.Action == ActionPurchase || a.Action == ActionReshape {
			money++
		}
	}
	if money > maxActions {
		return fmt.Errorf("%d purchase and reshape actions exceed max_actions_per_run %d", money, maxActions)
	}
	return nil
}
