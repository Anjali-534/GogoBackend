package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type driverDeleteRequest struct {
	Password string `json:"password" binding:"required"`
}

// hashAadhaar computes a one-way SHA-256 hash of an Aadhaar number salted
// with a server-side pepper (AADHAAR_HASH_PEPPER env var, never hardcoded
// or derived from any value already in the database). SHA-256 has no
// reverse function — there is no code path anywhere that recovers the
// original Aadhaar number from this hash, only ever a forward
// hash-and-compare against a newly submitted number.
func hashAadhaar(aadhaarNumber string) string {
	pepper := os.Getenv("AADHAAR_HASH_PEPPER")
	sum := sha256.Sum256([]byte(aadhaarNumber + pepper))
	return hex.EncodeToString(sum[:])
}

// driverDeletionBlockers runs every eligibility check and returns one
// human-readable reason per failed condition — never a generic message —
// so both the pre-check and the final delete call can surface exactly
// what's blocking the driver.
func driverDeletionBlockers(ctx context.Context, pool *pgxpool.Pool, driverID string) ([]string, error) {
	var reasons []string

	var activeTrips int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM bookings
		WHERE driver_id=$1 AND status IN ('accepted','arriving','in_progress')
	`, driverID).Scan(&activeTrips); err != nil {
		return nil, err
	}
	if activeTrips > 0 {
		reasons = append(reasons, "You have an active trip in progress")
	}

	var scheduledRides int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM bookings WHERE driver_id=$1 AND status='scheduled'
	`, driverID).Scan(&scheduledRides); err != nil {
		return nil, err
	}
	if scheduledRides > 0 {
		reasons = append(reasons, "You have an upcoming scheduled ride")
	}

	var walletBalance float64
	var isBlocked, isWalletBlocked bool
	var backgroundCheckStatus string
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(wallet_balance,0), COALESCE(is_blocked,false),
		       COALESCE(is_wallet_blocked,false), COALESCE(background_check_status,'')
		FROM drivers WHERE id=$1
	`, driverID).Scan(&walletBalance, &isBlocked, &isWalletBlocked, &backgroundCheckStatus); err != nil {
		return nil, err
	}
	if walletBalance != 0 {
		reasons = append(reasons, fmt.Sprintf(
			"Your wallet balance must be ₹0 before deleting (currently ₹%.2f)", walletBalance,
		))
	}
	if isBlocked {
		reasons = append(reasons, "Your account is currently blocked")
	}
	if isWalletBlocked {
		reasons = append(reasons, "Your wallet is currently blocked")
	}
	if backgroundCheckStatus == "flagged" {
		reasons = append(reasons, "Your background check is flagged for review")
	}

	var openComplaints int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM support_tickets
		WHERE driver_id=$1 AND type='driver_complaint' AND status IN ('open','in_progress')
	`, driverID).Scan(&openComplaints); err != nil {
		return nil, err
	}
	if openComplaints > 0 {
		reasons = append(reasons, "You have an open complaint under review")
	}

	var pendingDisputes int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM support_tickets
		WHERE driver_id=$1 AND refund_requested=true AND refund_status='pending'
	`, driverID).Scan(&pendingDisputes); err != nil {
		return nil, err
	}
	if pendingDisputes > 0 {
		reasons = append(reasons, "You have a pending payment dispute")
	}

	return reasons, nil
}

// verifyDriverPassword resolves user_id -> driver_id and re-checks the
// supplied password against users.password_hash. Returns the driver's id
// on success; writes the response itself and returns ok=false otherwise.
func verifyDriverPassword(c *gin.Context, ctx context.Context, pool *pgxpool.Pool, userID, password string) (driverID string, ok bool) {
	var passwordHash string
	if err := pool.QueryRow(ctx,
		"SELECT password_hash FROM users WHERE id=$1", userID,
	).Scan(&passwordHash); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return "", false
	}

	// 403, not 401 — same reasoning as ChangePassword: the driver-app's
	// shared axios interceptor treats any 401 as an expired session and
	// force-logs-out, which must not happen on a wrong password guess here.
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "current password is incorrect"})
		return "", false
	}

	if err := pool.QueryRow(ctx,
		"SELECT id FROM drivers WHERE user_id=$1", userID,
	).Scan(&driverID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "driver profile not found"})
		return "", false
	}

	return driverID, true
}

// CheckDriverDeletionEligibility is step 1+2 of the two-step deletion flow:
// re-confirm the password, then run every eligibility check as a
// read-only preview. Nothing is deleted here — the driver-app shows the
// returned reasons (if any) before ever offering the final "type DELETE"
// confirmation.
func CheckDriverDeletionEligibility(c *gin.Context) {
	var req driverDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()
	userID := c.GetString("user_id")

	driverID, ok := verifyDriverPassword(c, ctx, pool, userID, req.Password)
	if !ok {
		return
	}

	reasons, err := driverDeletionBlockers(ctx, pool, driverID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check eligibility"})
		return
	}
	if len(reasons) > 0 {
		c.JSON(http.StatusOK, gin.H{"eligible": false, "reasons": reasons})
		return
	}
	c.JSON(http.StatusOK, gin.H{"eligible": true})
}

// DeleteDriverAccount is the actual destructive step. It independently
// re-verifies the password and re-runs every eligibility check — never
// trusting that CheckDriverDeletionEligibility having passed a moment ago
// still holds, since a ride could have been accepted or a complaint
// opened in between.
//
// PII (name/email/phone/profile photo) is scrubbed immediately, same
// pattern as DeleteRiderAccount. driver_documents (KYC), bank/UPI details,
// and earnings/ledger rows are deliberately left untouched — Motor
// Vehicles Act / RTO document retention and the 3-year GST window already
// promised in the privacy policy. Location history purges 30 days later,
// see purgeDueDriverAccounts in account_purge.go.
//
// Ban-evasion hash: only fires when drivers.is_blocked=true at the moment
// of deletion. In practice, a banned driver can never reach this far
// through this endpoint — driverDeletionBlockers already rejects
// is_blocked=true drivers before the transaction below runs, the same
// eligibility gate this function re-checks. This branch is intentionally
// kept for a future admin-triggered deletion path that would call the
// same deletion logic while bypassing the self-service eligibility gate;
// today it is unreachable via the driver-app.
func DeleteDriverAccount(c *gin.Context) {
	var req driverDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()
	userID := c.GetString("user_id")

	driverID, ok := verifyDriverPassword(c, ctx, pool, userID, req.Password)
	if !ok {
		return
	}

	reasons, err := driverDeletionBlockers(ctx, pool, driverID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check eligibility"})
		return
	}
	if len(reasons) > 0 {
		c.JSON(http.StatusConflict, gin.H{"eligible": false, "reasons": reasons})
		return
	}

	var isBlocked bool
	if err := pool.QueryRow(ctx,
		"SELECT COALESCE(is_blocked,false) FROM drivers WHERE id=$1", driverID,
	).Scan(&isBlocked); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete account"})
		return
	}

	// The driver's Aadhaar number lives in driver_documents.doc_number
	// (uploaded during registration) — drivers.aadhaar_number is never
	// populated by any current signup/upload path, so it is not used here.
	var aadhaarNumber string
	_ = pool.QueryRow(ctx, `
		SELECT doc_number FROM driver_documents
		WHERE driver_id=$1 AND doc_type IN ('aadhaar','aadhaar_front') AND doc_number IS NOT NULL
		LIMIT 1
	`, driverID).Scan(&aadhaarNumber)

	tx, err := pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete account"})
		return
	}
	defer tx.Rollback(ctx)

	anonymizedEmail := fmt.Sprintf("deleted-%s@bogie.in", userID)
	if _, err := tx.Exec(ctx,
		`UPDATE users SET name='Deleted User', email=$2, is_active=false, deleted_at=NOW(), updated_at=NOW() WHERE id=$1`,
		userID, anonymizedEmail,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete account"})
		return
	}
	// phone/profile_photo_url scrubbed; vehicle/bank/UPI/earnings columns
	// on drivers are left untouched per the retention requirements above.
	if _, err := tx.Exec(ctx,
		`UPDATE drivers SET phone=NULL, profile_photo_url=NULL, updated_at=NOW() WHERE id=$1`,
		driverID,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete account"})
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM device_tokens WHERE user_id=$1`, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete account"})
		return
	}

	if isBlocked && aadhaarNumber != "" {
		if _, err := tx.Exec(ctx,
			`INSERT INTO driver_aadhaar_hashes (aadhaar_hash, is_banned) VALUES ($1, true)`,
			hashAadhaar(aadhaarNumber),
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete account"})
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete account"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "account deleted"})
}
