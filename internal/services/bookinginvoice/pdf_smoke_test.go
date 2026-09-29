package bookinginvoice

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

var testIssuer = Issuer{
	LegalName: "Sample Issuer (Not Real)",
	Address:   "Sample address line, Sample City",
	State:     "Sample State",
	GSTIN:     "SAMPLE-GSTIN-NOT-REAL",
}

func testInvoice(kind Kind) *Invoice {
	started := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	completed := started.Add(35 * time.Minute)
	return &Invoice{
		Number:    "BGR/2627/00001",
		IssuedAt:  completed,
		BookingID: "00000000-0000-0000-0000-000000000000",
		Kind:      kind,
		RiderName: "Test Rider",
		// Hostile text: PDF string delimiters, backslash, control chars,
		// newlines, Devanagari the core font can't draw, and an address long
		// enough to need wrapping.
		PickupAddress: "Flat 4 (2nd floor) \\ Block C\r\nSector 62, " + strings.Repeat("Long Road Name ", 12),
		DropAddress:   "राम नगर, Test Colony\t\x00\x07 End",
		ServiceName:   "Test Service",
		VehicleType:   "Test Vehicle",
		VehicleNumber: "XX 00 XX 0000",
		DriverName:    "Test Driver Surname",
		PaymentMethod: "wallet",
		StartedAt:     &started,
		CompletedAt:   &completed,
		DistanceKm:    12.34,
		TripFare:      250,
		Discount:      25,
		PromoCode:     "TESTPROMO",
		TotalAmount:   225,
	}
}

// Every variant x issuer combination must render a well-formed, non-empty PDF.
func TestGenerateSmoke(t *testing.T) {
	for _, kind := range []Kind{KindRide, KindGoods} {
		for _, issuer := range []Issuer{testIssuer, {}} {
			out, err := Generate(testInvoice(kind), issuer)
			if err != nil {
				t.Fatalf("Generate(kind=%d, complete=%v) failed: %v", kind, issuer.Complete(), err)
			}
			if !bytes.HasPrefix(out, []byte("%PDF-")) {
				t.Fatalf("Generate(kind=%d, complete=%v): missing PDF header", kind, issuer.Complete())
			}
		}
	}
}

func TestTitle(t *testing.T) {
	partial := testIssuer
	partial.GSTIN = "  "
	cases := []struct {
		kind      Kind
		issuer    Issuer
		wantTitle string
		wantLabel string
	}{
		{KindRide, testIssuer, "Invoice", "Invoice No"},
		{KindGoods, testIssuer, "Bill of Supply", "Bill No"},
		{KindRide, Issuer{}, "Receipt", "Receipt No"},
		{KindGoods, Issuer{}, "Receipt", "Receipt No"},
		{KindGoods, partial, "Receipt", "Receipt No"}, // one blank mandatory field is enough
	}
	for _, tc := range cases {
		if got := Title(tc.kind, tc.issuer); got != tc.wantTitle {
			t.Errorf("Title(%d, complete=%v) = %q, want %q", tc.kind, tc.issuer.Complete(), got, tc.wantTitle)
		}
		if got := NumberLabel(tc.kind, tc.issuer); got != tc.wantLabel {
			t.Errorf("NumberLabel(%d, complete=%v) = %q, want %q", tc.kind, tc.issuer.Complete(), got, tc.wantLabel)
		}
	}
}

// A zero discount must print "Rs.0.00", never "Rs.-0.00" (negated zero).
func TestZeroDiscountIsNotNegativeZero(t *testing.T) {
	inv := testInvoice(KindGoods)
	inv.Discount, inv.PromoCode = 0, ""
	for _, r := range FareRows(inv) {
		if r.Label == "Discount" && formatRupees(r.Amount) != "Rs.0.00" {
			t.Errorf("zero discount rendered as %q", formatRupees(r.Amount))
		}
	}
}

func TestFareRowsAddUp(t *testing.T) {
	inv := testInvoice(KindRide)
	inv.CarriedCancellationFee = 30
	inv.TotalAmount = 255.4 // 250 - 25 + 30 = 255, charged 255.40 -> rounding 0.40

	rows := FareRows(inv)
	byLabel := map[string]float64{}
	for _, r := range rows {
		byLabel[r.Label] = r.Amount
	}
	checks := map[string]float64{
		"Trip Fare":                 250,
		"Discount (TESTPROMO)":      -25,
		"Previous cancellation fee": 30,
		"Sub Total":                 255,
		"Rounding":                  0.4,
		"Net Fare":                  255.4,
		"Total Amount":              255.4,
	}
	for label, want := range checks {
		if got, ok := byLabel[label]; !ok || got != want {
			t.Errorf("row %q = %v (present=%v), want %v", label, got, ok, want)
		}
	}
	if last := rows[len(rows)-1]; last.Label != "Total Amount" || !last.Bold {
		t.Errorf("last row must be bold Total Amount, got %+v", last)
	}

	inv.CarriedCancellationFee = 0
	for _, r := range FareRows(inv) {
		if r.Label == "Previous cancellation fee" {
			t.Error("zero carried fee must not print a row")
		}
	}
}

func TestTextCleaner(t *testing.T) {
	clean := newTextCleaner(func(s string) string {
		// Stand-in for gofpdf's cp1252 translator: ASCII passes, anything
		// else becomes "." — which is exactly what the cleaner must pre-empt.
		var b strings.Builder
		for _, r := range s {
			if r < 0x80 {
				b.WriteRune(r)
			} else {
				b.WriteByte('.')
			}
		}
		return b.String()
	})
	cases := map[string]string{
		"a\r\nb\tc\x00d":   "a b c d",
		"  many   spaces ": "many spaces",
		"राम (Ram)":        "??? (Ram)",
		`paren ( \ )`:      `paren ( \ )`, // escaping is gofpdf's job on output
	}
	for in, want := range cases {
		if got := clean(in); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKindForCategory(t *testing.T) {
	for cat, want := range map[string]Kind{"truck": KindGoods, "parcel": KindGoods, "cab": KindRide, "ambulance": KindRide, "": KindRide} {
		if got := KindForCategory(cat); got != want {
			t.Errorf("KindForCategory(%q) = %d, want %d", cat, got, want)
		}
	}
}
