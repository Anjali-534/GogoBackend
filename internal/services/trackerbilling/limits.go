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
