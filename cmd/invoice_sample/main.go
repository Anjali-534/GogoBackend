// Command invoice_sample renders the four post-ride invoice PDF variants
// from made-up data so they can be reviewed locally. It never connects to a
// database, reads a real booking, or sends anything.
//
//	go run ./cmd/invoice_sample              # writes to <temp>/bogie-invoice-samples
//	go run ./cmd/invoice_sample -out <dir>
//
// By default the "complete issuer" samples use an obviously fake issuer.
// Pass -issuer-from-env to use the INVOICE_ISSUER_* env vars (and .env)
// instead — if those are incomplete, those samples render as receipts too.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/services/bookinginvoice"
	"github.com/joho/godotenv"
)

func main() {
	// Defaults outside the repo so sample PDFs never end up committed.
	outDir := flag.String("out", filepath.Join(os.TempDir(), "bogie-invoice-samples"), "directory to write the sample PDFs into")
	fromEnv := flag.Bool("issuer-from-env", false, "use INVOICE_ISSUER_* env vars instead of the fake issuer")
	flag.Parse()

	issuer := bookinginvoice.Issuer{
		LegalName:    "Sample Issuer Pvt. Ltd. (NOT REAL)",
		Address:      "Sample Tower, 1 Example Road, Sample City 000000",
		State:        "Sample State",
		GSTIN:        "SAMPLE-GSTIN-NOT-REAL",
		PAN:          "SAMPLE-PAN",
		SACRide:      "SAMPLE-SAC-RIDE",
		SACGoods:     "SAMPLE-SAC-GOODS",
		SupportEmail: "support@example.invalid",
	}
	if *fromEnv {
		_ = godotenv.Load()
		issuer = config.Load().InvoiceIssuer
		if !issuer.Complete() {
			log.Printf("note: INVOICE_ISSUER_* env is incomplete — the 'complete' samples will render as receipts")
		}
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("create %s: %v", *outDir, err)
	}

	samples := []struct {
		file   string
		inv    *bookinginvoice.Invoice
		issuer bookinginvoice.Issuer
	}{
		{"ride-invoice.pdf", rideSample(), issuer},
		{"ride-receipt.pdf", rideSample(), bookinginvoice.Issuer{}},
		{"goods-bill-of-supply.pdf", goodsSample(), issuer},
		{"goods-receipt.pdf", goodsSample(), bookinginvoice.Issuer{}},
	}
	for _, s := range samples {
		out, err := bookinginvoice.Generate(s.inv, s.issuer)
		if err != nil {
			log.Fatalf("%s: %v", s.file, err)
		}
		path := filepath.Join(*outDir, s.file)
		if err := os.WriteFile(path, out, 0o644); err != nil {
			log.Fatalf("write %s: %v", path, err)
		}
		fmt.Printf("wrote %s (%s)\n", path, bookinginvoice.Title(s.inv.Kind, s.issuer))
	}
}

func rideSample() *bookinginvoice.Invoice {
	started := time.Date(2026, 9, 29, 9, 12, 0, 0, time.UTC)
	completed := started.Add(38 * time.Minute)
	return &bookinginvoice.Invoice{
		Number:        "BGR/2627/00001",
		IssuedAt:      completed,
		BookingID:     "00000000-0000-0000-0000-000000000001",
		Kind:          bookinginvoice.KindRide,
		RiderName:     "Sample Rider",
		ServiceName:   "Sample Cab",
		VehicleType:   "Sedan",
		VehicleNumber: "XX 00 XX 0000",
		DriverName:    "Sample Driver",
		PaymentMethod: "wallet",
		PickupAddress: "Sample Apartments (Tower B), 12 Example Street, Sample Nagar, Sample City 000000",
		DropAddress:   "Sample Mall, Gate 3, Example Road",
		StartedAt:     &started,
		CompletedAt:   &completed,
		DistanceKm:    14.2,
		TripFare:      312,
		Discount:      50,
		PromoCode:     "SAMPLE50",
		// Previous-ride cancellation fee folded into this fare.
		CarriedCancellationFee: 30,
		TotalAmount:            292,
	}
}

func goodsSample() *bookinginvoice.Invoice {
	started := time.Date(2026, 9, 29, 14, 5, 0, 0, time.UTC)
	completed := started.Add(95 * time.Minute)
	return &bookinginvoice.Invoice{
		Number:        "BGR/2627/00002",
		IssuedAt:      completed,
		BookingID:     "00000000-0000-0000-0000-000000000002",
		Kind:          bookinginvoice.KindGoods,
		RiderName:     "Sample Consignor",
		ServiceName:   "Sample Mini Truck",
		VehicleType:   "Mini Truck",
		VehicleNumber: "XX 00 XX 0001",
		DriverName:    "Sample Driver",
		PaymentMethod: "cash",
		// Long, punctuation-heavy address to show wrapping + escaping.
		PickupAddress: "Warehouse 7 (Rear Entrance) \\ Loading Bay 2, " + strings.Repeat("Example Industrial Area Road, ", 4) + "Sample City",
		// Devanagari shows how the core font handles unsupported characters.
		DropAddress:    "राम नगर (Ram Nagar), Sample Colony, Sample City",
		ConsigneeName:  "Sample Consignee",
		ConsigneePhone: "0000000000",
		StartedAt:      &started,
		CompletedAt:    &completed,
		DistanceKm:     27.8,
		TripFare:       1150, // includes loading/unloading addons
		TotalAmount:    1150,
	}
}
