// Package trackerwalletbilling handles the Bogie Tracker daily
// subscription-charge job — auto-debiting each company's wallet for its
// subscription on next_billing_date, replacing the old manual
// staff-marked-paid plan-orders flow as the thing that keeps a company's
// subscription current. Mirrors trackersub/trackerdelivery's shape: runs
// once immediately at startup, then a 24h ticker; a panic on one tick (or
// one company) never kills the rest.
//
// Unlike the delivery reminders, there's no arrears/catch-up model and no
// cap: every tick just charges whatever's due today and moves on. A failed
// charge still advances next_billing_date by a month and flips the company
// to 'overdue' — the next day's tick retries against that new date, not
// against the missed one.
package trackerwalletbilling

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// defaultSubscriptionAmount is charged when a company has no
// subscription_amount override set (migration 052) — a placeholder until
// product finalizes real per-tier pricing.
const defaultSubscriptionAmount = 999.00

func StartWalletBillingJob(cfg *config.Config) {
	runBillingCycle()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		runBillingCycle()
	}
}

type candidate struct {
	companyID          string
	subscriptionAmount *float64
}

func runBillingCycle() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("wallet billing job: recovered from panic: %v", r)
		}
	}()

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT id, subscription_amount FROM tracker_companies
		WHERE next_billing_date IS NOT NULL
		  AND next_billing_date <= CURRENT_DATE
		  AND subscription_status != 'paused'
	`)
	if err != nil {
		log.Printf("wallet billing job: query failed: %v", err)
		return
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.companyID, &c.subscriptionAmount); err != nil {
			log.Printf("wallet billing job: row scan failed: %v", err)
			continue
		}
		candidates = append(candidates, c)
	}
	rows.Close()

	charged, failed, skipped := 0, 0, 0
	for _, c := range candidates {
		switch chargeCompany(ctx, c) {
		case outcomeCharged:
			charged++
		case outcomeFailed:
			failed++
		default:
			skipped++
		}
	}
	log.Printf("wallet billing job: candidates=%d charged=%d failed=%d skipped=%d", len(candidates), charged, failed, skipped)
}

type outcome int

const (
	outcomeCharged outcome = iota
	outcomeFailed
	outcomeSkipped
)

// chargeCompany attempts one company's charge for today. The
// tracker_wallet_charge_attempts insert happens FIRST, before any balance
// is touched — its UNIQUE(company_id, billing_date) constraint is what
// makes a duplicate/overlapping tick for the same day a no-op (outcomeSkipped)
// rather than a double charge.
func chargeCompany(ctx context.Context, c candidate) (result outcome) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("wallet billing job: company %s panicked: %v", c.companyID, r)
			result = outcomeFailed
		}
	}()

	pool := db.GetDB().GetPool()
	amount := defaultSubscriptionAmount
	if c.subscriptionAmount != nil {
		amount = *c.subscriptionAmount
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return outcomeFailed
	}
	defer tx.Rollback(ctx)

	attemptID := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO tracker_wallet_charge_attempts (id, company_id, billing_date, status, amount)
		VALUES ($1, $2, CURRENT_DATE, 'failed', $3)
	`, attemptID, c.companyID, amount)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return outcomeSkipped
		}
		log.Printf("wallet billing job: failed to claim attempt slot for company %s: %v", c.companyID, err)
		return outcomeFailed
	}

	var balance float64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(wallet_balance,0) FROM tracker_companies WHERE id=$1 FOR UPDATE`, c.companyID).Scan(&balance); err != nil {
		log.Printf("wallet billing job: company %s not found: %v", c.companyID, err)
		return outcomeFailed
	}

	success := balance >= amount
	newSubStatus := "overdue"
	if success {
		newBalance := balance - amount
		ledgerID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO tracker_wallet_ledger (id, company_id, type, amount, balance_after, status)
			VALUES ($1, $2, 'subscription_charge', $3, $4, 'completed')
		`, ledgerID, c.companyID, -amount, newBalance); err != nil {
			log.Printf("wallet billing job: ledger insert failed for company %s: %v", c.companyID, err)
			return outcomeFailed
		}
		if _, err := tx.Exec(ctx, `
			UPDATE tracker_wallet_charge_attempts SET status='charged', wallet_ledger_id=$1 WHERE id=$2
		`, ledgerID, attemptID); err != nil {
			return outcomeFailed
		}
		if _, err := tx.Exec(ctx, `UPDATE tracker_companies SET wallet_balance=$1 WHERE id=$2`, newBalance, c.companyID); err != nil {
			return outcomeFailed
		}
		newSubStatus = "active"
	}

	if _, err := tx.Exec(ctx, `
		UPDATE tracker_companies
		SET next_billing_date = (next_billing_date + INTERVAL '1 month')::date,
		    subscription_status = $1
		WHERE id = $2
	`, newSubStatus, c.companyID); err != nil {
		log.Printf("wallet billing job: failed to advance billing date for company %s: %v", c.companyID, err)
		return outcomeFailed
	}

	if err := tx.Commit(ctx); err != nil {
		return outcomeFailed
	}
	if success {
		return outcomeCharged
	}
	return outcomeFailed
}
