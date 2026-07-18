// Package trackersub handles Bogie Tracker subscription lifecycle jobs —
// renewal reminders and auto-suspending companies whose subscription has
// lapsed past its grace period.
package trackersub

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
	"github.com/deploykit/backend/internal/mail"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StartSubscriptionReminderMailer ticks once a day, emailing any active
// Bogie Tracker company whose subscription expires in exactly 7 or 1 days,
// then auto-suspending any company whose subscription lapsed more than 5
// days ago. Follows the same shape as ledger.StartMonthlyStatementMailer:
// runs once immediately at startup, then a 24h ticker; a panic on one tick
// (or one company) never kills the rest.
func StartSubscriptionReminderMailer(cfg *config.Config) {
	checkAndSendReminders(cfg)
	checkAndSuspendLapsed(cfg)
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		checkAndSendReminders(cfg)
		checkAndSuspendLapsed(cfg)
	}
}

func checkAndSendReminders(cfg *config.Config) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("subscription reminder mailer: recovered from panic: %v", r)
		}
	}()

	if !mail.IsConfigured(cfg) {
		return // Resend not set up — nothing to do, no point logging daily noise
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT id, company_name, contact_email, subscription_expires_at,
		       (subscription_expires_at::date - CURRENT_DATE)::int AS days_left
		FROM tracker_companies
		WHERE status = 'active'
		  AND subscription_expires_at IS NOT NULL
		  AND (subscription_expires_at::date - CURRENT_DATE) IN (7, 1)
	`)
	if err != nil {
		log.Printf("subscription reminder mailer: query failed: %v", err)
		return
	}

	type candidate struct {
		id, companyName, contactEmail string
		expiresAt                     time.Time
		daysLeft                      int
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.companyName, &c.contactEmail, &c.expiresAt, &c.daysLeft); err != nil {
			log.Printf("subscription reminder mailer: row scan failed: %v", err)
			continue
		}
		candidates = append(candidates, c)
	}
	rows.Close()

	sent, skipped, failed := 0, 0, 0
	for _, c := range candidates {
		switch processReminder(ctx, pool, cfg, c.id, c.companyName, c.contactEmail, c.expiresAt, c.daysLeft) {
		case outcomeSent:
			sent++
		case outcomeSkipped:
			skipped++
		default:
			failed++
		}
	}
	log.Printf("subscription reminder mailer: candidates=%d sent=%d skipped=%d failed=%d", len(candidates), sent, skipped, failed)
}

type reminderOutcome int

const (
	outcomeSent reminderOutcome = iota
	outcomeSkipped
	outcomeFailed
)

// reminderTypeFor maps days-left to the reminder_type stored in
// tracker_subscription_reminders_sent. Only ever called with 7 or 1 (the
// query above only selects those), so no other case is reachable.
func reminderTypeFor(daysLeft int) string {
	if daysLeft == 1 {
		return "1_day"
	}
	return "7_day"
}

// processReminder handles one company end-to-end: idempotency check,
// idempotency insert, then the email. The insert happens before the send
// (unlike ledger's statement mailer, which records after) because the send
// is fire-and-forget — there's no later point in this function to record
// success against. A company whose reminder happens to fail delivery (bad
// address, Resend hiccup) is still marked sent for this cycle; the next
// natural retry is the 1-day reminder (a different reminder_type) or the
// company's next renewal cycle, not tomorrow's tick.
func processReminder(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, companyID, companyName, contactEmail string, expiresAt time.Time, daysLeft int) reminderOutcome {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("subscription reminder mailer: company %s panicked: %v", companyID, r)
		}
	}()

	if contactEmail == "" {
		return outcomeSkipped
	}

	reminderType := reminderTypeFor(daysLeft)

	var alreadySent bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM tracker_subscription_reminders_sent
			WHERE company_id = $1 AND expires_at = $2 AND reminder_type = $3
		)
	`, companyID, expiresAt, reminderType).Scan(&alreadySent); err != nil {
		log.Printf("subscription reminder mailer: idempotency check failed for company %s: %v", companyID, err)
		return outcomeFailed
	}
	if alreadySent {
		return outcomeSkipped
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO tracker_subscription_reminders_sent (id, company_id, expires_at, reminder_type)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (company_id, expires_at, reminder_type) DO NOTHING
	`, uuid.New(), companyID, expiresAt, reminderType); err != nil {
		log.Printf("subscription reminder mailer: failed to record reminder for company %s: %v", companyID, err)
		return outcomeFailed
	}

	sendTrackerRenewalReminderEmail(cfg, companyName, contactEmail, expiresAt, daysLeft)
	return outcomeSent
}

// checkAndSuspendLapsed suspends any active Bogie Tracker company whose
// subscription expired more than 5 days ago (the grace period). The
// UPDATE...RETURNING does the select-and-transition in one atomic
// statement, so there's no separate SELECT-then-UPDATE race to worry
// about, and no dedup table is needed the way reminders needs one — once a
// company is suspended its status no longer matches status='active', so
// the same row can never be picked up twice. Reactivation (and whether
// credentials get regenerated) is handled entirely by
// MarkTrackerPlanOrderPaid, which checks suspension_reason='expired' to
// tell an auto-suspend apart from a genuine staff suspend.
func checkAndSuspendLapsed(cfg *config.Config) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("subscription auto-suspend: recovered from panic: %v", r)
		}
	}()

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		UPDATE tracker_companies
		SET status = 'suspended', suspension_reason = 'expired', updated_at = NOW()
		WHERE status = 'active'
		  AND subscription_expires_at IS NOT NULL
		  AND subscription_expires_at < NOW() - INTERVAL '5 days'
		RETURNING id, company_name, contact_email, subscription_expires_at
	`)
	if err != nil {
		log.Printf("subscription auto-suspend: update failed: %v", err)
		return
	}

	type suspended struct {
		id, companyName, contactEmail string
		expiresAt                     time.Time
	}
	var rowsSuspended []suspended
	for rows.Next() {
		var s suspended
		if err := rows.Scan(&s.id, &s.companyName, &s.contactEmail, &s.expiresAt); err != nil {
			log.Printf("subscription auto-suspend: row scan failed: %v", err)
			continue
		}
		rowsSuspended = append(rowsSuspended, s)
	}
	rows.Close()

	for _, s := range rowsSuspended {
		log.Printf("subscription auto-suspend: company %s suspended (expired %s)", s.id, s.expiresAt.Format("2 Jan 2006"))
		if s.contactEmail != "" {
			sendTrackerSubscriptionExpiredEmail(cfg, s.companyName, s.contactEmail, s.expiresAt)
		}
	}
	if len(rowsSuspended) > 0 {
		log.Printf("subscription auto-suspend: suspended=%d", len(rowsSuspended))
	}
}

// sendTrackerRenewalReminderEmail notifies a company that its Bogie Tracker
// subscription is expiring soon. Fire-and-forget goroutine, mirrors
// handlers.sendTrackerApprovedEmail's recover/IsConfigured shape. Links to
// bogie.in's pricing/checkout flow (not the tracker panel) since renewing
// means placing a new plan order, which only exists on the marketing site.
func sendTrackerRenewalReminderEmail(cfg *config.Config, companyName, toEmail string, expiresAt time.Time, daysLeft int) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("tracker renewal reminder email: recovered from panic: %v", r)
			}
		}()
		if !mail.IsConfigured(cfg) {
			return
		}

		when := fmt.Sprintf("in %d days", daysLeft)
		if daysLeft == 1 {
			when = "tomorrow"
		}

		body := fmt.Sprintf(
			"Hi %s,\n\n"+
				"Your Bogie Tracker subscription expires %s (%s).\n\n"+
				"Renew now to avoid any interruption to your dispatch tracking, drivers, and live tracking links:\n"+
				"https://bogie.in/bogie-tracker\n\n"+
				"If your subscription lapses, your account will be automatically suspended after a short grace period.\n\n"+
				"Questions? Reply to this email or contact support@bogie.in.\n\n"+
				"Warm regards,\nTeam Bogie\nbogie.in",
			companyName, when, expiresAt.Format("2 Jan 2006"),
		)

		if err := mail.Send(cfg, mail.Message{
			To:      toEmail,
			Subject: fmt.Sprintf("Your Bogie Tracker subscription expires %s", when),
			Body:    body,
		}); err != nil {
			log.Printf("tracker renewal reminder email: send failed for %s: %v", toEmail, err)
		}
	}()
}

// sendTrackerSubscriptionExpiredEmail notifies a company that its Bogie
// Tracker access has just been suspended for a lapsed subscription —
// distinct from a staff-initiated suspension, and distinct from the
// renewal reminders above. Explains that access is paused and that placing
// a new order reactivates the account automatically, no staff approval
// needed (per MarkTrackerPlanOrderPaid's suspension_reason='expired' check).
func sendTrackerSubscriptionExpiredEmail(cfg *config.Config, companyName, toEmail string, expiresAt time.Time) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("tracker subscription expired email: recovered from panic: %v", r)
			}
		}()
		if !mail.IsConfigured(cfg) {
			return
		}

		body := fmt.Sprintf(
			"Hi %s,\n\n"+
				"Your Bogie Tracker subscription expired on %s and, after the grace period, your account has now been suspended. "+
				"Dispatch tracking, drivers, and live tracking links are paused until you renew.\n\n"+
				"To reactivate, place a new order here:\n"+
				"https://bogie.in/bogie-tracker\n\n"+
				"Reactivation is automatic as soon as the order is marked paid — no staff approval needed, and your existing login and license key will keep working.\n\n"+
				"Questions? Reply to this email or contact support@bogie.in.\n\n"+
				"Warm regards,\nTeam Bogie\nbogie.in",
			companyName, expiresAt.Format("2 Jan 2006"),
		)

		if err := mail.Send(cfg, mail.Message{
			To:      toEmail,
			Subject: "Your Bogie Tracker subscription has expired — account suspended",
			Body:    body,
		}); err != nil {
			log.Printf("tracker subscription expired email: send failed for %s: %v", toEmail, err)
		}
	}()
}
