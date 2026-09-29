// Package bookinginvoice renders post-ride invoice PDFs for completed
// bookings. Pure functions only: no DB access and no email — callers build
// an Invoice from the booking row and pass the issuer from config.
package bookinginvoice

import (
	"math"
	"strings"
	"time"
)

// Kind selects the document variant.
type Kind int

const (
	// KindRide is the passenger-trip variant (cab, ambulance).
	KindRide Kind = iota
	// KindGoods is the consignment variant (truck, parcel).
	KindGoods
)

// KindForCategory maps a service_types.category to its document variant.
func KindForCategory(category string) Kind {
	switch category {
	case "truck", "parcel":
		return KindGoods
	}
	return KindRide
}

// Issuer is the business issuing the document, read from env via config —
// never hardcoded. LegalName, Address, State and GSTIN are mandatory for an
// invoice / bill of supply; if any is blank the document falls back to a
// plain "Receipt" (see Complete).
type Issuer struct {
	LegalName    string
	Address      string
	State        string
	GSTIN        string
	PAN          string // optional
	CIN          string // optional
	SACRide      string // optional, shown on the ride variant when set
	SACGoods     string // optional, shown on the goods variant when set
	SupportEmail string // optional
}

// Complete reports whether every mandatory issuer field is set.
func (i Issuer) Complete() bool {
	for _, v := range []string{i.LegalName, i.Address, i.State, i.GSTIN} {
		if strings.TrimSpace(v) == "" {
			return false
		}
	}
	return true
}

// Invoice is everything the PDF needs for one completed booking.
type Invoice struct {
	Number    string // bookings.invoice_number, e.g. BGR/2627/00001
	IssuedAt  time.Time
	BookingID string
	Kind      Kind

	RiderName     string // "Billed to" / consignor
	ServiceName   string
	VehicleType   string
	VehicleNumber string
	DriverName    string // full name; only the first name is printed
	PaymentMethod string // bookings.payment_method: cash | wallet | company_wallet

	PickupAddress string
	DropAddress   string
	StartedAt     *time.Time
	CompletedAt   *time.Time
	DistanceKm    float64

	// Goods variant only. Goods type/weight/packages/declared value aren't
	// collected at booking time yet, so they always print "Not declared".
	ConsigneeName  string // bookings.receiver_name
	ConsigneePhone string // bookings.receiver_phone

	// Fare breakdown (migration 063). TotalAmount is bookings.final_fare —
	// the amount actually charged — and is always the bottom line.
	TripFare               float64
	Discount               float64
	PromoCode              string
	CarriedCancellationFee float64
	TotalAmount            float64
}

// Title is the document heading. Without complete issuer details the
// document is a plain "Receipt" — no invoice / bill-of-supply wording.
func Title(kind Kind, issuer Issuer) string {
	if !issuer.Complete() {
		return "Receipt"
	}
	if kind == KindGoods {
		return "Bill of Supply"
	}
	return "Invoice"
}

// NumberLabel is the label printed before the document number, matching
// Title's wording.
func NumberLabel(kind Kind, issuer Issuer) string {
	if !issuer.Complete() {
		return "Receipt No"
	}
	if kind == KindGoods {
		return "Bill No"
	}
	return "Invoice No"
}

// FareRow is one line of the fare table. Signed amounts: discounts negative.
type FareRow struct {
	Label  string
	Amount float64
	Bold   bool
}

// FareRows builds the fare table: Trip Fare, Discount, carried cancellation
// fee (only when non-zero), Sub Total, Rounding, Net Fare, Total Amount.
// Rounding absorbs whatever separates the itemized Sub Total from the
// amount actually charged, so Total Amount always equals TotalAmount.
//
// Tax lines, when a tax treatment is decided, belong between Net Fare and
// Total Amount — none are added here.
func FareRows(inv *Invoice) []FareRow {
	discountLabel := "Discount"
	if inv.PromoCode != "" {
		discountLabel = "Discount (" + inv.PromoCode + ")"
	}

	rows := []FareRow{
		{Label: "Trip Fare", Amount: round2(inv.TripFare)},
		{Label: discountLabel, Amount: round2(-inv.Discount)}, // round2 after negating: no -0
	}
	if inv.CarriedCancellationFee != 0 {
		rows = append(rows, FareRow{Label: "Previous cancellation fee", Amount: round2(inv.CarriedCancellationFee)})
	}

	subTotal := round2(inv.TripFare - inv.Discount + inv.CarriedCancellationFee)
	total := round2(inv.TotalAmount)
	rounding := round2(total - subTotal)
	netFare := round2(subTotal + rounding)

	rows = append(rows,
		FareRow{Label: "Sub Total", Amount: subTotal},
		FareRow{Label: "Rounding", Amount: rounding},
		FareRow{Label: "Net Fare", Amount: netFare},
		FareRow{Label: "Total Amount", Amount: total, Bold: true},
	)
	return rows
}

func round2(v float64) float64 {
	r := math.Round(v*100) / 100
	if r == 0 {
		return 0 // normalize -0
	}
	return r
}
