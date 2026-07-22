// Package trackerrider provisions the synthetic rider identity a Bogie
// Tracker company books rides through, so a company's bookings flow via the
// existing bookings pipeline indistinguishably from a regular rider's.
package trackerrider

import (
	"context"
	"fmt"

	"github.com/deploykit/backend/internal/db"
	"github.com/google/uuid"
)

// EnsureTrackerCompanyRiderProfile returns the rider id backing companyID's
// synthetic booking identity, creating the users+riders pair on first call.
// Idempotent: tracker_companies.synthetic_rider_id is the source of truth,
// checked (and, on the creating call, set) under a row lock so concurrent
// callers for the same company can't race into creating two riders.
//
// The synthetic user gets no password_hash — it's never logged into
// directly, only ever reached via the tracker company's own login, so unlike
// RiderSignup there's no real credential to hash. password_hash is nullable
// for exactly this reason (see the GitHub-OAuth signup path, which leaves it
// NULL for the same "not a password login" reason).
func EnsureTrackerCompanyRiderProfile(ctx context.Context, companyID string) (string, error) {
	pool := db.GetDB().GetPool()

	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("trackerrider: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var companyName, contactPhone *string
	var existingRiderID *string
	err = tx.QueryRow(ctx, `
		SELECT company_name, contact_phone, synthetic_rider_id
		FROM tracker_companies
		WHERE id = $1
		FOR UPDATE
	`, companyID).Scan(&companyName, &contactPhone, &existingRiderID)
	if err != nil {
		return "", fmt.Errorf("trackerrider: load company %s: %w", companyID, err)
	}

	if existingRiderID != nil {
		return *existingRiderID, nil
	}

	userID := uuid.New()
	riderID := uuid.New()
	syntheticEmail := fmt.Sprintf("tracker-company-%s@synthetic.gogoo.internal", companyID)

	name := "Bogie Tracker Company"
	if companyName != nil && *companyName != "" {
		name = *companyName
	}

	// riders.phone is NOT NULL but tracker_companies.contact_phone was made
	// optional in migration 033, so a company with no phone on file still
	// needs something here — this synthetic rider is never called or texted,
	// so a placeholder is fine.
	phone := "0000000000"
	if contactPhone != nil && *contactPhone != "" {
		phone = *contactPhone
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO users (id, email, name, is_verified)
		VALUES ($1, $2, $3, true)
	`, userID, syntheticEmail, name); err != nil {
		return "", fmt.Errorf("trackerrider: create synthetic user for company %s: %w", companyID, err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO riders (id, user_id, phone)
		VALUES ($1, $2, $3)
	`, riderID, userID, phone); err != nil {
		return "", fmt.Errorf("trackerrider: create synthetic rider for company %s: %w", companyID, err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE tracker_companies SET synthetic_rider_id = $1, updated_at = NOW() WHERE id = $2
	`, riderID, companyID); err != nil {
		return "", fmt.Errorf("trackerrider: store synthetic_rider_id for company %s: %w", companyID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("trackerrider: commit tx for company %s: %w", companyID, err)
	}

	return riderID.String(), nil
}
