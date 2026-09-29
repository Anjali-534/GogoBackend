package dateutil

import (
	"testing"
	"time"
)

func TestFinancialYearCode(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"mid-year", time.Date(2026, 9, 29, 12, 0, 0, 0, ISTLocation), "2627"},
		{"first instant of FY (IST)", time.Date(2026, 4, 1, 0, 0, 0, 0, ISTLocation), "2627"},
		{"last instant of previous FY (IST)", time.Date(2026, 3, 31, 23, 59, 59, 0, ISTLocation), "2526"},
		// 31 Mar 19:00 UTC is already 1 Apr 00:30 IST — the IST boundary wins.
		{"UTC still 31 March, IST already 1 April", time.Date(2026, 3, 31, 19, 0, 0, 0, time.UTC), "2627"},
		// 31 Mar 18:00 UTC is 31 Mar 23:30 IST — still the old FY.
		{"UTC and IST both 31 March", time.Date(2026, 3, 31, 18, 0, 0, 0, time.UTC), "2526"},
		{"January belongs to FY that started previous April", time.Date(2027, 1, 15, 0, 0, 0, 0, ISTLocation), "2627"},
		{"century crossover", time.Date(2099, 12, 1, 0, 0, 0, 0, ISTLocation), "9900"},
	}
	for _, tc := range cases {
		if got := FinancialYearCode(tc.at); got != tc.want {
			t.Errorf("%s: FinancialYearCode(%v) = %q, want %q", tc.name, tc.at, got, tc.want)
		}
	}
}
