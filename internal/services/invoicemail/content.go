package invoicemail

import (
	"fmt"
	"html"
	"strings"

	"github.com/deploykit/backend/internal/dateutil"
	"github.com/deploykit/backend/internal/mail"
	"github.com/deploykit/backend/internal/services/bookinginvoice"
)

// idempotencyKey is the Resend Idempotency-Key for a booking's invoice email,
// in Resend's recommended <event-type>/<entity-id> shape.
func idempotencyKey(bookingID string) string {
	return "invoice-email/" + bookingID
}

// docWord is the lower-case document name used in the subject and filename,
// following bookinginvoice.Title: invoice / bill of supply / receipt.
func docWord(kind bookinginvoice.Kind, issuer bookinginvoice.Issuer) string {
	return strings.ToLower(bookinginvoice.Title(kind, issuer))
}

// tripWord is what the booking is called in the subject line.
func tripWord(kind bookinginvoice.Kind) string {
	if kind == bookinginvoice.KindGoods {
		return "booking"
	}
	return "ride"
}

// tripDate is the ride's date in IST: completion time, else issue time.
func tripDate(inv *bookinginvoice.Invoice) string {
	t := inv.IssuedAt
	if inv.CompletedAt != nil {
		t = *inv.CompletedAt
	}
	return t.In(dateutil.ISTLocation).Format("2 Jan 2006")
}

func subject(inv *bookinginvoice.Invoice, issuer bookinginvoice.Issuer) string {
	return fmt.Sprintf("Your Bogie %s %s for your %s on %s",
		docWord(inv.Kind, issuer), inv.Number, tripWord(inv.Kind), tripDate(inv))
}

// attachmentName is e.g. bogie-invoice-BGR-2627-00001.pdf. Invoice numbers
// contain "/", which can't appear in a filename.
func attachmentName(inv *bookinginvoice.Invoice, issuer bookinginvoice.Issuer) string {
	word := strings.ReplaceAll(docWord(inv.Kind, issuer), " ", "-")
	return fmt.Sprintf("bogie-%s-%s.pdf", word, strings.ReplaceAll(inv.Number, "/", "-"))
}

func rupees(v float64) string {
	return fmt.Sprintf("Rs. %.2f", v)
}

// oneLine collapses whitespace (including newlines) so a user-entered
// address can't break the plain-text layout.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// buildMessage assembles the email for one invoice. pdf is the output of
// bookinginvoice.Generate for the same inv and issuer. Every value that came
// from a user or the booking row is passed through html.EscapeString before
// it goes into the HTML body — bookinginvoice escapes for the PDF only.
func buildMessage(inv *bookinginvoice.Invoice, issuer bookinginvoice.Issuer, to string, pdf []byte) mail.Message {
	doc := docWord(inv.Kind, issuer)
	trip := tripWord(inv.Kind)
	date := tripDate(inv)
	pickup := oneLine(inv.PickupAddress)
	drop := oneLine(inv.DropAddress)
	name := oneLine(inv.RiderName)
	total := rupees(inv.TotalAmount)

	greeting := "Hi,"
	if name != "" {
		greeting = "Hi " + name + ","
	}
	support := "Questions about this " + trip + "? Reply to this email"
	if issuer.SupportEmail != "" {
		support += " or write to " + issuer.SupportEmail
	}
	support += "."

	text := fmt.Sprintf(
		"%s\n\nThanks for choosing Bogie. Your %s %s for your %s on %s is attached.\n\n"+
			"From: %s\nTo: %s\nDate: %s\nTotal: %s\n\n%s\n\n— Team Bogie",
		greeting, doc, inv.Number, trip, date, pickup, drop, date, total, support,
	)

	esc := html.EscapeString
	var b strings.Builder
	b.WriteString(`<div style="font-family:Arial,Helvetica,sans-serif;color:#111827;max-width:560px;">`)
	b.WriteString(`<p style="font-size:22px;font-weight:bold;color:#FF6B2B;margin:0 0 16px;">Bogie</p>`)
	b.WriteString(`<p style="font-size:14px;margin:0 0 12px;">` + esc(greeting) + `</p>`)
	b.WriteString(`<p style="font-size:14px;margin:0 0 16px;">Thanks for choosing Bogie. Your ` + esc(doc) + ` <strong>` +
		esc(inv.Number) + `</strong> for your ` + esc(trip) + ` on ` + esc(date) + ` is attached.</p>`)
	b.WriteString(`<table style="border-collapse:collapse;font-size:14px;margin:0 0 16px;">`)
	for _, row := range [][2]string{{"From", pickup}, {"To", drop}, {"Date", date}, {"Total", total}} {
		b.WriteString(`<tr><td style="padding:4px 16px 4px 0;color:#6B7280;vertical-align:top;">` + esc(row[0]) +
			`</td><td style="padding:4px 0;">` + esc(row[1]) + `</td></tr>`)
	}
	b.WriteString(`</table>`)
	b.WriteString(`<p style="font-size:13px;color:#6B7280;margin:0 0 4px;">` + esc(support) + `</p>`)
	b.WriteString(`<p style="font-size:13px;color:#6B7280;margin:0;">— Team Bogie</p>`)
	b.WriteString(`</div>`)

	return mail.Message{
		To:       to,
		Subject:  subject(inv, issuer),
		Body:     text,
		HTMLBody: b.String(),
		FromName: "Bogie",
		Attachments: []mail.Attachment{{
			Filename:    attachmentName(inv, issuer),
			ContentType: "application/pdf",
			Data:        pdf,
		}},
	}
}
