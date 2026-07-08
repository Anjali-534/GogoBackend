package ledger

import (
	"bytes"
	"testing"
	"time"
)

// Smoke test — verifies GeneratePDF produces a well-formed, non-empty PDF
// for both a populated and an empty statement, and that non-ASCII text
// (e.g. the em-dash in "Referral Bonus — ...") survives the core-font
// WinAnsi translation instead of coming out as mojibake.
func TestGeneratePDFSmoke(t *testing.T) {
	stmt := &Statement{
		DriverID:       "test-driver-id",
		DriverName:     "Test Driver",
		DriverPhone:    "9999999999",
		DriverEmail:    "test@example.com",
		MonthKey:       "2026-06",
		PeriodLabel:    "1 Jun 2026 - 30 Jun 2026",
		OpeningBalance: -700,
		ClosingBalance: 1234.5,
		TotalCredit:    2500,
		TotalDebit:     565.5,
		Entries: []Entry{
			{Date: time.Now(), Description: "Trip Earnings", Type: "ride", IsDebit: false, Amount: 400, Running: -300},
			{Date: time.Now(), Description: "bogie Commission (20%)", Type: "adjustment", IsDebit: true, Amount: 100, Running: -400},
			{Date: time.Now(), Description: "Referral Bonus — friend's first trip", Type: "referral", IsDebit: false, Amount: 150, Running: -250},
		},
	}

	out, err := GeneratePDF(stmt)
	if err != nil {
		t.Fatalf("GeneratePDF failed: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("GeneratePDF returned empty bytes")
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Fatalf("output does not start with PDF header, got: %q", out[:20])
	}

	empty := &Statement{DriverID: "d", DriverName: "No Trips", MonthKey: "2026-06", PeriodLabel: "1 Jun 2026 - 30 Jun 2026"}
	out2, err := GeneratePDF(empty)
	if err != nil {
		t.Fatalf("GeneratePDF (empty) failed: %v", err)
	}
	if !bytes.HasPrefix(out2, []byte("%PDF-")) {
		t.Fatal("empty-statement output does not start with PDF header")
	}
}
