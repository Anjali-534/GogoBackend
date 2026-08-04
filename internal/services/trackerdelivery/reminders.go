// Package trackerdelivery handles the Bogie Tracker delivery-confirmation
// reminder job — nudging the consignee to respond on the receipt page once
// the driver has claimed delivery, and flagging the order for staff
// attention if 7 daily reminders get no response. Mirrors trackersub's
// shape (StartSubscriptionReminderMailer): runs once immediately at
// startup, then a 24h ticker; a panic on one tick (or one order) never
// kills the rest.
package trackerdelivery

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/deploykit/backend/internal/api/handlers"
	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
	"github.com/deploykit/backend/internal/mail"
	"github.com/google/uuid"
)

const maxReminders = 7

// StartDeliveryReminderMailer ticks once a day, emailing the consignee (and
// booked-for party) of any order where the driver has claimed delivery
// (delivery_claimed event + signature) but received_confirmed_at is still
// NULL — one reminder per day, up to maxReminders. Past that cap with still
// no response, needs_staff_attention is set and the order stops being
// picked up by the query below (no more reminders, no re-flagging).
func StartDeliveryReminderMailer(cfg *config.Config) {
	checkAndSendReminders(cfg)
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		checkAndSendReminders(cfg)
	}
}

// candidate bundles an order's dispatch-shaped fields (for the reused
// shipment-details table), a snapshot of the sending company's own
// name/logo (for the shared email footer), and receipt/claim metadata —
// everything sendTrackerDeliveryReminderEmail needs to build a body that
// matches the dispatch-details email in look and content.
type candidate struct {
	order          handlers.TrackerOrder
	companyName    string
	companyLogoURL *string
	receiptToken   string
	claimedAt      time.Time
}

func checkAndSendReminders(cfg *config.Config) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("delivery reminder mailer: recovered from panic: %v", r)
		}
	}()

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Selects every field DispatchEmailRows needs (same set the
	// dispatch-details email pulls in tracker_notify.go) plus the sending
	// company's own name/logo for the shared email footer.
	rows, err := pool.Query(ctx, `
		SELECT o.id, o.booked_for_company_name, o.booked_for_state,
		       o.consignee_name, o.consignee_state,
		       COALESCE(o.consignee_email, ''), COALESCE(o.booked_for_email, ''),
		       o.dispatch_from, o.dispatch_to, o.material, o.quantity, o.vehicle_number,
		       COALESCE(o.driver_name, ''), COALESCE(o.driver_phone, ''),
		       COALESCE(o.transporter_name, ''), o.dispatch_datetime, o.documents_enclosed,
		       COALESCE(o.received_confirmation_token, ''),
		       c.company_name, c.logo_url,
		       e.claimed_at
		FROM tracker_orders o
		JOIN tracker_companies c ON c.id = o.company_id
		JOIN LATERAL (
			SELECT MIN(created_at) AS claimed_at FROM tracker_order_events
			WHERE order_id = o.id AND reported_by = 'driver' AND event_kind = 'delivery_claimed'
		) e ON true
		WHERE o.status NOT IN ('delivered', 'cancelled')
		  AND o.signature_url IS NOT NULL
		  AND o.received_confirmed_at IS NULL
		  AND o.needs_staff_attention = FALSE
		  AND e.claimed_at IS NOT NULL
		  AND e.claimed_at <= NOW() - INTERVAL '24 hours'
	`)
	if err != nil {
		log.Printf("delivery reminder mailer: query failed: %v", err)
		return
	}

	var candidates []candidate
	var consigneeEmail, bookedForEmail string
	for rows.Next() {
		var c candidate
		if err := rows.Scan(
			&c.order.ID, &c.order.BookedForCompanyName, &c.order.BookedForState,
			&c.order.ConsigneeName, &c.order.ConsigneeState,
			&consigneeEmail, &bookedForEmail,
			&c.order.DispatchFrom, &c.order.DispatchTo, &c.order.Material, &c.order.Quantity, &c.order.VehicleNumber,
			&c.order.DriverName, &c.order.DriverPhone,
			&c.order.TransporterName, &c.order.DispatchDatetime, &c.order.DocumentsEnclosed,
			&c.receiptToken,
			&c.companyName, &c.companyLogoURL,
			&c.claimedAt,
		); err != nil {
			log.Printf("delivery reminder mailer: row scan failed: %v", err)
			continue
		}
		if consigneeEmail != "" {
			c.order.ConsigneeEmail = &consigneeEmail
		}
		if bookedForEmail != "" {
			c.order.BookedForEmail = &bookedForEmail
		}
		candidates = append(candidates, c)
	}
	rows.Close()

	sent, skipped, flagged, failed := 0, 0, 0, 0
	for _, c := range candidates {
		switch processCandidate(ctx, cfg, c) {
		case outcomeSent:
			sent++
		case outcomeSkipped:
			skipped++
		case outcomeFlagged:
			flagged++
		default:
			failed++
		}
	}
	log.Printf("delivery reminder mailer: candidates=%d sent=%d skipped=%d flagged=%d failed=%d", len(candidates), sent, skipped, flagged, failed)
}

type outcome int

const (
	outcomeSent outcome = iota
	outcomeSkipped
	outcomeFlagged
	outcomeFailed
)

// processCandidate handles one order end-to-end. reminderNumber is the
// whole number of 24h periods since the driver claimed delivery — 1 on the
// first eligible tick, up to maxReminders. Past maxReminders, the order is
// flagged needs_staff_attention instead of emailed, and the query in
// checkAndSendReminders excludes it from every future tick (no cleanup
// needed).
func processCandidate(ctx context.Context, cfg *config.Config, c candidate) outcome {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("delivery reminder mailer: order %s panicked: %v", c.order.ID, r)
		}
	}()

	pool := db.GetDB().GetPool()
	reminderNumber := int(time.Since(c.claimedAt).Hours() / 24)
	if reminderNumber < 1 {
		reminderNumber = 1
	}

	if reminderNumber > maxReminders {
		if _, err := pool.Exec(ctx, `
			UPDATE tracker_orders SET needs_staff_attention = TRUE, updated_at = NOW() WHERE id = $1
		`, c.order.ID); err != nil {
			log.Printf("delivery reminder mailer: failed to flag order %s: %v", c.order.ID, err)
			return outcomeFailed
		}
		log.Printf("delivery reminder mailer: order %s flagged needs_staff_attention after %d reminders with no response", c.order.ID, maxReminders)
		return outcomeFlagged
	}

	toList := recipientList(c)
	if len(toList) == 0 {
		return outcomeSkipped
	}

	var alreadySent bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM tracker_delivery_reminders_sent
			WHERE order_id = $1 AND reminder_number = $2
		)
	`, c.order.ID, reminderNumber).Scan(&alreadySent); err != nil {
		log.Printf("delivery reminder mailer: idempotency check failed for order %s: %v", c.order.ID, err)
		return outcomeFailed
	}
	if alreadySent {
		return outcomeSkipped
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO tracker_delivery_reminders_sent (id, order_id, reminder_number)
		VALUES ($1, $2, $3)
		ON CONFLICT (order_id, reminder_number) DO NOTHING
	`, uuid.New(), c.order.ID, reminderNumber); err != nil {
		log.Printf("delivery reminder mailer: failed to record reminder for order %s: %v", c.order.ID, err)
		return outcomeFailed
	}

	sendTrackerDeliveryReminderEmail(cfg, c, reminderNumber)
	return outcomeSent
}

// recipientList mirrors the receipt-link recipient rule used at dispatch
// time (tracker_notify.go): consignee and booked-for, deduped, empties
// dropped — whichever of the two has an email on file is the one who's
// actually expected to respond on the receipt page.
func recipientList(c candidate) []string {
	var out []string
	if c.order.ConsigneeEmail != nil && *c.order.ConsigneeEmail != "" {
		out = append(out, *c.order.ConsigneeEmail)
	}
	if c.order.BookedForEmail != nil && *c.order.BookedForEmail != "" &&
		(c.order.ConsigneeEmail == nil || *c.order.BookedForEmail != *c.order.ConsigneeEmail) {
		out = append(out, *c.order.BookedForEmail)
	}
	return out
}

// sendTrackerDeliveryReminderEmail nudges the consignee to confirm receipt.
// Fire-and-forget goroutine, same recover/IsConfigured shape as every other
// tracker email sender (see tracker_mail.go). Body/HTML body reuse the same
// shipment-details table (handlers.DispatchEmailRows) and shared HTML chrome
// (handlers.TrackerEmail*) as the dispatch-details email, so the two stay
// visually and structurally consistent.
func sendTrackerDeliveryReminderEmail(cfg *config.Config, c candidate, reminderNumber int) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("tracker delivery reminder email: recovered from panic: %v", r)
			}
		}()
		if !mail.IsConfigured(cfg) {
			return
		}

		toList := recipientList(c)
		if len(toList) == 0 {
			return
		}

		receiptLink := ""
		if c.receiptToken != "" {
			receiptLink = strings.TrimRight(cfg.TrackerPanelURL, "/") + "/receipt/" + c.receiptToken
		}

		rows := handlers.DispatchEmailRows(c.order)

		body := buildDeliveryReminderEmailBody(rows, receiptLink, reminderNumber)
		htmlBody := buildDeliveryReminderEmailBodyHTML(cfg, rows, c.companyName, c.companyLogoURL, receiptLink, reminderNumber)

		if err := mail.Send(cfg, mail.Message{
			To:       strings.Join(toList, ","),
			Subject:  "Please confirm receipt of your delivery",
			Body:     body,
			HTMLBody: htmlBody,
		}); err != nil {
			log.Printf("tracker delivery reminder email: send failed for order=%s: %v", c.order.ID, err)
		}
	}()
}

// buildDeliveryReminderEmailBody is the plain-text reminder body — same
// shipment-details table (via TrackerEmailDetailsTableText, shared with the
// dispatch-details email) as its HTML counterpart below.
func buildDeliveryReminderEmailBody(rows [][2]string, receiptLink string, reminderNumber int) string {
	var b strings.Builder
	b.WriteString("Dear Sir/Ma'am,\n\n")
	b.WriteString("This is a reminder mail — our driver has marked your shipment as delivered, but we haven't yet received your confirmation. Please review the shipment details below.\n\n")
	b.WriteString("DISPATCH DETAILS\n")
	b.WriteString("=================\n\n")
	b.WriteString(handlers.TrackerEmailDetailsTableText(rows))
	b.WriteString("\n")
	if receiptLink != "" {
		b.WriteString(fmt.Sprintf("Please confirm receipt here: %s\n\n", receiptLink))
	}
	b.WriteString(fmt.Sprintf(
		"This is reminder %d of %d — if we don't hear back after %d days, we'll flag this order for our team to follow up with you directly.\n\n",
		reminderNumber, maxReminders, maxReminders,
	))
	b.WriteString("Questions? Reply to this email.\n\n")
	b.WriteString("Warm regards,\nTeam Bogie\nbogie.in")
	return b.String()
}

// buildDeliveryReminderEmailBodyHTML mirrors buildDispatchEmailBodyHTML's
// shape (tracker_notify.go) — same table, same styled-button pattern, same
// card+footer chrome — but with the reminder-specific opening/CTA and a
// reminder-count note instead of a tracking link.
func buildDeliveryReminderEmailBodyHTML(cfg *config.Config, rows [][2]string, companyName string, companyLogoURL *string, receiptLink string, reminderNumber int) string {
	var b strings.Builder
	b.WriteString(`<p style="font-size:14px;color:#111827;margin:0 0 4px;">Dear Sir/Ma'am,</p>`)
	b.WriteString(`<p style="font-size:14px;color:` + handlers.TrackerEmailTextGray + `;margin:0 0 4px;">This is a reminder mail — our driver has marked your shipment as delivered, but we haven't yet received your confirmation. Please review the shipment details below.</p>`)
	b.WriteString(handlers.TrackerEmailDetailsTableHTML(rows))

	if receiptLink != "" {
		b.WriteString(`<div style="margin:4px 0 8px;">`)
		b.WriteString(handlers.TrackerEmailButtonHTML(receiptLink, "Confirm Receipt"))
		b.WriteString(`</div>`)
	}

	b.WriteString(fmt.Sprintf(
		`<p style="font-size:12px;color:%s;margin-top:18px;">This is reminder %d of %d — if we don't hear back after %d days, we'll flag this order for our team to follow up with you directly.</p>`,
		handlers.TrackerEmailMutedGray, reminderNumber, maxReminders, maxReminders,
	))

	return handlers.TrackerEmailWrapHTML(cfg, companyName, companyLogoURL, b.String())
}
