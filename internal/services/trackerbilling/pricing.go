// Package trackerbilling holds the server-side mirror of Bogie Tracker's
// plan pricing (base_amount lookup at order-creation time — never trusted
// from the client, see migration 032) and the tax-invoice PDF generator used
// once an order is marked paid.
package trackerbilling

import "fmt"

// GSTRate is the GST percentage applied to every tracker plan order's
// base_amount. Mirrors app/bogie-tracker/TrackerPricing.tsx on the
// marketing site — kept in sync manually since there's no shared package
// between the two repos/deploys.
const GSTRate = 0.18

// planPricing maps plan -> billing_duration -> base_amount (INR, the full
// period total actually billed — e.g. Single User Quarterly is billed at
// Rs.375/month x 3 months = Rs.1125, not Rs.375). "lifetime" only sells with
// "onetime"; every other plan only sells with the four recurring durations.
var planPricing = map[string]map[string]float64{
	"single": {
		"monthly":    500,
		"quarterly":  1125,
		"halfYearly": 2100,
		"yearly":     4020,
	},
	"2users": {
		"monthly":    700,
		"quarterly":  1575,
		"halfYearly": 2940,
		"yearly":     5628,
	},
	"5users": {
		"monthly":    1000,
		"quarterly":  2250,
		"halfYearly": 4200,
		"yearly":     8040,
	},
	"mega": {
		"monthly":    2000,
		"quarterly":  4500,
		"halfYearly": 8400,
		"yearly":     16080,
	},
	"lifetime": {
		"onetime": 20000,
	},
}

// Lookup returns the base/GST/total amounts for a plan+billing_duration
// combination, or an error if that combination isn't sold — including valid
// individual values that just aren't paired together (e.g. "lifetime" +
// "monthly").
func Lookup(plan, billingDuration string) (base, gst, total float64, err error) {
	durations, ok := planPricing[plan]
	if !ok {
		return 0, 0, 0, fmt.Errorf("unknown plan: %s", plan)
	}
	base, ok = durations[billingDuration]
	if !ok {
		return 0, 0, 0, fmt.Errorf("plan %q is not sold with billing_duration %q", plan, billingDuration)
	}
	base = round2(base)
	gst = round2(base * GSTRate)
	total = round2(base + gst)
	return base, gst, total, nil
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
