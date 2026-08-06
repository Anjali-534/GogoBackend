package handlers

// Bogie Tracker — status-change emails (Phase 4). Fired from
// UpdateTrackerCompanyOrderStatus after its transaction commits, same
// recipients as the Phase 3 creation email (Booked For + Contact Person +
// CC/BCC via fetchTrackerOrderCCEmails).
//
// Only customer-meaningful transitions email: dispatched, in_transit,
// delivered, cancelled. 'loading'/'loaded' are internal pre-dispatch
// warehouse ticks — not something a receiving party needs an email about —
// so they're deliberately excluded from trackerStatusEmailCopy below; a
// status not present in that map is silently skipped, no email sent.
//
// Unlike the creation email, these are explicitly do-not-reply: ReplyTo
// points at a dedicated no-reply address instead of defaulting to
// cfg.ResendFromEmail, and the body says so — a status ping isn't something
// the company is set up to receive replies to.

import (
	"context"
	"fmt"
	"html"
	"log"
	"strings"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
	"github.com/deploykit/backend/internal/mail"
)

const trackerNoReplyAddress = "no-reply@bogie.in"

type trackerStatusEmailCopyEntry struct {
	Headline string // e.g. "Your Shipment Has Been Dispatched"
}

// trackerStatusEmailCopy is also the trigger allowlist — a status not
// present here never sends an email at all.
var trackerStatusEmailCopy = map[string]trackerStatusEmailCopyEntry{
	"dispatched": {Headline: "Your Shipment Has Been Dispatched"},
	"in_transit": {Headline: "Your Shipment Is In Transit"},
	"delivered":  {Headline: "Your Shipment Has Been Delivered"},
	"cancelled":  {Headline: "Shipment Cancelled"},
}

// maybeSendTrackerOrderStatusEmail is called by UpdateTrackerCompanyOrderStatus
// right after its transaction commits. Does its own (fast, synchronous) DB
// fetch for the fields the email needs, then hands off to a goroutine for
// the network-bound Cloudinary/Resend calls — same split as
// SendTrackerOrderCreationEmail. A status outside trackerStatusEmailCopy is
// a silent no-op, not an error.
func maybeSendTrackerOrderStatusEmail(cfg *config.Config, companyID, orderID, status string) {
	emailCopy, ok := trackerStatusEmailCopy[status]
	if !ok {
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var o TrackerOrder
	var companyName string
	var companyLogoURL *string
	err := pool.QueryRow(ctx, `
		SELECT o.id, o.dispatch_from, o.dispatch_to, o.vehicle_number,
		       COALESCE(o.driver_name,''), COALESCE(o.driver_phone,''),
		       COALESCE(o.transporter_name,''), o.internal_reference,
		       o.contact_person_name, o.contact_person_email,
		       o.booked_for_email, o.public_tracking_token,
		       o.received_confirmation_token, o.signature_url,
		       o.received_confirmed_at, o.delivery_condition, o.delivery_condition_reason,
		       c.company_name, c.logo_url
		FROM tracker_orders o
		JOIN tracker_companies c ON c.id = o.company_id
		WHERE o.id = $1 AND o.company_id = $2
	`, orderID, companyID).Scan(
		&o.ID, &o.DispatchFrom, &o.DispatchTo, &o.VehicleNumber,
		&o.DriverName, &o.DriverPhone,
		&o.TransporterName, &o.InternalReference,
		&o.ContactPersonName, &o.ContactPersonEmail,
		&o.BookedForEmail, &o.PublicTrackingToken,
		&o.ReceivedConfirmationToken, &o.SignatureURL,
		&o.ReceivedConfirmedAt, &o.DeliveryCondition, &o.DeliveryConditionReason,
		&companyName, &companyLogoURL,
	)
	if err != nil {
		log.Printf("tracker status email: failed to load order=%s status=%s: %v", orderID, status, err)
		return
	}

	ccEmails, bccEmails, err := fetchTrackerOrderCCEmails(ctx, pool, orderID)
	if err != nil {
		log.Printf("tracker status email: failed to load cc/bcc for order=%s: %v", orderID, err)
		return
	}

	sendTrackerOrderStatusEmail(cfg, o, companyName, companyLogoURL, status, emailCopy, ccEmails, bccEmails)
}

func sendTrackerOrderStatusEmail(cfg *config.Config, o TrackerOrder, companyName string, companyLogoURL *string, status string, emailCopy trackerStatusEmailCopyEntry, ccEmails, bccEmails []string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("tracker status email: recovered from panic: %v", r)
			}
		}()
		if !mail.IsConfigured(cfg) {
			return
		}

		var toList []string
		if o.BookedForEmail != nil && *o.BookedForEmail != "" {
			toList = append(toList, *o.BookedForEmail)
		}
		if o.ContactPersonEmail != nil && *o.ContactPersonEmail != "" &&
			(o.BookedForEmail == nil || *o.ContactPersonEmail != *o.BookedForEmail) {
			toList = append(toList, *o.ContactPersonEmail)
		}
		if len(toList) == 0 && len(ccEmails) == 0 && len(bccEmails) == 0 {
			return // nobody to send to
		}

		trackingLink := strings.TrimRight(cfg.TrackerPanelURL, "/") + "/track/" + o.PublicTrackingToken

		var attachments []mail.Attachment
		// Proof of delivery is a separate concept from the 'delivered'
		// status itself — the driver signs (delivery_claimed event) before
		// the company runs this transition, but signing isn't guaranteed to
		// have happened yet, so signature_url may still be nil here.
		if status == "delivered" && o.SignatureURL != nil && *o.SignatureURL != "" {
			built, _ := buildTrackerEmailAttachments([]trackerEmailAttachable{
				{Label: "Proof of Delivery", FileURL: *o.SignatureURL},
			})
			attachments = built
		}

		subjectRef := o.ID
		if o.InternalReference != nil && *o.InternalReference != "" {
			subjectRef = *o.InternalReference
		}
		subject := fmt.Sprintf("%s — %s from %s", emailCopy.Headline, subjectRef, companyName)

		var plainBody, htmlBody string
		if status == "delivered" {
			// Delivered gets its own bespoke builder (table layout + real
			// delivery-condition status) instead of the generic
			// plain-text-in-a-box every other status transition uses — see
			// buildTrackerDeliveredEmailBody/HTML below.
			plainBody = buildTrackerDeliveredEmailBody(cfg, o, companyName)
			htmlBody = buildTrackerDeliveredEmailBodyHTML(cfg, o, companyName, companyLogoURL)
		} else {
			plainBody = buildTrackerStatusEmailBody(o, companyName, status, emailCopy, trackingLink)
			htmlBody = trackerEmailFromPlainText(cfg, companyName, companyLogoURL, plainBody)
		}

		if err := mail.Send(cfg, mail.Message{
			To:          strings.Join(toList, ","),
			CC:          strings.Join(ccEmails, ","),
			BCC:         strings.Join(bccEmails, ","),
			Subject:     subject,
			Body:        plainBody,
			HTMLBody:    htmlBody,
			Attachments: attachments,
			FromName:    trackerEmailFromName(companyName),
			ReplyTo:     trackerNoReplyAddress,
		}); err != nil {
			log.Printf("tracker status email: send failed for order=%s status=%s: %v", o.ID, status, err)
		}
	}()
}

// buildTrackerStatusEmailBody builds the plain-text body for every
// non-delivered tracked transition (dispatched, in_transit, cancelled) —
// delivered has its own dedicated builder below.
func buildTrackerStatusEmailBody(o TrackerOrder, companyName, status string, emailCopy trackerStatusEmailCopyEntry, trackingLink string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s.\n\n", emailCopy.Headline))
	b.WriteString(fmt.Sprintf("Company: %s\n", companyName))
	b.WriteString(fmt.Sprintf("Route: %s -> %s\n", o.DispatchFrom, o.DispatchTo))
	b.WriteString(fmt.Sprintf("Vehicle Number: %s\n", o.VehicleNumber))
	if o.DriverName != "" {
		driver := o.DriverName
		if o.DriverPhone != "" {
			driver += " (" + o.DriverPhone + ")"
		}
		b.WriteString(fmt.Sprintf("Driver: %s\n", driver))
	}
	if o.TransporterName != "" {
		b.WriteString(fmt.Sprintf("Transporter: %s\n", o.TransporterName))
	}
	if o.InternalReference != nil && *o.InternalReference != "" {
		b.WriteString(fmt.Sprintf("Internal Reference: %s\n", *o.InternalReference))
	}

	switch status {
	case "cancelled":
		// Nothing left to track — omit the tracking link entirely rather
		// than sending the recipient to a dead-feeling status page.
	default:
		b.WriteString(fmt.Sprintf("\nTrack this shipment live: %s\n", trackingLink))
	}

	b.WriteString("\nThis is an automated message from Bogie Tracker — please do not reply to this email.\n")
	return b.String()
}

// trackerStatusEmailRows builds the [label, value] rows for the delivered
// email's FIELD/DETAILS table — Company, Route, Vehicle Number, Driver,
// mirroring trackerCreationEmailRows' shape so every tracker email's table
// looks the same.
func trackerStatusEmailRows(o TrackerOrder, companyName string) [][2]string {
	rows := [][2]string{
		{"Company", companyName},
		{"Route", o.DispatchFrom + " -> " + o.DispatchTo},
		{"Vehicle Number", o.VehicleNumber},
	}
	if o.DriverName != "" {
		driver := o.DriverName
		if o.DriverPhone != "" {
			driver += " (" + o.DriverPhone + ")"
		}
		rows = append(rows, [2]string{"Driver", driver})
	}
	return rows
}

const trackerDeliveryTimestampFormat = "02 Jan 2006, 3:04 PM"

// buildTrackerDeliveredEmailBody is the plain-text delivered-status body —
// the FIELD/DETAILS table (as text) plus the real delivery-condition
// status pulled from received_confirmed_at/delivery_condition/
// delivery_condition_reason (migration 049), instead of a tracking link.
// No tracking link at all here — see buildTrackerDeliveredEmailBodyHTML for
// why it's dropped for this status specifically.
func buildTrackerDeliveredEmailBody(cfg *config.Config, o TrackerOrder, companyName string) string {
	var b strings.Builder
	b.WriteString("Your Shipment Has Been Delivered.\n\n")
	b.WriteString(TrackerEmailDetailsTableText(trackerStatusEmailRows(o, companyName)))

	if o.SignatureURL != nil && *o.SignatureURL != "" {
		b.WriteString("\nSigned proof of delivery is attached to this email.\n")
	}

	b.WriteString("\n")
	switch {
	case o.ReceivedConfirmedAt == nil:
		// Consignee hasn't responded on the receipt page yet — nothing to
		// report, so this is the one case that still gets the link.
		if o.ReceivedConfirmationToken != nil && *o.ReceivedConfirmationToken != "" {
			receiptLink := strings.TrimRight(cfg.TrackerPanelURL, "/") + "/receipt/" + *o.ReceivedConfirmationToken
			b.WriteString(fmt.Sprintf("Once your goods arrive, confirm receipt here: %s\n", receiptLink))
		}
	case o.DeliveryCondition != nil && *o.DeliveryCondition == "bad":
		b.WriteString("❌ Reported — not in good condition\n")
		if o.DeliveryConditionReason != nil && *o.DeliveryConditionReason != "" {
			b.WriteString(fmt.Sprintf("Reason: %s\n", *o.DeliveryConditionReason))
		}
		b.WriteString(fmt.Sprintf("Reported on %s\n", o.ReceivedConfirmedAt.Format(trackerDeliveryTimestampFormat)))
	default:
		b.WriteString("✅ Received in good condition\n")
		b.WriteString(fmt.Sprintf("Confirmed on %s\n", o.ReceivedConfirmedAt.Format(trackerDeliveryTimestampFormat)))
	}

	b.WriteString("\nThis is an automated message from Bogie Tracker — please do not reply to this email.\n")
	return b.String()
}

// buildTrackerDeliveredEmailBodyHTML is the rendered-HTML counterpart of
// buildTrackerDeliveredEmailBody — same FIELD/DETAILS table
// (TrackerEmailDetailsTableHTML) every other tracker email already uses,
// plus a green/red condition badge in place of the old "Track this
// shipment" + "Confirm receipt here" link pair. The tracking link is
// dropped entirely for delivered — the shipment has already arrived, so a
// live-tracking link is dead weight on a status that means tracking is
// over; unlike dispatched/in_transit, where it's the whole point of the
// email.
func buildTrackerDeliveredEmailBodyHTML(cfg *config.Config, o TrackerOrder, companyName string, companyLogoURL *string) string {
	var b strings.Builder
	b.WriteString(`<p style="font-size:14px;color:#111827;margin:0 0 4px;">Your Shipment Has Been Delivered.</p>`)
	b.WriteString(TrackerEmailDetailsTableHTML(trackerStatusEmailRows(o, companyName)))

	if o.SignatureURL != nil && *o.SignatureURL != "" {
		b.WriteString(`<p style="font-size:13px;color:` + TrackerEmailTextGray + `;margin:0 0 8px;">Signed proof of delivery is attached to this email.</p>`)
	}

	switch {
	case o.ReceivedConfirmedAt == nil:
		if o.ReceivedConfirmationToken != nil && *o.ReceivedConfirmationToken != "" {
			receiptLink := strings.TrimRight(cfg.TrackerPanelURL, "/") + "/receipt/" + *o.ReceivedConfirmationToken
			b.WriteString(`<div style="margin:4px 0 8px;">`)
			b.WriteString(TrackerEmailButtonHTML(receiptLink, "Confirm Receipt"))
			b.WriteString(`</div>`)
		}
	case o.DeliveryCondition != nil && *o.DeliveryCondition == "bad":
		b.WriteString(`<div style="text-align:center;padding:16px 0;">`)
		b.WriteString(`<p style="font-size:15px;font-weight:700;color:#DC2626;margin:0 0 8px;">&#10060; Reported — not in good condition</p>`)
		if o.DeliveryConditionReason != nil && *o.DeliveryConditionReason != "" {
			b.WriteString(`<p style="font-size:13px;color:#6B7280;background-color:#F9FAFB;border-radius:12px;padding:12px;text-align:left;margin:0 0 8px;">` +
				html.EscapeString(*o.DeliveryConditionReason) + `</p>`)
		}
		b.WriteString(`<p style="font-size:12px;color:` + TrackerEmailMutedGray + `;margin:0;">Reported on ` +
			o.ReceivedConfirmedAt.Format(trackerDeliveryTimestampFormat) + `</p>`)
		b.WriteString(`</div>`)
	default:
		b.WriteString(`<div style="text-align:center;padding:16px 0;">`)
		b.WriteString(`<p style="font-size:15px;font-weight:700;color:#16A34A;margin:0 0 8px;">&#9989; Received in good condition</p>`)
		b.WriteString(`<p style="font-size:12px;color:` + TrackerEmailMutedGray + `;margin:0;">Confirmed on ` +
			o.ReceivedConfirmedAt.Format(trackerDeliveryTimestampFormat) + `</p>`)
		b.WriteString(`</div>`)
	}

	b.WriteString(`<p style="font-size:12px;color:` + TrackerEmailMutedGray + `;margin-top:18px;">This is an automated message from Bogie Tracker — please do not reply to this email.</p>`)

	return TrackerEmailWrapHTML(cfg, companyName, companyLogoURL, b.String())
}
