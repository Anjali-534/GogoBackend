package trackerbilling

// dispatchesPerDay mirrors app/bogie-tracker/TrackerPricing.tsx's per-plan
// dispatchesPerDay feature — kept in sync manually, same as GSTRate/planPricing.
// -1 means unlimited (mega, lifetime).
var dispatchesPerDay = map[string]int{
	"single":   5,
	"2users":   10,
	"5users":   30,
	"mega":     -1,
	"lifetime": -1,
}

// DispatchLimit returns the daily dispatch cap for a plan, and whether the
// plan is unlimited. ok is false for an unrecognized plan — callers must
// treat that as "no limit info", not as "limit of zero".
func DispatchLimit(plan string) (limit int, unlimited bool, ok bool) {
	v, ok := dispatchesPerDay[plan]
	if !ok {
		return 0, false, false
	}
	if v == -1 {
		return 0, true, true
	}
	return v, false, true
}

// tierRank orders plans low -> high for upgrade/downgrade comparisons.
// Mirrors bogie-tracker-panel/lib/types.ts's PLAN_TIER_ORDER — kept in sync
// manually, same as dispatchesPerDay/planPricing. The backend is the
// authoritative copy: CreateTrackerPlanOrder must never trust a client's own
// notion of which plan outranks which for anything that blocks/allows an
// order.
var tierRank = map[string]int{
	"single":   0,
	"2users":   1,
	"5users":   2,
	"mega":     3,
	"lifetime": 99,
}

// TierRank returns a plan's rank for upgrade/downgrade comparisons — higher
// means a higher tier. ok is false for an unrecognized plan.
func TierRank(plan string) (rank int, ok bool) {
	rank, ok = tierRank[plan]
	return rank, ok
}

// panelLoginStaffCap is the number of ADDITIONAL staff logins a plan allows,
// beyond the owner. The pricing page's "1/2/5/unlimited" figure is the plan's
// TOTAL login count including the owner, so this is that number minus one —
// e.g. "5 Users" = owner + 4 staff. -1 means unlimited (mega, lifetime).
// "single" is 0: that plan is owner-only, no staff logins at all.
var panelLoginStaffCap = map[string]int{
	"single":   0,
	"2users":   1,
	"5users":   4,
	"mega":     -1,
	"lifetime": -1,
}

// PanelLoginStaffCap returns how many staff logins (beyond the owner) a plan
// allows, and whether it's unlimited. ok is false for an unrecognized plan —
// callers must treat that as "no limit info", not as "limit of zero".
func PanelLoginStaffCap(plan string) (cap int, unlimited bool, ok bool) {
	v, ok := panelLoginStaffCap[plan]
	if !ok {
		return 0, false, false
	}
	if v == -1 {
		return 0, true, true
	}
	return v, false, true
}
