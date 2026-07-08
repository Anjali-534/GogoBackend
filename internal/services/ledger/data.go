// Package ledger builds and renders a driver's monthly earnings statement —
// the same Statement + PDF bytes are used by both the on-demand download
// endpoint and the automated monthly emailer, so there is exactly one place
// that knows what a statement looks like.
package ledger

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Entry is one line of the statement's transaction table.
type Entry struct {
	Date        time.Time
	Description string
	Type        string
	IsDebit     bool
	Amount      float64
	Running     float64
}

// Statement is everything needed to render a bank-style PDF for one driver
// for one calendar month.
type Statement struct {
	DriverID       string
	DriverName     string
	DriverPhone    string
	DriverEmail    string
	MonthKey       string // "2026-06"
	PeriodLabel    string // "1 Jun 2026 - 30 Jun 2026"
	OpeningBalance float64
	ClosingBalance float64
	TotalCredit    float64
	TotalDebit     float64
	Entries        []Entry
}

// delta returns the signed effect of a ledger row on the wallet balance —
// driver_earnings.amount is always stored positive; is_debit carries the sign.
func delta(amount float64, isDebit bool) float64 {
	if isDebit {
		return -amount
	}
	return amount
}

// BuildStatement assembles a Statement for driverID covering monthKey
// ("YYYY-MM"). Balances are reconstructed from the driver's CURRENT wallet
// balance minus every ledger delta recorded after the statement period,
// since driver_earnings only stores deltas, not historical snapshots.
func BuildStatement(ctx context.Context, pool *pgxpool.Pool, driverID string, monthKey string) (*Statement, error) {
	monthStart, err := time.Parse("2006-01", monthKey)
	if err != nil {
		return nil, fmt.Errorf("invalid month %q: %w", monthKey, err)
	}
	monthEnd := monthStart.AddDate(0, 1, 0)

	var name, email, phone string
	var currentBalance float64
	err = pool.QueryRow(ctx, `
		SELECT u.name, u.email, d.phone, COALESCE(d.wallet_balance, -700.00)
		FROM drivers d
		JOIN users u ON u.id = d.user_id
		WHERE d.id = $1
	`, driverID).Scan(&name, &email, &phone, &currentBalance)
	if err != nil {
		return nil, fmt.Errorf("driver lookup failed: %w", err)
	}

	// Everything recorded strictly after the statement period, netted off
	// the current balance, gives the balance as it stood at monthEnd.
	var deltaAfterPeriod float64
	err = pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN is_debit THEN -amount ELSE amount END), 0)
		FROM driver_earnings
		WHERE driver_id = $1 AND created_at >= $2
	`, driverID, monthEnd).Scan(&deltaAfterPeriod)
	if err != nil {
		return nil, fmt.Errorf("post-period sum failed: %w", err)
	}
	closingBalance := currentBalance - deltaAfterPeriod

	rows, err := pool.Query(ctx, `
		SELECT created_at, amount, type, COALESCE(description,''), is_debit, COALESCE(debit_type,'')
		FROM driver_earnings
		WHERE driver_id = $1 AND created_at >= $2 AND created_at < $3
		ORDER BY created_at ASC
	`, driverID, monthStart, monthEnd)
	if err != nil {
		return nil, fmt.Errorf("entries query failed: %w", err)
	}
	defer rows.Close()

	type raw struct {
		createdAt   time.Time
		amount      float64
		typ         string
		description string
		isDebit     bool
		debitType   string
	}
	var rawEntries []raw
	var withinPeriodDelta, totalCredit, totalDebit float64
	for rows.Next() {
		var r raw
		if err := rows.Scan(&r.createdAt, &r.amount, &r.typ, &r.description, &r.isDebit, &r.debitType); err != nil {
			continue
		}
		rawEntries = append(rawEntries, r)
		withinPeriodDelta += delta(r.amount, r.isDebit)
		if r.isDebit {
			totalDebit += r.amount
		} else {
			totalCredit += r.amount
		}
	}

	openingBalance := closingBalance - withinPeriodDelta

	entries := make([]Entry, 0, len(rawEntries))
	running := openingBalance
	for _, r := range rawEntries {
		running += delta(r.amount, r.isDebit)
		entries = append(entries, Entry{
			Date:        r.createdAt,
			Description: describeEntry(r.typ, r.description, r.debitType),
			Type:        r.typ,
			IsDebit:     r.isDebit,
			Amount:      r.amount,
			Running:     running,
		})
	}

	periodEndDisplay := monthEnd.AddDate(0, 0, -1)
	return &Statement{
		DriverID:       driverID,
		DriverName:     name,
		DriverPhone:    phone,
		DriverEmail:    email,
		MonthKey:       monthKey,
		PeriodLabel:    fmt.Sprintf("%s - %s", monthStart.Format("2 Jan 2006"), periodEndDisplay.Format("2 Jan 2006")),
		OpeningBalance: openingBalance,
		ClosingBalance: closingBalance,
		TotalCredit:    totalCredit,
		TotalDebit:     totalDebit,
		Entries:        entries,
	}, nil
}

// describeEntry mirrors the driver app's ledger screen labeling
// (profile/ledger.tsx's entryLabel) so the PDF and in-app list never
// disagree about what a transaction is called.
func describeEntry(typ, description, debitType string) string {
	switch {
	case debitType == "registration_fee":
		return "Registration Fee"
	case debitType == "commission":
		return "bogie Commission (20%)"
	case typ == "ride":
		return "Trip Earnings"
	case typ == "referral":
		return "Referral Bonus — friend's first trip"
	case description != "":
		return description
	default:
		return "Transaction"
	}
}
