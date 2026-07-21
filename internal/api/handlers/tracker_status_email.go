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
	err := pool.QueryRow(ctx, `
		SELECT o.id, o.dispatch_from, o.dispatch_to, o.vehicle_number,
		       COALESCE(o.driver_name,''), COALESCE(o.driver_phone,''),
		       COALESCE(o.transporter_name,''), o.internal_reference,
		       o.contact_person_name, o.contact_person_email,
		       o.booked_for_email, o.public_tracking_token,
		       o.received_confirmation_token, o.signature_url,
		       c.company_name
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
		&companyName,
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

	sendTrackerOrderStatusEmail(cfg, o, companyName, status, emailCopy, ccEmails, bccEmails)
}

func sendTrackerOrderStatusEmail(cfg *config.Config, o TrackerOrder, companyName, status string, emailCopy trackerStatusEmailCopyEntry, ccEmails, bccEmails []string) {
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

		if err := mail.Send(cfg, mail.Message{
			To:          strings.Join(toList, ","),
			CC:          strings.Join(ccEmails, ","),
			BCC:         strings.Join(bccEmails, ","),
			Subject:     subject,
			Body:        buildTrackerStatusEmailBody(cfg, o, status, emailCopy, trackingLink),
			Attachments: attachments,
			FromName:    "Bogie Tracker - " + companyName,
			ReplyTo:     trackerNoReplyAddress,
		}); err != nil {
			log.Printf("tracker status email: send failed for order=%s status=%s: %v", o.ID, status, err)
		}
	}()
}

func buildTrackerStatusEmailBody(cfg *config.Config, o TrackerOrder, status string, emailCopy trackerStatusEmailCopyEntry, trackingLink string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s.\n\n", emailCopy.Headline))
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
	case "delivered":
		b.WriteString(fmt.Sprintf("\nTrack this shipment: %s\n", trackingLink))
		if o.SignatureURL != nil && *o.SignatureURL != "" {
			b.WriteString("\nSigned proof of delivery is attached to this email.\n")
		}
		if o.ReceivedConfirmationToken != nil && *o.ReceivedConfirmationToken != "" {
			receiptLink := strings.TrimRight(cfg.TrackerPanelURL, "/") + "/receipt/" + *o.ReceivedConfirmationToken
			b.WriteString(fmt.Sprintf("\nOnce your goods arrive, confirm receipt here: %s\n", receiptLink))
		}
	default:
		b.WriteString(fmt.Sprintf("\nTrack this shipment live: %s\n", trackingLink))
	}

	b.WriteString("\nThis is an automated message from Bogie Tracker — please do not reply to this email.\n")
	return b.String()
}
