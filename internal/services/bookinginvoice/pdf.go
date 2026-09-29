package bookinginvoice

import (
	"bytes"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/deploykit/backend/internal/dateutil"
	"github.com/jung-kurt/gofpdf"
)

const (
	brandOrangeR, brandOrangeG, brandOrangeB = 255, 107, 43 // #FF6B2B
	pageMarginMM                             = 15
	labelColMM                               = 38
	notDeclared                              = "Not declared"
)

// Generate renders the invoice as a one-page A4 PDF — same visual language
// as trackerbilling.GenerateInvoicePDF and ledger.GeneratePDF (brand-orange
// wordmark, orange table header). The title and issuer block depend on
// whether issuer is Complete: see Title.
func Generate(inv *Invoice, issuer Issuer) ([]byte, error) {
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(pageMarginMM, pageMarginMM, pageMarginMM)
	pdf.SetAutoPageBreak(true, 20)
	pdf.AddPage()

	w := &writer{pdf: pdf, tr: newTextCleaner(pdf.UnicodeTranslatorFromDescriptor(""))}

	w.header(inv, issuer)
	w.issuerBlock(inv, issuer)
	w.parties(inv)
	w.tripDetails(inv)
	if inv.Kind == KindGoods {
		w.goodsDetails()
	}
	w.fareTable(inv)
	w.footer(issuer)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("pdf render failed: %w", err)
	}
	return buf.Bytes(), nil
}

// newTextCleaner wraps gofpdf's cp1252 translator for user-provided text
// (names, addresses): control characters and line breaks collapse to single
// spaces, and characters the core font can't draw (e.g. Devanagari) become
// "?" — gofpdf's own fallback is ".", which reads as real punctuation.
// PDF string escaping of \ ( ) is done by gofpdf itself on output.
func newTextCleaner(tr func(string) string) func(string) string {
	return func(s string) string {
		var b strings.Builder
		for _, r := range s {
			switch {
			case unicode.IsControl(r) || unicode.IsSpace(r):
				b.WriteRune(' ')
			case r >= 0x80 && tr(string(r)) == ".":
				b.WriteRune('?')
			default:
				b.WriteRune(r)
			}
		}
		return tr(strings.Join(strings.Fields(b.String()), " "))
	}
}

type writer struct {
	pdf *gofpdf.Fpdf
	tr  func(string) string
}

func (w *writer) header(inv *Invoice, issuer Issuer) {
	p := w.pdf
	p.SetFont("Helvetica", "B", 18)
	p.SetTextColor(brandOrangeR, brandOrangeG, brandOrangeB)
	p.CellFormat(0, 10, "bogie", "", 1, "L", false, 0, "")

	p.SetFont("Helvetica", "B", 13)
	p.SetTextColor(20, 20, 20)
	p.CellFormat(0, 8, Title(inv.Kind, issuer), "", 1, "L", false, 0, "")

	p.SetFont("Helvetica", "", 9)
	p.SetTextColor(90, 90, 90)
	p.CellFormat(0, 5, w.tr(fmt.Sprintf("%s: %s", NumberLabel(inv.Kind, issuer), inv.Number)), "", 1, "L", false, 0, "")
	p.CellFormat(0, 5, "Date: "+inv.IssuedAt.In(dateutil.ISTLocation).Format("02 Jan 2006"), "", 1, "L", false, 0, "")
	p.CellFormat(0, 5, w.tr("Booking Ref: "+inv.BookingID), "", 1, "L", false, 0, "")
	p.Ln(2)
	w.rule()
}

// issuerBlock prints the issuer. On a plain receipt (incomplete issuer)
// only the name/address that ARE configured are shown, and never tax
// identifiers — a receipt must not look like a tax document.
func (w *writer) issuerBlock(inv *Invoice, issuer Issuer) {
	complete := issuer.Complete()
	if strings.TrimSpace(issuer.LegalName) == "" && strings.TrimSpace(issuer.Address) == "" {
		return
	}
	w.sectionTitle("Issued By")
	w.plain(issuer.LegalName)
	w.plain(issuer.Address)
	if complete {
		w.kv("State", issuer.State)
		w.kv("GSTIN", issuer.GSTIN)
		w.kv("PAN", issuer.PAN)
		w.kv("CIN", issuer.CIN)
		sac := issuer.SACRide
		if inv.Kind == KindGoods {
			sac = issuer.SACGoods
		}
		w.kv("SAC", sac)
	}
	w.pdf.Ln(3)
}

func (w *writer) parties(inv *Invoice) {
	if inv.Kind == KindGoods {
		w.sectionTitle("Consignor")
		w.plain(inv.RiderName)
		w.pdf.Ln(2)
		w.sectionTitle("Consignee")
		w.kvDeclared("Name", inv.ConsigneeName)
		w.kvDeclared("Phone", inv.ConsigneePhone)
	} else {
		w.sectionTitle("Billed To")
		w.plain(inv.RiderName)
	}
	w.pdf.Ln(3)
}

func (w *writer) tripDetails(inv *Invoice) {
	if inv.Kind == KindGoods {
		w.sectionTitle("Consignment Details")
	} else {
		w.sectionTitle("Trip Details")
	}
	w.kv("Service", inv.ServiceName)
	w.kv("Vehicle", strings.TrimSpace(inv.VehicleType+" "+inv.VehicleNumber))
	w.kv("Driver", firstName(inv.DriverName))
	w.kv("Pickup", inv.PickupAddress)
	w.kv("Drop", inv.DropAddress)
	w.kv("Started", formatTime(inv.StartedAt))
	w.kv("Completed", formatTime(inv.CompletedAt))
	if inv.DistanceKm > 0 {
		w.kv("Distance", fmt.Sprintf("%.1f km", inv.DistanceKm))
	}
	w.kv("Payment", paymentLabel(inv.PaymentMethod))
	w.pdf.Ln(3)
}

// goodsDetails: none of these are collected at booking time yet.
func (w *writer) goodsDetails() {
	w.sectionTitle("Goods")
	w.kv("Goods type", notDeclared)
	w.kv("Weight", notDeclared)
	w.kv("Packages", notDeclared)
	w.kv("Declared value", notDeclared)
	w.pdf.Ln(3)
}

func (w *writer) fareTable(inv *Invoice) {
	p := w.pdf
	labelW, amountW := 140.0, 40.0

	p.SetFont("Helvetica", "B", 8)
	p.SetFillColor(brandOrangeR, brandOrangeG, brandOrangeB)
	p.SetTextColor(255, 255, 255)
	p.CellFormat(labelW, 7, "Description", "1", 0, "L", true, 0, "")
	p.CellFormat(amountW, 7, "Amount", "1", 1, "R", true, 0, "")

	p.SetFillColor(255, 255, 255)
	for _, row := range FareRows(inv) {
		if row.Bold {
			p.SetFont("Helvetica", "B", 10)
			p.SetTextColor(20, 20, 20)
		} else {
			p.SetFont("Helvetica", "", 9)
			p.SetTextColor(40, 40, 40)
		}
		p.CellFormat(labelW, 7, w.tr(row.Label), "1", 0, "L", true, 0, "")
		p.CellFormat(amountW, 7, formatRupees(row.Amount), "1", 1, "R", true, 0, "")
	}
}

func (w *writer) footer(issuer Issuer) {
	p := w.pdf
	p.Ln(10)
	w.rule()
	p.SetFont("Helvetica", "", 8)
	p.SetTextColor(140, 140, 140)
	p.CellFormat(0, 5, "Generated on "+time.Now().In(dateutil.ISTLocation).Format("2 Jan 2006, 3:04 PM"), "", 1, "L", false, 0, "")
	if s := strings.TrimSpace(issuer.SupportEmail); s != "" {
		p.CellFormat(0, 5, w.tr("Support: "+s), "", 1, "L", false, 0, "")
	}
}

// ── layout helpers ───────────────────────────────────────────────────────

func (w *writer) rule() {
	p := w.pdf
	p.SetDrawColor(230, 230, 230)
	p.SetLineWidth(0.3)
	y := p.GetY()
	p.Line(pageMarginMM, y, 210-pageMarginMM, y)
	p.Ln(4)
}

func (w *writer) sectionTitle(s string) {
	w.pdf.SetFont("Helvetica", "B", 10)
	w.pdf.SetTextColor(20, 20, 20)
	w.pdf.CellFormat(0, 6, s, "", 1, "L", false, 0, "")
}

// plain prints one user-provided value on its own line(s), wrapping if long.
func (w *writer) plain(s string) {
	if strings.TrimSpace(s) == "" {
		return
	}
	w.pdf.SetFont("Helvetica", "", 9)
	w.pdf.SetTextColor(60, 60, 60)
	w.pdf.MultiCell(0, 5, w.tr(s), "", "L", false)
}

// kv prints "Label   value", wrapping the value under itself (MultiCell)
// so long addresses never run off the page. Blank values are skipped.
func (w *writer) kv(label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	p := w.pdf
	p.SetFont("Helvetica", "", 9)
	p.SetTextColor(120, 120, 120)
	p.CellFormat(labelColMM, 5, label, "", 0, "L", false, 0, "")
	p.SetTextColor(40, 40, 40)
	p.MultiCell(0, 5, w.tr(value), "", "L", false)
}

// kvDeclared is kv, but a blank value prints "Not declared" instead of
// being skipped.
func (w *writer) kvDeclared(label, value string) {
	if strings.TrimSpace(value) == "" {
		value = notDeclared
	}
	w.kv(label, value)
}

func formatRupees(v float64) string {
	if v < 0 {
		return fmt.Sprintf("- Rs.%.2f", -v)
	}
	return fmt.Sprintf("Rs.%.2f", v)
}

func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.In(dateutil.ISTLocation).Format("02 Jan 2006, 3:04 PM")
}

func firstName(full string) string {
	if f := strings.Fields(full); len(f) > 0 {
		return f[0]
	}
	return ""
}

func paymentLabel(method string) string {
	switch method {
	case "wallet":
		return "bogie Wallet"
	case "company_wallet":
		return "Company Wallet"
	case "cash":
		return "Cash"
	}
	return method
}
