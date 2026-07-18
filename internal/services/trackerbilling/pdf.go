package trackerbilling

import (
	"bytes"
	"fmt"
	"time"

	"github.com/jung-kurt/gofpdf"
)

const (
	brandOrangeR, brandOrangeG, brandOrangeB = 255, 107, 43 // #FF6B2B
	pageMarginMM                             = 15
)

// Invoice is everything GenerateInvoicePDF needs to render a tax invoice for
// a paid tracker_plan_orders row. Regenerated on demand from the order's own
// columns rather than persisted as a blob — see the invoice-download handler.
type Invoice struct {
	InvoiceNumber string
	IssuedAt      time.Time
	OrderID       string

	Plan            string
	BillingDuration string

	BaseAmount  float64
	GSTAmount   float64
	TotalAmount float64

	BillingName        string
	BillingAddressLine string
	BillingCity        string
	BillingState       string
	BillingPincode     string
	GSTIN              string
}

var planLabels = map[string]string{
	"single":   "Single User",
	"2users":   "2 Users",
	"5users":   "5 Users",
	"mega":     "Mega",
	"lifetime": "Lifetime",
}

var durationLabels = map[string]string{
	"monthly":    "Monthly",
	"quarterly":  "Quarterly",
	"halfYearly": "Half-Yearly",
	"yearly":     "Yearly",
	"onetime":    "One-time",
}

// PlanLabel returns the human-readable name for a plan code, falling back to
// the raw code for anything unrecognized.
func PlanLabel(plan string) string {
	if l, ok := planLabels[plan]; ok {
		return l
	}
	return plan
}

func durationLabel(d string) string {
	if l, ok := durationLabels[d]; ok {
		return l
	}
	return d
}

// GenerateInvoicePDF renders a one-page GST tax invoice: header, bill-to
// box, a single line-item row for the plan/duration, and a totals summary —
// same visual language as ledger.GeneratePDF (brand-orange header, orange
// table header row) for a consistent look across bogie's PDFs.
func GenerateInvoicePDF(inv *Invoice) ([]byte, error) {
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(pageMarginMM, pageMarginMM, pageMarginMM)
	pdf.SetAutoPageBreak(true, 20)
	pdf.AddPage()

	// The core Helvetica font only understands WinAnsi (cp1252), not raw
	// UTF-8 — without this translator, non-ASCII characters like an em-dash
	// come out as mojibake (same fix as ledger.GeneratePDF).
	tr := pdf.UnicodeTranslatorFromDescriptor("")

	drawInvoiceHeader(pdf, tr, inv)
	drawBillTo(pdf, tr, inv)
	drawLineItems(pdf, tr, inv)
	drawInvoiceFooter(pdf, tr)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("pdf render failed: %w", err)
	}
	return buf.Bytes(), nil
}

func drawInvoiceHeader(pdf *gofpdf.Fpdf, tr func(string) string, inv *Invoice) {
	pdf.SetFont("Helvetica", "B", 18)
	pdf.SetTextColor(brandOrangeR, brandOrangeG, brandOrangeB)
	pdf.CellFormat(0, 10, "bogie", "", 1, "L", false, 0, "")

	pdf.SetFont("Helvetica", "B", 13)
	pdf.SetTextColor(20, 20, 20)
	pdf.CellFormat(0, 8, tr("Tax Invoice"), "", 1, "L", false, 0, "")

	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(90, 90, 90)
	pdf.CellFormat(0, 5, tr(fmt.Sprintf("Invoice No: %s", inv.InvoiceNumber)), "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 5, tr(fmt.Sprintf("Date: %s", inv.IssuedAt.Format("02 Jan 2006"))), "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 5, tr(fmt.Sprintf("Order Ref: %s", inv.OrderID)), "", 1, "L", false, 0, "")
	pdf.Ln(2)

	pdf.SetDrawColor(230, 230, 230)
	pdf.SetLineWidth(0.3)
	y := pdf.GetY()
	pdf.Line(pageMarginMM, y, 210-pageMarginMM, y)
	pdf.Ln(4)
}

func drawBillTo(pdf *gofpdf.Fpdf, tr func(string) string, inv *Invoice) {
	pdf.SetFont("Helvetica", "B", 10)
	pdf.SetTextColor(20, 20, 20)
	pdf.CellFormat(0, 6, tr("Billed To"), "", 1, "L", false, 0, "")

	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(60, 60, 60)
	pdf.CellFormat(0, 5, tr(inv.BillingName), "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 5, tr(inv.BillingAddressLine), "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 5, tr(fmt.Sprintf("%s, %s - %s", inv.BillingCity, inv.BillingState, inv.BillingPincode)), "", 1, "L", false, 0, "")
	if inv.GSTIN != "" {
		pdf.CellFormat(0, 5, tr(fmt.Sprintf("GSTIN: %s", inv.GSTIN)), "", 1, "L", false, 0, "")
	}
	pdf.Ln(4)
}

func drawLineItems(pdf *gofpdf.Fpdf, tr func(string) string, inv *Invoice) {
	widths := []float64{95, 30, 32.5, 32.5}
	headers := []string{"Description", "Duration", "Amount", fmt.Sprintf("GST (%.0f%%)", GSTRate*100)}

	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetFillColor(brandOrangeR, brandOrangeG, brandOrangeB)
	pdf.SetTextColor(255, 255, 255)
	for i, h := range headers {
		align := "L"
		if i >= 2 {
			align = "R"
		}
		pdf.CellFormat(widths[i], 7, h, "1", 0, align, true, 0, "")
	}
	pdf.Ln(-1)

	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(40, 40, 40)
	pdf.SetFillColor(255, 255, 255)
	desc := fmt.Sprintf("Bogie Tracker - %s Plan", PlanLabel(inv.Plan))
	pdf.CellFormat(widths[0], 8, tr(desc), "1", 0, "L", true, 0, "")
	pdf.CellFormat(widths[1], 8, tr(durationLabel(inv.BillingDuration)), "1", 0, "L", true, 0, "")
	pdf.CellFormat(widths[2], 8, fmt.Sprintf("Rs.%.2f", inv.BaseAmount), "1", 0, "R", true, 0, "")
	pdf.CellFormat(widths[3], 8, fmt.Sprintf("Rs.%.2f", inv.GSTAmount), "1", 0, "R", true, 0, "")
	pdf.Ln(-1)
	pdf.Ln(6)

	labelW := widths[0] + widths[1] + widths[2]
	pdf.SetFont("Helvetica", "B", 11)
	pdf.SetTextColor(20, 20, 20)
	pdf.CellFormat(labelW, 8, tr("Total Paid"), "", 0, "R", false, 0, "")
	pdf.CellFormat(widths[3], 8, fmt.Sprintf("Rs.%.2f", inv.TotalAmount), "", 1, "R", false, 0, "")
}

func drawInvoiceFooter(pdf *gofpdf.Fpdf, tr func(string) string) {
	pdf.Ln(10)
	pdf.SetDrawColor(230, 230, 230)
	y := pdf.GetY()
	pdf.Line(pageMarginMM, y, 210-pageMarginMM, y)
	pdf.Ln(3)

	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(140, 140, 140)
	pdf.CellFormat(0, 5, fmt.Sprintf("Generated on %s", time.Now().Format("2 Jan 2006, 3:04 PM")), "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 5, tr("Aggarwal Publicity and Marketing Pvt. Ltd. — bogie"), "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 5, "Support: support@bogie.in", "", 1, "L", false, 0, "")
}
