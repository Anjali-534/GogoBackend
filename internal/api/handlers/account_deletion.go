package handlers

import (
	"context"
	"fmt"
	"net/http"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
)

// DeleteRiderAccount soft-deletes the calling rider's own account — user_id
// comes from the JWT (set by AuthMiddleware), never a request param, so a
// rider can only ever delete themselves.
//
// PII is scrubbed immediately: name, email, phone, profile photo, saved
// addresses, and push-notification tokens. Booking/payment rows are left
// untouched — GST requires those retained for 3 years, as already promised
// in the privacy policy — they only reference rider_id, so no PII lives in
// them directly. users.is_active is flipped to false and deleted_at is
// stamped, which AuthMiddleware checks on every subsequent request, so any
// token already issued to this user stops working immediately even though
// nothing here can reach into a client's AsyncStorage to erase it.
//
// A non-zero wallet balance blocks deletion outright — real money is never
// silently zeroed out. The 30-day hard-purge of location history runs
// separately (see StartAccountPurge).
func DeleteRiderAccount(c *gin.Context) {
	userID := c.GetString("user_id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var riderID string
	var walletBalance float64
	err := pool.QueryRow(ctx,
		`SELECT id, COALESCE(wallet_balance,0) FROM riders WHERE user_id=$1`, userID,
	).Scan(&riderID, &walletBalance)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "rider profile not found"})
		return
	}

	if walletBalance > 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error": fmt.Sprintf("you have a wallet balance of ₹%.2f — please withdraw or use it before deleting your account", walletBalance),
		})
		return
	}
	if walletBalance < 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error": fmt.Sprintf("you have an outstanding balance of ₹%.2f — please clear it before deleting your account", -walletBalance),
		})
		return
	}

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
	if _, err := tx.Exec(ctx,
		`UPDATE riders SET phone=NULL, profile_photo_url=NULL, saved_addresses='[]'::jsonb, updated_at=NOW() WHERE user_id=$1`,
		userID,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete account"})
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM device_tokens WHERE user_id=$1`, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete account"})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete account"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "account deleted"})
}
