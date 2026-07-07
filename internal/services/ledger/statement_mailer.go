package ledger

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

// MonthlyStatementDay is the day of the month the mailer sends out the
// previous month's statement to every driver with a usable email on file.
const MonthlyStatementDay = 1

// MigrateSentStatements creates the idempotency table that stops the mailer
// double-sending if the backend restarts on the 1st.
func MigrateSentStatements() error {
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS sent_statements (
			id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			driver_id  UUID NOT NULL,
			month      TEXT NOT NULL,
			sent_at    TIMESTAMPTZ DEFAULT NOW(),
			UNIQUE(driver_id, month)
		)
	`)
	return err
}

// StartMonthlyStatementMailer ticks once a day and, on MonthlyStatementDay,
// emails every driver their previous month's earnings statement. Follows the
// same shape as handlers.StartScheduledDispatcher: start once with
// `go ledger.StartMonthlyStatementMailer(cfg)` from main.go; a panic on one
// driver never stops the rest.
func StartMonthlyStatementMailer(cfg *config.Config) {
	checkAndSendMonthlyStatements(cfg)
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		checkAndSendMonthlyStatements(cfg)
	}
}

func checkAndSendMonthlyStatements(cfg *config.Config) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("monthly statement mailer: recovered from panic: %v", r)
		}
	}()

	if !mail.IsConfigured(cfg) {
		return // SMTP not set up yet — nothing to do, no point logging daily noise
	}
	if time.Now().Day() != MonthlyStatementDay {
		return
	}

	monthKey := time.Now().AddDate(0, -1, 0).Format("2006-01")
	sendStatementsForMonth(cfg, monthKey)
}

func sendStatementsForMonth(cfg *config.Config, monthKey string) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT d.id
		FROM drivers d
		JOIN users u ON u.id = d.user_id
		WHERE u.email IS NOT NULL AND u.email LIKE '%@%'
	`)
	if err != nil {
		log.Printf("monthly statement mailer: driver list query failed: %v", err)
		return
	}
	var driverIDs []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			driverIDs = append(driverIDs, id)
		}
	}
	rows.Close()

	sent, skipped, failed := 0, 0, 0
	for _, driverID := range driverIDs {
		outcome, _ := sendOneStatement(ctx, cfg, driverID, monthKey)
		switch outcome {
		case outcomeSent:
			sent++
		case outcomeSkipped:
			skipped++
		case outcomeFailed:
			failed++
		}
	}
	log.Printf("monthly statement mailer: month=%s sent=%d skipped=%d failed=%d", monthKey, sent, skipped, failed)
}

type sendOutcome int

const (
	outcomeSent sendOutcome = iota
	outcomeSkipped
	outcomeFailed
)

// sendOneStatement handles a single driver end-to-end and never lets a
// panic escape — the caller loop must keep going regardless of what
// happens to any individual driver. The returned error is nil unless
// outcome is outcomeFailed.
func sendOneStatement(ctx context.Context, cfg *config.Config, driverID string, monthKey string) (outcome sendOutcome, retErr error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("monthly statement mailer: driver %s panicked: %v", driverID, r)
			outcome = outcomeFailed
			retErr = fmt.Errorf("panic: %v", r)
		}
	}()

	pool := db.GetDB().GetPool()

	var alreadySent bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sent_statements WHERE driver_id = $1 AND month = $2)`,
		driverID, monthKey).Scan(&alreadySent)
	if err != nil {
		log.Printf("monthly statement mailer: idempotency check failed for driver %s: %v", driverID, err)
		return outcomeFailed, fmt.Errorf("idempotency check failed: %w", err)
	}
	if alreadySent {
		return outcomeSkipped, nil
	}

	stmt, err := BuildStatement(ctx, pool, driverID, monthKey)
	if err != nil {
		log.Printf("monthly statement mailer: build failed for driver %s: %v", driverID, err)
		return outcomeFailed, fmt.Errorf("BuildStatement failed: %w", err)
	}
	if stmt.DriverEmail == "" || !strings.Contains(stmt.DriverEmail, "@") {
		return outcomeSkipped, nil
	}

	pdfBytes, err := GeneratePDF(stmt)
	if err != nil {
		log.Printf("monthly statement mailer: pdf failed for driver %s: %v", driverID, err)
		return outcomeFailed, fmt.Errorf("GeneratePDF failed: %w", err)
	}

	monthLabel := stmt.PeriodLabel
	body := fmt.Sprintf(
		"Hi %s,\n\nYour gogoo earnings statement for %s is attached.\n\n"+
			"Opening balance: Rs.%.0f\nClosing balance: Rs.%.0f\n\n"+
			"Questions about your earnings? Reply to this email or contact support@bogie.in.\n\n"+
			"— Team gogoo",
		stmt.DriverName, monthLabel, stmt.OpeningBalance, stmt.ClosingBalance,
	)

	err = mail.Send(cfg, mail.Message{
		To:      stmt.DriverEmail,
		Subject: fmt.Sprintf("Your gogoo earnings statement — %s", monthKey),
		Body:    body,
		Attachments: []mail.Attachment{{
			Filename:    fmt.Sprintf("gogoo-ledger-%s-%s.pdf", driverID, monthKey),
			ContentType: "application/pdf",
			Data:        pdfBytes,
		}},
	})
	if err != nil {
		log.Printf("monthly statement mailer: send failed for driver %s: %v", driverID, err)
		return outcomeFailed, fmt.Errorf("mail.Send failed: %w", err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO sent_statements (id, driver_id, month) VALUES ($1, $2, $3)
		ON CONFLICT (driver_id, month) DO NOTHING
	`, uuid.New(), driverID, monthKey)
	if err != nil {
		// Email already went out — log loudly since a retry next tick would double-send.
		log.Printf("monthly statement mailer: CRITICAL — sent to driver %s but failed to record idempotency row: %v", driverID, err)
	}

	return outcomeSent, nil
}
