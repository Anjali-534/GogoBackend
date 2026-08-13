package handlers

// Bogie Tracker — shared HTML email chrome. Every tracker email (dispatch
// details, status changes, signup/approval lifecycle, plan invoices) wraps
// its content with trackerEmailWrapHTML so they all carry the same
// look — a centered card, the sending company's own logo/name as their
// branded sign-off, and a "Powered by Bogie Tracker" credit line underneath.
//
// Deliberately table-based with inline styles rather than a <style> block —
// standard transactional-email practice, since Gmail/Outlook/many mobile
// clients strip <style> tags but always honor inline style attributes.

import (
	"context"
	"fmt"
	"html"
	"strings"

	"github.com/deploykit/backend/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	trackerEmailBrandOrange = "#FF6B2B"
	trackerEmailBorderGray  = "#E5E7EB"
	// TrackerEmailTextGray and TrackerEmailMutedGray are exported (unlike the
	// two constants above) because the delivery-reminder mailer
	// (internal/services/trackerdelivery, a separate package) needs them to
	// build a body that visually matches the dispatch-details email.
	TrackerEmailTextGray  = "#374151"
	TrackerEmailMutedGray = "#9CA3AF"
)

// fetchTrackerCompanyLogoURL is a single cheap lookup shared by every email
// builder below — companyID is always already known (each send is for one
// specific company), so this is one extra indexed single-row read per send,
// never a join into a larger/more fragile query.
func fetchTrackerCompanyLogoURL(ctx context.Context, pool *pgxpool.Pool, companyID string) *string {
	var logoURL *string
	_ = pool.QueryRow(ctx, `SELECT logo_url FROM tracker_companies WHERE id = $1`, companyID).Scan(&logoURL)
	return logoURL
}

// trackerEmailFromName is the one shared FromName format for every tracker
// send — "<Company> via Bogie Tracker" — so recipients immediately see which
// company the email is on behalf of, and that it's routed through Bogie.
func trackerEmailFromName(companyName string) string {
	return companyName + " via Bogie Tracker"
}

// TrackerEmailFromName is the exported form of trackerEmailFromName, for
// trackerdelivery (a separate package) to reuse the exact same "<Company>
// via Bogie Tracker" sender-name format as every other tracker email.
func TrackerEmailFromName(companyName string) string {
	return trackerEmailFromName(companyName)
}

// TrackerEmailWrapHTML wraps bodyHTML in the shared card chrome + footer.
// bodyHTML is caller-built and trusted to already be safe/escaped — every
// helper in this file that interpolates dynamic values (company name, order
// fields, etc.) escapes them individually before they reach here. Exported
// for the same cross-package reason as TrackerEmailTextGray above.
func TrackerEmailWrapHTML(cfg *config.Config, companyName string, companyLogoURL *string, bodyHTML string) string {
	panelURL := strings.TrimRight(cfg.TrackerPanelURL, "/")
	var b strings.Builder
	b.WriteString(`<div style="background-color:#F3F4F6;padding:24px 12px;font-family:Arial,Helvetica,sans-serif;">`)
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:600px;margin:0 auto;background:#ffffff;border-radius:12px;border:1px solid ` + trackerEmailBorderGray + `;">`)
	b.WriteString(`<tr><td style="padding:28px 28px 8px 28px;">`)
	b.WriteString(bodyHTML)
	b.WriteString(`</td></tr>`)
	b.WriteString(`<tr><td style="padding:20px 28px 24px 28px;border-top:1px solid ` + trackerEmailBorderGray + `;">`)
	b.WriteString(trackerEmailFooterHTML(panelURL, companyName, companyLogoURL))
	b.WriteString(`</td></tr>`)
	b.WriteString(`</table>`)
	b.WriteString(`</div>`)
	return b.String()
}

// trackerEmailFooterHTML is the sending company's own branded sign-off
// (their logo if uploaded, else just their name) followed by Bogie's own
// muted "Powered by" credit line — every tracker email ends with both.
func trackerEmailFooterHTML(panelURL, companyName string, companyLogoURL *string) string {
	safeCompanyName := html.EscapeString(companyName)

	var companyBlock string
	if companyLogoURL != nil && *companyLogoURL != "" {
		companyBlock = fmt.Sprintf(
			`<img src="%s" width="32" height="32" alt="%s" style="width:32px;height:32px;border-radius:50%%;object-fit:cover;vertical-align:middle;margin-right:8px;border:1px solid %s;">`+
				`<span style="font-size:14px;font-weight:700;color:#111827;vertical-align:middle;">%s</span>`,
			html.EscapeString(*companyLogoURL), safeCompanyName, trackerEmailBorderGray, safeCompanyName,
		)
	} else {
		companyBlock = fmt.Sprintf(`<span style="font-size:14px;font-weight:700;color:#111827;">%s</span>`, safeCompanyName)
	}

	return fmt.Sprintf(
		`<div>%s</div>`+
			`<div style="margin-top:14px;padding-top:14px;border-top:1px solid #F3F4F6;">`+
			`<a href="%s" style="text-decoration:none;color:%s;font-size:11px;">`+
			`<img src="%s/logo.png" width="52" alt="Bogie" style="width:52px;height:auto;vertical-align:middle;margin-right:6px;opacity:0.8;">`+
			`Powered by Bogie Tracker`+
			`</a>`+
			`</div>`,
		companyBlock, panelURL, TrackerEmailMutedGray, panelURL,
	)
}

// TrackerEmailButtonHTML renders a bulletproof-style HTML button (inline
// styles, no background-image/CSS trickery that Outlook's Word rendering
// engine would drop) for the tracking/receipt/confirm links. Exported for
// the same cross-package reason as TrackerEmailTextGray above.
func TrackerEmailButtonHTML(href, label string) string {
	return fmt.Sprintf(
		`<a href="%s" style="display:inline-block;background-color:%s;color:#ffffff;text-decoration:none;font-weight:700;font-size:13px;padding:11px 20px;border-radius:8px;margin:4px 10px 4px 0;">%s</a>`,
		html.EscapeString(href), trackerEmailBrandOrange, html.EscapeString(label),
	)
}

// TrackerEmailDetailsTableHTML renders a [label, value] slice as a real
// bordered HTML table with a brand-orange header row, matching the classic
// paper dispatch sheet's SR/HEADS/DESCRIPTION layout that
// TrackerEmailDetailsTableText (the plain-text equivalent) mirrors. Exported
// for the same cross-package reason as TrackerEmailTextGray above.
func TrackerEmailDetailsTableHTML(rows [][2]string) string {
	var b strings.Builder
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="border-collapse:collapse;margin:14px 0 18px;">`)
	b.WriteString(`<tr>`)
	b.WriteString(`<th align="left" style="padding:8px 10px;font-size:11px;text-transform:uppercase;letter-spacing:0.05em;color:#B45309;background-color:#FFF3EC;border-bottom:2px solid ` + trackerEmailBrandOrange + `;">Field</th>`)
	b.WriteString(`<th align="left" style="padding:8px 10px;font-size:11px;text-transform:uppercase;letter-spacing:0.05em;color:#B45309;background-color:#FFF3EC;border-bottom:2px solid ` + trackerEmailBrandOrange + `;">Details</th>`)
	b.WriteString(`</tr>`)
	for i, row := range rows {
		bg := "#ffffff"
		if i%2 == 1 {
			bg = "#FAFAFA"
		}
		b.WriteString(fmt.Sprintf(
			`<tr style="background-color:%s;">`+
				`<td style="padding:8px 10px;font-size:13px;color:#6B7280;font-weight:600;border-bottom:1px solid #F3F4F6;white-space:nowrap;">%s</td>`+
				`<td style="padding:8px 10px;font-size:13px;color:#111827;border-bottom:1px solid #F3F4F6;">%s</td>`+
				`</tr>`,
			bg, html.EscapeString(row[0]), html.EscapeString(row[1]),
		))
	}
	b.WriteString(`</table>`)
	return b.String()
}

// TrackerEmailDetailsTableText is the plain-text counterpart of
// TrackerEmailDetailsTableHTML — same SR/HEADS/DESCRIPTION layout as the
// classic paper dispatch sheet. Shared by every plain-text tracker email
// that needs the table (dispatch details, delivery reminder) so they can
// never drift out of sync with each other.
func TrackerEmailDetailsTableText(rows [][2]string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-4s %-16s %s\n", "SR", "HEADS", "DESCRIPTION"))
	b.WriteString(strings.Repeat("-", 60) + "\n")
	for i, row := range rows {
		b.WriteString(fmt.Sprintf("%-4d %-16s %s\n", i+1, row[0], row[1]))
	}
	return b.String()
}

// trackerEmailFromPlainText is the low-effort-but-consistent HTML treatment
// for every tracker email that doesn't get a full custom layout (signup,
// OTP, approved, license, rejected, status-change, plan invoice, and the
// order-creation "Shipment Details" email) — it keeps using its existing
// plain-text body builder untouched (still sent as the Text fallback too),
// and just escapes + line-wraps that same content inside the shared
// card+footer chrome, so every tracker email at least gets consistent
// branding even without a bespoke redesign.
func trackerEmailFromPlainText(cfg *config.Config, companyName string, companyLogoURL *string, plainBody string) string {
	escaped := html.EscapeString(plainBody)
	bodyHTML := `<div style="font-size:14px;color:` + TrackerEmailTextGray + `;white-space:pre-wrap;line-height:1.6;">` + escaped + `</div>`
	return TrackerEmailWrapHTML(cfg, companyName, companyLogoURL, bodyHTML)
}

// trackerGSTStateCodes is the standard 2-digit GST state/UT code table —
// same official codes as the panel's client-side lib/gstin.ts, kept as an
// independent copy here since this package can't import frontend code.
// Used only to derive the sending company's own state for the "Bill From"
// row below (BookedFor*/Consignee* already have a stored *_state field
// straight from the order, no derivation needed for those).
var trackerGSTStateCodes = map[string]string{
	"01": "Jammu and Kashmir", "02": "Himachal Pradesh", "03": "Punjab", "04": "Chandigarh",
	"05": "Uttarakhand", "06": "Haryana", "07": "Delhi", "08": "Rajasthan", "09": "Uttar Pradesh",
	"10": "Bihar", "11": "Sikkim", "12": "Arunachal Pradesh", "13": "Nagaland", "14": "Manipur",
	"15": "Mizoram", "16": "Tripura", "17": "Meghalaya", "18": "Assam", "19": "West Bengal",
	"20": "Jharkhand", "21": "Odisha", "22": "Chhattisgarh", "23": "Madhya Pradesh", "24": "Gujarat",
	"25": "Daman and Diu", "26": "Dadra and Nagar Haveli and Daman and Diu", "27": "Maharashtra",
	"28": "Andhra Pradesh (Old)", "29": "Karnataka", "30": "Goa", "31": "Lakshadweep", "32": "Kerala",
	"33": "Tamil Nadu", "34": "Puducherry", "35": "Andaman and Nicobar Islands", "36": "Telangana",
	"37": "Andhra Pradesh", "38": "Ladakh", "97": "Other Territory", "99": "Centre Jurisdiction",
}

func trackerGSTState(gstin string) string {
	if len(gstin) < 2 {
		return ""
	}
	return trackerGSTStateCodes[gstin[:2]]
}

// trackerFullOrderEmailRows builds the comprehensive FIELD/DETAILS table
// shared by every tracker order-lifecycle stakeholder email (dispatched,
// in_transit, delivered, cancelled — see tracker_status_email.go). Every
// dispatch-sheet-relevant field captured on the order, not a per-status
// subset, so recipients see the same complete picture regardless of which
// stage triggered the email — replaces the old narrower, per-email row
// lists (trackerStatusChangeEmailRows/trackerStatusEmailRows) that used to
// vary what each status showed.
func trackerFullOrderEmailRows(o TrackerOrder, companyName, companyGSTIN string) [][2]string {
	rows := [][2]string{{"Company", companyName}}

	billFrom := companyName
	if companyGSTIN != "" {
		billFrom += " · GSTIN: " + companyGSTIN
		if state := trackerGSTState(companyGSTIN); state != "" {
			billFrom += " · " + state
		}
	}
	rows = append(rows, [2]string{"Bill From", billFrom})

	billTo := o.BookedForCompanyName
	if o.BookedForPhone != "" {
		billTo += " · " + o.BookedForPhone
	}
	if o.BookedForEmail != nil && *o.BookedForEmail != "" {
		billTo += " · " + *o.BookedForEmail
	}
	if o.BookedForGstin != nil && *o.BookedForGstin != "" {
		billTo += " · GSTIN: " + *o.BookedForGstin
	}
	if o.BookedForState != nil && *o.BookedForState != "" {
		billTo += " · " + *o.BookedForState
	}
	rows = append(rows, [2]string{"Bill To", billTo})

	rows = append(rows, [2]string{"Dispatch From", o.DispatchFrom})
	rows = append(rows, [2]string{"Ship To", o.DispatchTo})
	if o.RegisteredAddress != nil && *o.RegisteredAddress != "" {
		rows = append(rows, [2]string{"Registered Address", *o.RegisteredAddress})
	}
	if o.FactoryAddress != nil && *o.FactoryAddress != "" {
		rows = append(rows, [2]string{"Factory / Godown Address", *o.FactoryAddress})
	}

	if o.ConsigneeName != nil && *o.ConsigneeName != "" {
		consignee := *o.ConsigneeName
		if o.ConsigneeEmail != nil && *o.ConsigneeEmail != "" {
			consignee += " · " + *o.ConsigneeEmail
		}
		if o.ConsigneeGstin != nil && *o.ConsigneeGstin != "" {
			consignee += " · GSTIN: " + *o.ConsigneeGstin
		}
		if o.ConsigneeState != nil && *o.ConsigneeState != "" {
			consignee += " · " + *o.ConsigneeState
		}
		rows = append(rows, [2]string{"Consignee", consignee})
	}

	material := "—"
	if o.Material != nil && *o.Material != "" {
		material = *o.Material
	}
	rows = append(rows, [2]string{"Material Description", material})

	if o.Quantity != nil && *o.Quantity != "" {
		rows = append(rows, [2]string{"Quantity", *o.Quantity})
	}

	rows = append(rows, [2]string{"Vehicle Number", o.VehicleNumber})

	if o.DriverName != "" {
		driver := o.DriverName
		if o.DriverPhone != "" {
			driver += " (" + o.DriverPhone + ")"
		}
		rows = append(rows, [2]string{"Driver", driver})
	}

	if o.TransporterName != "" {
		transporter := o.TransporterName
		if o.TransporterPhone != "" {
			transporter += " (" + o.TransporterPhone + ")"
		}
		rows = append(rows, [2]string{"Transporter", transporter})
	}

	if o.EwayBillNumber != "" {
		rows = append(rows, [2]string{"E-way Bill Number", o.EwayBillNumber})
	}
	if o.DocumentsEnclosed != nil && *o.DocumentsEnclosed != "" {
		rows = append(rows, [2]string{"Documents Enclosed", *o.DocumentsEnclosed})
	}

	priorityLabel, ok := map[string]string{"normal": "Normal", "urgent": "Urgent", "same_day": "Same-day"}[o.Priority]
	if !ok {
		priorityLabel = "Normal"
	}
	rows = append(rows, [2]string{"Priority", priorityLabel})

	if o.ExpectedDeliveryDate != nil {
		rows = append(rows, [2]string{"Expected Delivery Date", o.ExpectedDeliveryDate.Format("02 Jan 2006")})
	}

	contact := "—"
	if o.ContactPersonName != nil && *o.ContactPersonName != "" {
		contact = *o.ContactPersonName
		if o.ContactPersonDesignation != nil && *o.ContactPersonDesignation != "" {
			contact += " (" + *o.ContactPersonDesignation + ")"
		}
		if o.ContactPersonPhone != nil && *o.ContactPersonPhone != "" {
			contact += " · " + *o.ContactPersonPhone
		}
		if o.ContactPersonEmail != nil && *o.ContactPersonEmail != "" {
			contact += " · " + *o.ContactPersonEmail
		}
	}
	rows = append(rows, [2]string{"Contact Person", contact})

	if o.DeclaredValue != nil {
		rows = append(rows, [2]string{"Declared Value", fmt.Sprintf("₹%.2f", *o.DeclaredValue)})
	}
	if len(o.SpecialHandling) > 0 {
		rows = append(rows, [2]string{"Special Handling", strings.Join(o.SpecialHandling, ", ")})
	}
	if o.InternalReference != nil && *o.InternalReference != "" {
		rows = append(rows, [2]string{"Internal Reference", *o.InternalReference})
	}

	return rows
}
