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

type candidate struct {
	orderID, consigneeName, consigneeEmail, bookedForEmail, receiptToken string
	claimedAt                                                            time.Time
}

func checkAndSendReminders(cfg *config.Config) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("delivery reminder mailer: recovered from panic: %v", r)
		}
	}()

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT o.id, COALESCE(o.consignee_name, ''), COALESCE(o.consignee_email, ''),
		       COALESCE(o.booked_for_email, ''), o.received_confirmation_token, e.claimed_at
		FROM tracker_orders o
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
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.orderID, &c.consigneeName, &c.consigneeEmail, &c.bookedForEmail, &c.receiptToken, &c.claimedAt); err != nil {
			log.Printf("delivery reminder mailer: row scan failed: %v", err)
			continue
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
			log.Printf("delivery reminder mailer: order %s panicked: %v", c.orderID, r)
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
		`, c.orderID); err != nil {
			log.Printf("delivery reminder mailer: failed to flag order %s: %v", c.orderID, err)
			return outcomeFailed
		}
		log.Printf("delivery reminder mailer: order %s flagged needs_staff_attention after %d reminders with no response", c.orderID, maxReminders)
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
	`, c.orderID, reminderNumber).Scan(&alreadySent); err != nil {
		log.Printf("delivery reminder mailer: idempotency check failed for order %s: %v", c.orderID, err)
		return outcomeFailed
	}
	if alreadySent {
		return outcomeSkipped
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO tracker_delivery_reminders_sent (id, order_id, reminder_number)
		VALUES ($1, $2, $3)
		ON CONFLICT (order_id, reminder_number) DO NOTHING
	`, uuid.New(), c.orderID, reminderNumber); err != nil {
		log.Printf("delivery reminder mailer: failed to record reminder for order %s: %v", c.orderID, err)
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
	if c.consigneeEmail != "" {
		out = append(out, c.consigneeEmail)
	}
	if c.bookedForEmail != "" && c.bookedForEmail != c.consigneeEmail {
		out = append(out, c.bookedForEmail)
	}
	return out
}

// sendTrackerDeliveryReminderEmail nudges the consignee to confirm receipt.
// Fire-and-forget goroutine, same recover/IsConfigured shape as every other
// tracker email sender (see tracker_mail.go).
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

		greeting := "Hi,"
		if c.consigneeName != "" {
			greeting = fmt.Sprintf("Hi %s,", c.consigneeName)
		}

		receiptLink := ""
		if c.receiptToken != "" {
			receiptLink = strings.TrimRight(cfg.TrackerPanelURL, "/") + "/receipt/" + c.receiptToken
		}

		body := fmt.Sprintf(
			"%s\n\n"+
				"Our driver has marked your shipment as delivered, but we haven't heard back from you yet "+
				"confirming receipt. Please take a moment to confirm:\n\n%s\n\n"+
				"This is reminder %d of %d — if we don't hear back after %d days, "+
				"we'll flag this order for our team to follow up with you directly.\n\n"+
				"Questions? Reply to this email.\n\n"+
				"Warm regards,\nTeam Bogie\nbogie.in",
			greeting, receiptLink, reminderNumber, maxReminders, maxReminders,
		)

		if err := mail.Send(cfg, mail.Message{
			To:      strings.Join(toList, ","),
			Subject: "Please confirm receipt of your delivery",
			Body:    body,
		}); err != nil {
			log.Printf("tracker delivery reminder email: send failed for order=%s: %v", c.orderID, err)
		}
	}()
}
