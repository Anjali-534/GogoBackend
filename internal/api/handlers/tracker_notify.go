package handlers

// Bogie Tracker — dispatch notification emails. A company sends the
// traditional dispatch-details summary to whichever order stakeholders have
// an email on file, straight from the order detail page. Synchronous (not
// the usual fire-and-forget goroutine pattern in tracker_mail.go) because
// the caller needs a per-recipient sent/skipped/failed result back in the
// response — at most 3 recipients, so a few hundred ms of Resend latency is
// an acceptable blocking cost here.

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
	"github.com/deploykit/backend/internal/mail"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var validNotifyRecipients = map[string]bool{
	"booked_for":  true,
	"consignee":   true,
	"transporter": true,
	"driver":      true,
}

type notifyResult struct {
	Recipient string `json:"recipient"`
	Email     string `json:"email,omitempty"`
	Status    string `json:"status"` // sent | skipped | failed
	Reason    string `json:"reason,omitempty"`
}

// POST /gogoo/tracker/orders/:id/notify
func NotifyTrackerOrderStakeholders(c *gin.Context) {
	companyID := c.GetString("company_id")
	orderID := c.Param("id")
	var req struct {
		Recipients []string `json:"recipients" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	for _, r := range req.Recipients {
		if !validNotifyRecipients[r] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid recipient: " + r})
			return
		}
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var o TrackerOrder
	var companyName, contactEmail string
	var notificationEmail *string
	var companyLogoURL *string
	err := pool.QueryRow(ctx, `
		SELECT o.booked_for_company_name, o.booked_for_phone, o.booked_for_email,
		       o.dispatch_from, o.dispatch_to,
		       COALESCE(o.transporter_name,''), o.transporter_email,
		       COALESCE(o.driver_name,''), COALESCE(o.driver_phone,''),
		       o.vehicle_number, COALESCE(o.eway_bill_file_url,''), o.status, o.public_tracking_token,
		       o.consignee_name, o.consignee_email, o.material, o.quantity,
		       o.dispatch_datetime, o.documents_enclosed, o.received_confirmation_token,
		       o.booked_for_state, o.consignee_state, o.order_type,
		       c.company_name, c.contact_email, c.notification_email, c.logo_url
		FROM tracker_orders o
		JOIN tracker_companies c ON c.id = o.company_id
		WHERE o.id = $1 AND o.company_id = $2
	`, orderID, companyID).Scan(
		&o.BookedForCompanyName, &o.BookedForPhone, &o.BookedForEmail,
		&o.DispatchFrom, &o.DispatchTo,
		&o.TransporterName, &o.TransporterEmail,
		&o.DriverName, &o.DriverPhone,
		&o.VehicleNumber, &o.EwayBillFileURL, &o.Status, &o.PublicTrackingToken,
		&o.ConsigneeName, &o.ConsigneeEmail, &o.Material, &o.Quantity,
		&o.DispatchDatetime, &o.DocumentsEnclosed, &o.ReceivedConfirmationToken,
		&o.BookedForState, &o.ConsigneeState, &o.OrderType,
		&companyName, &contactEmail, &notificationEmail, &companyLogoURL,
	)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	docs, err := fetchTrackerOrderDocuments(ctx, pool, orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	var attachables []trackerEmailAttachable
	if o.EwayBillFileURL != "" {
		attachables = append(attachables, trackerEmailAttachable{Label: "E-Way Bill", FileURL: o.EwayBillFileURL})
	}
	for _, d := range docs {
		attachables = append(attachables, trackerEmailAttachable{Label: trackerDocDisplayLabel(d), FileURL: d.FileURL})
	}
	attachments, skippedDocs := buildTrackerEmailAttachments(attachables)

	cfg := c.MustGet("config").(*config.Config)
	trackingLink := strings.TrimRight(cfg.TrackerPanelURL, "/") + "/track/" + o.PublicTrackingToken

	// The receipt token is generated up front at order creation for every
	// order created after this feature shipped (see CreateTrackerCompanyOrder)
	// — but orders created before it predate the column and still have NULL
	// here. Lazy-backfill on first notify send so old orders get a working
	// receipt link too, same self-healing pattern as cacheTrackerOrderRoute.
	if o.ReceivedConfirmationToken == nil {
		token, err := generateTrackingToken()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate receipt token"})
			return
		}
		if _, err := pool.Exec(ctx, `
			UPDATE tracker_orders SET received_confirmation_token = $1, updated_at = NOW() WHERE id = $2
		`, token, orderID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to backfill receipt token"})
			return
		}
		o.ReceivedConfirmationToken = &token
	}
	receiptLink := strings.TrimRight(cfg.TrackerPanelURL, "/") + "/receipt/" + *o.ReceivedConfirmationToken

	subject := fmt.Sprintf("Dispatch Details — %s (Truck %s)", o.BookedForCompanyName, o.VehicleNumber)
	// Only consignee/booked_for get the receipt-confirmation line — the
	// transporter isn't the one confirming goods were received.
	//
	// omitTrackingLink: on an inbound order, BookedFor* is repurposed as the
	// Supplier (approved field reuse, migration 050) and the "shipment" is
	// really the company's own inbound pickup — a public live-tracking link
	// isn't meaningful to send them. Rather than splitting into a separate
	// send path for inbound orders, buildDispatchEmailBody takes a shared
	// flag and omits just the one line.
	omitTrackingLink := o.OrderType == "inbound"
	bodyWithReceipt := buildDispatchEmailBody(o, trackingLink, receiptLink, omitTrackingLink, skippedDocs)
	bodyWithoutReceipt := buildDispatchEmailBody(o, trackingLink, "", omitTrackingLink, skippedDocs)
	htmlWithReceipt := buildDispatchEmailBodyHTML(cfg, o, companyName, companyLogoURL, trackingLink, receiptLink, omitTrackingLink)
	htmlWithoutReceipt := buildDispatchEmailBodyHTML(cfg, o, companyName, companyLogoURL, trackingLink, "", omitTrackingLink)

	// Reply-To is the company's own address — never the client's domain,
	// which would fail SPF/DKIM if we tried to send "from" it.
	replyTo := contactEmail
	if notificationEmail != nil && *notificationEmail != "" {
		replyTo = *notificationEmail
	}
	fromName := trackerEmailFromName(companyName)

	var results []notifyResult
	var sentTo []string

	for _, r := range req.Recipients {
		if r == "driver" {
			// Drivers have no email field by design — they already have the
			// tracking link via WhatsApp, so this is always a no-op skip.
			results = append(results, notifyResult{Recipient: r, Status: "skipped", Reason: "no email on file"})
			continue
		}

		var name, email string
		body, htmlBody := bodyWithoutReceipt, htmlWithoutReceipt
		switch r {
		case "booked_for":
			name = o.BookedForCompanyName
			if o.BookedForEmail != nil {
				email = *o.BookedForEmail
			}
			body, htmlBody = bodyWithReceipt, htmlWithReceipt
		case "consignee":
			if o.ConsigneeName != nil {
				name = *o.ConsigneeName
			}
			if o.ConsigneeEmail != nil {
				email = *o.ConsigneeEmail
			}
			body, htmlBody = bodyWithReceipt, htmlWithReceipt
		case "transporter":
			name = o.TransporterName
			if o.TransporterEmail != nil {
				email = *o.TransporterEmail
			}
		}

		if email == "" {
			results = append(results, notifyResult{Recipient: r, Status: "skipped", Reason: "no email on file"})
			continue
		}

		if err := mail.Send(cfg, mail.Message{
			To:          email,
			Subject:     subject,
			Body:        body,
			HTMLBody:    htmlBody,
			Attachments: attachments,
			FromName:    fromName,
			ReplyTo:     replyTo,
		}); err != nil {
			results = append(results, notifyResult{Recipient: r, Email: email, Status: "failed", Reason: err.Error()})
			continue
		}

		results = append(results, notifyResult{Recipient: r, Email: email, Status: "sent"})
		label := name
		if label == "" {
			label = r
		}
		sentTo = append(sentTo, label)
	}

	if len(sentTo) > 0 {
		pool.Exec(ctx, `
			INSERT INTO tracker_order_events (id, order_id, status, note)
			VALUES ($1,$2,$3,$4)
		`, uuid.New(), orderID, o.Status, "Dispatch details emailed to "+strings.Join(sentTo, ", "))
	}

	c.JSON(http.StatusOK, gin.H{"results": results})
}

// DispatchEmailRows builds the shared [label, value] field list used by
// every dispatch-shaped tracker email (dispatch details, delivery
// reminder) — single source of truth so they never drift out of sync with
// each other. Exported so trackerdelivery (a separate package) can reuse it
// for the delivery-reminder email instead of duplicating the field list.
func DispatchEmailRows(o TrackerOrder) [][2]string {
	bookedForState := "—"
	if o.BookedForState != nil && *o.BookedForState != "" {
		bookedForState = *o.BookedForState
	}
	consignee := "—"
	if o.ConsigneeName != nil && *o.ConsigneeName != "" {
		consignee = *o.ConsigneeName
	}
	consigneeState := "—"
	if o.ConsigneeState != nil && *o.ConsigneeState != "" {
		consigneeState = *o.ConsigneeState
	}
	material := "—"
	if o.Material != nil && *o.Material != "" {
		material = *o.Material
	}
	quantity := "—"
	if o.Quantity != nil && *o.Quantity != "" {
		quantity = *o.Quantity
	}
	transporter := "—"
	if o.TransporterName != "" {
		transporter = o.TransporterName
	}
	driver := "—"
	if o.DriverName != "" {
		driver = o.DriverName
		if o.DriverPhone != "" {
			driver += " (" + o.DriverPhone + ")"
		}
	}
	dateTime := "—"
	if o.DispatchDatetime != nil {
		dateTime = o.DispatchDatetime.Format("02 Jan 2006, 3:04 PM")
	}
	docs := "—"
	if o.DocumentsEnclosed != nil && *o.DocumentsEnclosed != "" {
		docs = *o.DocumentsEnclosed
	}

	return [][2]string{
		{"Party Name", o.BookedForCompanyName},
		{"Party State", bookedForState},
		{"Consignee", consignee},
		{"Consignee State", consigneeState},
		{"Route", o.DispatchFrom + " -> " + o.DispatchTo},
		{"Material", material},
		{"Quantity", quantity},
		{"Truck No.", o.VehicleNumber},
		{"Driver", driver},
		{"Transporter", transporter},
		{"Date & Time", dateTime},
		{"Copy Enclosed", docs},
	}
}

// buildDispatchEmailBody mirrors the traditional paper dispatch sheet — a
// SR NO / HEADS / DESCRIPTION table — as plain text. Sent as the Text
// fallback alongside buildDispatchEmailBodyHTML's rendered version (see
// NotifyTrackerOrderStakeholders). receiptLink is only passed for the
// consignee/booked_for recipients — empty omits the "confirm receipt" line
// entirely (transporter emails never get it). omitTrackingLink drops the
// "Track this shipment live" line — set for inbound orders, where
// BookedFor* is the Supplier rather than a party tracking an outbound
// delivery (see NotifyTrackerOrderStakeholders). skippedDocs lists the
// labels of any documents that didn't make it into the actual email
// attachments (fetch failure or over the size budget — see
// buildTrackerEmailAttachments), so the "Copy Enclosed" claim doesn't go
// silently unfulfilled.
func buildDispatchEmailBody(o TrackerOrder, trackingLink, receiptLink string, omitTrackingLink bool, skippedDocs []string) string {
	rows := DispatchEmailRows(o)

	var b strings.Builder
	b.WriteString("DISPATCH DETAILS\n")
	b.WriteString("=================\n\n")
	b.WriteString(TrackerEmailDetailsTableText(rows))
	b.WriteString("\n")
	if !omitTrackingLink {
		b.WriteString(fmt.Sprintf("Track this shipment live: %s\n\n", trackingLink))
	}
	if receiptLink != "" {
		b.WriteString(fmt.Sprintf("Once your goods arrive, confirm receipt here: %s\n\n", receiptLink))
	}
	if len(skippedDocs) > 0 {
		pronoun := "them"
		if len(skippedDocs) == 1 {
			pronoun = "it"
		}
		b.WriteString(fmt.Sprintf(
			"Note: %s exceeded email size limits and could not be attached. You can view and download %s from the order's tracking page.\n\n",
			strings.Join(skippedDocs, ", "), pronoun,
		))
	}
	b.WriteString("This is an automated dispatch notification sent via Bogie Tracker.\n")

	return b.String()
}

// buildDispatchEmailBodyHTML is the rendered-HTML counterpart of
// buildDispatchEmailBody — same fields (via the shared dispatchEmailRows),
// same per-recipient trackingLink/receiptLink/omitTrackingLink rules, but as
// a real bordered table with styled tracking/receipt buttons, wrapped in the
// shared card+footer chrome (company's own branding + "Powered by Bogie
// Tracker").
func buildDispatchEmailBodyHTML(
	cfg *config.Config, o TrackerOrder, companyName string, companyLogoURL *string,
	trackingLink, receiptLink string, omitTrackingLink bool,
) string {
	rows := DispatchEmailRows(o)

	var b strings.Builder
	b.WriteString(`<p style="font-size:14px;color:#111827;margin:0 0 4px;">Dear Sir,</p>`)
	b.WriteString(`<p style="font-size:14px;color:` + TrackerEmailTextGray + `;margin:0 0 4px;">Please find the dispatch details for your shipment below.</p>`)
	b.WriteString(TrackerEmailDetailsTableHTML(rows))

	if !omitTrackingLink || receiptLink != "" {
		b.WriteString(`<div style="margin:4px 0 8px;">`)
		if !omitTrackingLink {
			b.WriteString(TrackerEmailButtonHTML(trackingLink, "Track Shipment Live"))
		}
		if receiptLink != "" {
			b.WriteString(TrackerEmailButtonHTML(receiptLink, "Confirm Receipt"))
		}
		b.WriteString(`</div>`)
	}

	b.WriteString(`<p style="font-size:12px;color:` + TrackerEmailMutedGray + `;margin-top:18px;">This is an automated dispatch notification sent via Bogie Tracker.</p>`)

	return TrackerEmailWrapHTML(cfg, companyName, companyLogoURL, b.String())
}
