package ledger

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

// GeneratePDF renders a Statement as a bank-statement-style PDF: header with
// driver identity + period, a summary box (opening/earned/deducted/closing),
// a transaction table with a running balance column, and a footer.
func GeneratePDF(stmt *Statement) ([]byte, error) {
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(pageMarginMM, pageMarginMM, pageMarginMM)
	pdf.SetAutoPageBreak(true, 20)
	pdf.AddPage()

	// The core Helvetica font only understands WinAnsi (cp1252), not raw
	// UTF-8 — without this translator, non-ASCII characters like an em-dash
	// come out as mojibake (verified: "—" rendered as "â€"" before this fix).
	tr := pdf.UnicodeTranslatorFromDescriptor("")

	drawHeader(pdf, tr, stmt)
	drawSummaryBox(pdf, tr, stmt)
	drawTransactionTable(pdf, tr, stmt)
	drawFooter(pdf, tr)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("pdf render failed: %w", err)
	}
	return buf.Bytes(), nil
}

func drawHeader(pdf *gofpdf.Fpdf, tr func(string) string, stmt *Statement) {
	pdf.SetFont("Helvetica", "B", 18)
	pdf.SetTextColor(brandOrangeR, brandOrangeG, brandOrangeB)
	pdf.CellFormat(0, 10, "gogoo", "", 1, "L", false, 0, "")

	pdf.SetFont("Helvetica", "B", 13)
	pdf.SetTextColor(20, 20, 20)
	pdf.CellFormat(0, 8, tr("Driver Earnings Statement"), "", 1, "L", false, 0, "")

	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(90, 90, 90)
	pdf.CellFormat(0, 6, tr(fmt.Sprintf("Statement period: %s", stmt.PeriodLabel)), "", 1, "L", false, 0, "")
	pdf.Ln(2)

	pdf.SetDrawColor(230, 230, 230)
	pdf.SetLineWidth(0.3)
	y := pdf.GetY()
	pdf.Line(pageMarginMM, y, 210-pageMarginMM, y)
	pdf.Ln(4)

	pdf.SetFont("Helvetica", "B", 10)
	pdf.SetTextColor(20, 20, 20)
	pdf.CellFormat(0, 6, tr(stmt.DriverName), "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(90, 90, 90)
	pdf.CellFormat(0, 5, tr(fmt.Sprintf("Driver ID: %s", stmt.DriverID)), "", 1, "L", false, 0, "")
	if stmt.DriverPhone != "" {
		pdf.CellFormat(0, 5, tr(fmt.Sprintf("Phone: %s", stmt.DriverPhone)), "", 1, "L", false, 0, "")
	}
	pdf.Ln(4)
}

func drawSummaryBox(pdf *gofpdf.Fpdf, tr func(string) string, stmt *Statement) {
	boxY := pdf.GetY()
	boxH := 22.0
	pdf.SetFillColor(250, 250, 250)
	pdf.SetDrawColor(230, 230, 230)
	pdf.Rect(pageMarginMM, boxY, 210-2*pageMarginMM, boxH, "FD")

	colW := (210.0 - 2*pageMarginMM) / 4.0
	cols := []struct {
		label string
		value float64
		color [3]int
	}{
		{"Opening Balance", stmt.OpeningBalance, [3]int{20, 20, 20}},
		{"Total Earned", stmt.TotalCredit, [3]int{16, 163, 74}},
		{"Total Deducted", stmt.TotalDebit, [3]int{220, 38, 38}},
		{"Closing Balance", stmt.ClosingBalance, [3]int{20, 20, 20}},
	}
	for i, c := range cols {
		x := pageMarginMM + float64(i)*colW
		pdf.SetXY(x, boxY+4)
		pdf.SetFont("Helvetica", "", 8)
		pdf.SetTextColor(120, 120, 120)
		pdf.CellFormat(colW, 5, c.label, "", 2, "C", false, 0, "")
		pdf.SetX(x)
		pdf.SetFont("Helvetica", "B", 12)
		pdf.SetTextColor(c.color[0], c.color[1], c.color[2])
		sign := ""
		if i == 1 {
			sign = "+"
		} else if i == 2 {
			sign = "-"
		}
		pdf.CellFormat(colW, 8, fmt.Sprintf("%s Rs.%.0f", sign, c.value), "", 2, "C", false, 0, "")
	}
	pdf.SetXY(pageMarginMM, boxY+boxH+6)
}

func drawTransactionTable(pdf *gofpdf.Fpdf, tr func(string) string, stmt *Statement) {
	pdf.SetFont("Helvetica", "B", 11)
	pdf.SetTextColor(20, 20, 20)
	pdf.CellFormat(0, 7, "Transactions", "", 1, "L", false, 0, "")
	pdf.Ln(1)

	usableWidth := 210.0 - 2*pageMarginMM
	widths := []float64{22, 68, 24, 24, 24, 28} // date, description, type, credit, debit, running
	_ = usableWidth
	headers := []string{"Date", "Description", "Type", "Credit", "Debit", "Balance"}

	drawTableHeader := func() {
		pdf.SetFont("Helvetica", "B", 8)
		pdf.SetFillColor(brandOrangeR, brandOrangeG, brandOrangeB)
		pdf.SetTextColor(255, 255, 255)
		for i, h := range headers {
			align := "L"
			if i >= 3 {
				align = "R"
			}
			pdf.CellFormat(widths[i], 7, h, "1", 0, align, true, 0, "")
		}
		pdf.Ln(-1)
	}
	drawTableHeader()

	pdf.SetFont("Helvetica", "", 8)
	if len(stmt.Entries) == 0 {
		pdf.SetTextColor(120, 120, 120)
		pdf.CellFormat(sum(widths), 8, "No transactions this period.", "1", 1, "C", false, 0, "")
		return
	}

	rowN := 0
	for _, e := range stmt.Entries {
		if pdf.GetY() > 270 {
			pdf.AddPage()
			drawTableHeader()
			pdf.SetFont("Helvetica", "", 8)
		}
		if rowN%2 == 1 {
			pdf.SetFillColor(248, 248, 248)
		} else {
			pdf.SetFillColor(255, 255, 255)
		}
		pdf.SetTextColor(40, 40, 40)

		credit, debit := "", ""
		if e.IsDebit {
			debit = fmt.Sprintf("%.0f", e.Amount)
		} else {
			credit = fmt.Sprintf("%.0f", e.Amount)
		}

		pdf.CellFormat(widths[0], 7, e.Date.Format("02 Jan"), "1", 0, "L", true, 0, "")
		pdf.CellFormat(widths[1], 7, tr(truncate(e.Description, 42)), "1", 0, "L", true, 0, "")
		pdf.CellFormat(widths[2], 7, e.Type, "1", 0, "L", true, 0, "")
		pdf.SetTextColor(16, 163, 74)
		pdf.CellFormat(widths[3], 7, credit, "1", 0, "R", true, 0, "")
		pdf.SetTextColor(220, 38, 38)
		pdf.CellFormat(widths[4], 7, debit, "1", 0, "R", true, 0, "")
		pdf.SetTextColor(40, 40, 40)
		pdf.CellFormat(widths[5], 7, fmt.Sprintf("%.0f", e.Running), "1", 0, "R", true, 0, "")
		pdf.Ln(-1)
		rowN++
	}
}

func drawFooter(pdf *gofpdf.Fpdf, tr func(string) string) {
	pdf.Ln(6)
	pdf.SetDrawColor(230, 230, 230)
	y := pdf.GetY()
	pdf.Line(pageMarginMM, y, 210-pageMarginMM, y)
	pdf.Ln(3)

	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(140, 140, 140)
	pdf.CellFormat(0, 5, fmt.Sprintf("Generated on %s", time.Now().Format("2 Jan 2006, 3:04 PM")), "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 5, tr("Aggarwal Publicity and Marketing Pvt. Ltd. — gogoo"), "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 5, "Support: support@gogoo.in", "", 1, "L", false, 0, "")
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func sum(vals []float64) float64 {
	total := 0.0
	for _, v := range vals {
		total += v
	}
	return total
}
