package dateutil

import (
	"fmt"
	"time"
)

// FinancialYearCode returns the Indian financial year (1 April - 31 March,
// IST) that t falls in, as the two-digit crossover code used in invoice
// numbers: any instant in FY 2026-27 returns "2627". The boundary is
// midnight 1 April in IST, not UTC — the server clock runs in UTC on
// Railway, where 1 April 00:00-05:29 IST is still 31 March.
func FinancialYearCode(t time.Time) string {
	ist := t.In(ISTLocation)
	start := ist.Year()
	if ist.Month() < time.April {
		start--
	}
	return fmt.Sprintf("%02d%02d", start%100, (start+1)%100)
}
