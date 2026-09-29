// Command invoice_email_send test-sends one post-ride invoice email through
// the real invoicemail/mail code path, without waiting for a ride to
// complete. It ignores INVOICE_EMAIL_ENABLED (it only runs when you run it)
// but needs RESEND_API_KEY (and optionally RESEND_FROM_EMAIL) from the
// environment or .env.
//
// Fake mode — no database, no real booking; made-up invoice data:
//
//	go run ./cmd/invoice_email_send -fake -to delivered@resend.dev
//	go run ./cmd/invoice_email_send -fake -kind goods -to delivered@resend.dev
//	go run ./cmd/invoice_email_send -fake -dry-run   # write the email to disk, send nothing
//
// Booking mode — loads a real invoiced booking from the DB_* database (.env):
//
//	go run ./cmd/invoice_email_send -booking <id> -to delivered@resend.dev
//	go run ./cmd/invoice_email_send -booking <id> -to delivered@resend.dev -record
//
// -to is mandatory and always replaces the rider's address, so a real rider
// is never emailed. Without -record nothing is written to the database (no
// claim, no attempt count, no idempotency key, so it can be repeated). With
// -record it goes through the full claim/record path exactly like production,
// including the Idempotency-Key — after a successful -record the booking is
// marked sent and won't send again. A non-localhost DB_HOST is refused
// unless -allow-remote is passed.
//
// Resend test recipients: delivered@resend.dev (accepted and marked
// delivered, reaches no real inbox), bounced@resend.dev, complained@resend.dev.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
	"github.com/deploykit/backend/internal/mail"
	"github.com/deploykit/backend/internal/services/bookinginvoice"
	"github.com/deploykit/backend/internal/services/invoicemail"
	"github.com/joho/godotenv"
)

func main() {
	fake := flag.Bool("fake", false, "send a made-up invoice; no database")
	kind := flag.String("kind", "ride", "with -fake: ride | goods")
	fakeReceipt := flag.Bool("receipt", false, "with -fake: render with an empty issuer (Receipt variant) instead of INVOICE_ISSUER_* from env")
	bookingID := flag.String("booking", "", "invoiced booking id to load from the DB_* database")
	to := flag.String("to", "", "recipient (required unless -dry-run); always replaces the rider's address")
	record := flag.Bool("record", false, "with -booking: claim and record on the bookings row like production")
	allowRemote := flag.Bool("allow-remote", false, "with -booking: allow a DB_HOST other than localhost")
	dryRun := flag.Bool("dry-run", false, "write subject/HTML/text/PDF to -out instead of sending")
	outDir := flag.String("out", filepath.Join(os.TempDir(), "bogie-invoice-email"), "with -dry-run: output directory")
	flag.Parse()

	if *fake == (*bookingID != "") {
		log.Fatal("pass exactly one of -fake or -booking <id>")
	}
	if *to == "" && !*dryRun {
		log.Fatal("-to is required (e.g. -to delivered@resend.dev)")
	}
	if *record && (*fake || *dryRun) {
		log.Fatal("-record only applies to a real send in -booking mode")
	}

	_ = godotenv.Load()
	cfg := config.Load()
	if !*dryRun && !mail.IsConfigured(cfg) {
		log.Fatal("RESEND_API_KEY / RESEND_FROM_EMAIL not set (env or .env)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var msg mail.Message
	if *fake {
		issuer := cfg.InvoiceIssuer
		if *fakeReceipt {
			issuer = bookinginvoice.Issuer{}
		}
		var err error
		msg, err = invoicemail.Compose(fakeInvoice(*kind), issuer, *to)
		if err != nil {
			log.Fatalf("compose: %v", err)
		}
	} else {
		if !*allowRemote && cfg.DBHost != "localhost" && cfg.DBHost != "127.0.0.1" {
			log.Fatalf("DB_HOST is %q, not localhost — pass -allow-remote if you really mean it", cfg.DBHost)
		}
		if err := db.Init(cfg); err != nil {
			log.Fatalf("database: %v", err)
		}
		if *record {
			outcome := invoicemail.SendRedirected(ctx, cfg, *bookingID, *to)
			fmt.Printf("booking %s: %s (see bookings.invoice_last_error / invoice_email_* for details)\n", *bookingID, outcome)
			return
		}
		m, skip, err := invoicemail.Preview(ctx, cfg, *bookingID, *to)
		if err != nil {
			log.Fatalf("load booking %s: %v", *bookingID, err)
		}
		if skip != "" {
			fmt.Printf("booking %s would be skipped in production: %s — not sending\n", *bookingID, skip)
			return
		}
		msg = m
	}

	fmt.Printf("subject:    %s\nattachment: %s (%d bytes)\n", msg.Subject, msg.Attachments[0].Filename, len(msg.Attachments[0].Data))

	if *dryRun {
		if err := os.MkdirAll(*outDir, 0o755); err != nil {
			log.Fatalf("create %s: %v", *outDir, err)
		}
		files := map[string][]byte{
			"email.html":                []byte(msg.HTMLBody),
			"email.txt":                 []byte("Subject: " + msg.Subject + "\n\n" + msg.Body),
			msg.Attachments[0].Filename: msg.Attachments[0].Data,
		}
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(*outDir, name), data, 0o644); err != nil {
				log.Fatalf("write %s: %v", name, err)
			}
		}
		fmt.Printf("dry run: wrote email.html, email.txt and the PDF to %s — nothing sent\n", *outDir)
		return
	}

	if err := mail.Send(cfg, msg); err != nil {
		log.Fatalf("send failed: %v", err)
	}
	fmt.Printf("sent to %s — check the Resend dashboard's email log\n", *to)
}

// fakeInvoice is made-up data only, same spirit as cmd/invoice_sample.
func fakeInvoice(kind string) *bookinginvoice.Invoice {
	started := time.Now().Add(-45 * time.Minute)
	completed := time.Now().Add(-5 * time.Minute)
	inv := &bookinginvoice.Invoice{
		Number:        "BGR/2627/99999",
		IssuedAt:      completed,
		BookingID:     "00000000-0000-0000-0000-000000000000",
		Kind:          bookinginvoice.KindRide,
		RiderName:     "Sample Rider <test & escaping>",
		ServiceName:   "Sample Cab",
		VehicleType:   "Sample Sedan",
		VehicleNumber: "XX 00 XX 0000",
		DriverName:    "Sample Driver",
		PaymentMethod: "wallet",
		PickupAddress: "Sample Apartments (Tower B), 12 Example Street, Sample City 000000",
		DropAddress:   `Sample Mall, Gate "3" & Food Court`,
		StartedAt:     &started,
		CompletedAt:   &completed,
		DistanceKm:    14.2,
		TripFare:      312,
		Discount:      50,
		PromoCode:     "SAMPLE50",
		TotalAmount:   262,
	}
	if kind == "goods" {
		inv.Kind = bookinginvoice.KindGoods
		inv.ServiceName = "Sample Mini Truck"
		inv.VehicleType = "Sample Mini Truck"
		inv.ConsigneeName = "Sample Consignee"
		inv.ConsigneePhone = "0000000000"
		inv.Discount, inv.PromoCode, inv.TripFare, inv.TotalAmount = 0, "", 1150, 1150
	} else if kind != "ride" {
		log.Fatalf("-kind must be ride or goods, got %q", kind)
	}
	return inv
}
