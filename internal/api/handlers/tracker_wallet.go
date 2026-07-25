package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Same rzp instance as the rider wallet (wallet.go) — nil-when-unconfigured,
// every handler below checks and returns 503 rather than assuming keys exist.

const (
	trackerWalletTopupMin = 100.00
	trackerWalletTopupMax = 100000.00
)

// POST /gogoo/tracker/wallet/topup/create-order
func CreateTrackerWalletTopupOrder(c *gin.Context) {
	if rzp == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "payments not yet configured"})
		return
	}

	companyID := c.GetString("company_id")

	var req struct {
		Amount float64 `json:"amount"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.Amount < trackerWalletTopupMin || req.Amount > trackerWalletTopupMax {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("amount must be between ₹%.0f and ₹%.0f", trackerWalletTopupMin, trackerWalletTopupMax)})
		return
	}

	ctx := context.Background()
	// company_id + purpose travel in Razorpay's "notes" and are echoed back
	// verbatim on the webhook payload, same as the rider topup flow.
	receipt := fmt.Sprintf("tracker-topup-%s-%d", companyID, time.Now().UnixNano())
	amountPaise := int64(math.Round(req.Amount * 100))
	orderID, err := rzp.CreateOrder(ctx, amountPaise, receipt, map[string]string{
		"company_id": companyID,
		"purpose":    "tracker_wallet_topup",
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to create payment order"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"order_id": orderID,
		"amount":   req.Amount,
	})
}

// POST /gogoo/tracker/wallet/topup/webhook — public (no JWT: this is
// Razorpay calling us). Nothing in the payload is trusted until the
// signature verifies. Mirrors WalletTopupWebhook exactly, scoped to
// tracker_companies/tracker_wallet_ledger instead of riders/wallet_ledger.
func TrackerWalletTopupWebhook(c *gin.Context) {
	if rzp == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "payments not yet configured"})
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if !rzp.VerifyWebhookSignature(body, c.GetHeader("X-Razorpay-Signature")) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid signature"})
		return
	}

	var payload struct {
		Event   string `json:"event"`
		Payload struct {
			Payment struct {
				Entity struct {
					ID     string            `json:"id"`
					Amount int64             `json:"amount"`
					Notes  map[string]string `json:"notes"`
				} `json:"entity"`
			} `json:"payment"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	entity := payload.Payload.Payment.Entity
	if payload.Event != "payment.captured" || entity.Notes["purpose"] != "tracker_wallet_topup" {
		c.JSON(http.StatusOK, gin.H{"status": "ignored"})
		return
	}
	paymentID := entity.ID
	companyID := entity.Notes["company_id"]
	amount := float64(entity.Amount) / 100.0
	if paymentID == "" || companyID == "" || amount <= 0 {
		c.JSON(http.StatusOK, gin.H{"status": "ignored"})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()
	tx, err := pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer tx.Rollback(ctx)

	var balance float64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(wallet_balance,0) FROM tracker_companies WHERE id=$1 FOR UPDATE`, companyID).Scan(&balance); err != nil {
		c.JSON(http.StatusOK, gin.H{"status": "company not found"})
		return
	}
	newBalance := balance + amount

	_, err = tx.Exec(ctx, `
        INSERT INTO tracker_wallet_ledger (id, company_id, type, amount, balance_after, razorpay_payment_id, status)
        VALUES ($1, $2, 'topup', $3, $4, $5, 'completed')
    `, uuid.New(), companyID, amount, newBalance, paymentID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Duplicate webhook delivery for a payment we already credited —
			// the unique index on razorpay_payment_id is what makes this safe.
			c.JSON(http.StatusOK, gin.H{"status": "already processed"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE tracker_companies SET wallet_balance=$1 WHERE id=$2`, newBalance, companyID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "credited"})
}

// GET /gogoo/tracker/wallet/ledger — company-scoped balance, subscription
// state, and transaction history.
func GetTrackerWalletLedger(c *gin.Context) {
	companyID := c.GetString("company_id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
        SELECT id, type, amount, balance_after, razorpay_payment_id, status, created_at
        FROM tracker_wallet_ledger WHERE company_id=$1 ORDER BY created_at DESC LIMIT 200
    `, companyID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	out := []gin.H{}
	for rows.Next() {
		var id, typ, status string
		var amount, balanceAfter float64
		var razorpayPaymentID *string
		var createdAt time.Time
		if rows.Scan(&id, &typ, &amount, &balanceAfter, &razorpayPaymentID, &status, &createdAt) != nil {
			continue
		}
		out = append(out, gin.H{
			"id": id, "type": typ, "amount": amount, "balance_after": balanceAfter,
			"razorpay_payment_id": razorpayPaymentID, "status": status, "created_at": createdAt,
		})
	}

	var balance float64
	var subscriptionAmount *float64
	var subscriptionStatus string
	var nextBillingDate *time.Time
	pool.QueryRow(ctx, `
        SELECT COALESCE(wallet_balance,0), subscription_amount, subscription_status, next_billing_date
        FROM tracker_companies WHERE id=$1
    `, companyID).Scan(&balance, &subscriptionAmount, &subscriptionStatus, &nextBillingDate)

	c.JSON(http.StatusOK, gin.H{
		"balance":             balance,
		"subscription_amount": subscriptionAmount,
		"subscription_status": subscriptionStatus,
		"next_billing_date":   nextBillingDate,
		"payments_available":  rzp != nil,
		"ledger":              out,
	})
}

// debitCompanyWalletForRide atomically checks-and-debits a tracker
// company's wallet for a completed Book-a-Ride booking's fare — mirrors
// debitWalletForRide (wallet.go) against tracker_companies/
// tracker_wallet_ledger instead of riders/wallet_ledger. Returns false
// (no-op, nothing written) on insufficient balance so the caller can fall
// back to cash, same as the rider path; this never partially debits.
func debitCompanyWalletForRide(ctx context.Context, pool *pgxpool.Pool, companyID string, fare float64) bool {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false
	}
	defer tx.Rollback(ctx)

	var balance float64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(wallet_balance,0) FROM tracker_companies WHERE id=$1 FOR UPDATE`, companyID).Scan(&balance); err != nil {
		return false
	}
	if balance < fare {
		return false
	}
	newBalance := balance - fare

	if _, err := tx.Exec(ctx, `
        INSERT INTO tracker_wallet_ledger (id, company_id, type, amount, balance_after, status)
        VALUES ($1, $2, 'ride_payment', $3, $4, 'completed')
    `, uuid.New(), companyID, -fare, newBalance); err != nil {
		return false
	}
	if _, err := tx.Exec(ctx, `UPDATE tracker_companies SET wallet_balance=$1 WHERE id=$2`, newBalance, companyID); err != nil {
		return false
	}
	return tx.Commit(ctx) == nil
}
