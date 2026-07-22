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
	"github.com/deploykit/backend/internal/services/payments"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// rzp is the one instance every wallet handler shares. It is nil whenever
// RAZORPAY_KEY_ID/RAZORPAY_KEY_SECRET are unset — server startup never
// depends on it, handlers check for nil and return 503 instead.
var rzp = payments.NewRazorpayClient()

const (
	walletTopupMin = 50.00
	walletTopupMax = 10000.00
)

func riderIDForUser(ctx context.Context, pool *pgxpool.Pool, userID string) (string, error) {
	var riderID string
	err := pool.QueryRow(ctx, `SELECT id FROM riders WHERE user_id=$1`, userID).Scan(&riderID)
	return riderID, err
}

// POST /gogoo/wallet/topup/create-order
func CreateWalletTopupOrder(c *gin.Context) {
	if rzp == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "payments not yet configured"})
		return
	}

	var req struct {
		Amount float64 `json:"amount"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.Amount < walletTopupMin || req.Amount > walletTopupMax {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("amount must be between ₹%.0f and ₹%.0f", walletTopupMin, walletTopupMax)})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()
	riderID, err := riderIDForUser(ctx, pool, c.GetString("user_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "rider profile not found"})
		return
	}

	// rider_id + purpose travel in Razorpay's "notes" and are echoed back
	// verbatim on the webhook payload — the webhook trusts nothing else
	// about who a payment belongs to.
	receipt := fmt.Sprintf("topup-%s-%d", riderID, time.Now().UnixNano())
	amountPaise := int64(math.Round(req.Amount * 100))
	orderID, err := rzp.CreateOrder(ctx, amountPaise, receipt, map[string]string{
		"rider_id": riderID,
		"purpose":  "wallet_topup",
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

// POST /gogoo/wallet/topup/webhook — public (no JWT: this is Razorpay
// calling us, not the app). Nothing in the payload is trusted until the
// signature verifies. This is the ONLY path that ever credits a top-up —
// client-side "payment success" callbacks are never trusted.
func WalletTopupWebhook(c *gin.Context) {
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

	// Ack (200) anything that isn't a captured wallet-topup payment so
	// Razorpay doesn't keep retrying an event we deliberately ignore.
	entity := payload.Payload.Payment.Entity
	if payload.Event != "payment.captured" || entity.Notes["purpose"] != "wallet_topup" {
		c.JSON(http.StatusOK, gin.H{"status": "ignored"})
		return
	}
	paymentID := entity.ID
	riderID := entity.Notes["rider_id"]
	amount := float64(entity.Amount) / 100.0
	if paymentID == "" || riderID == "" || amount <= 0 {
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
	if err := tx.QueryRow(ctx, `SELECT COALESCE(wallet_balance,0) FROM riders WHERE id=$1 FOR UPDATE`, riderID).Scan(&balance); err != nil {
		// Rider gone — nothing useful to retry towards.
		c.JSON(http.StatusOK, gin.H{"status": "rider not found"})
		return
	}
	newBalance := balance + amount

	_, err = tx.Exec(ctx, `
        INSERT INTO wallet_ledger (id, rider_id, type, amount, balance_after, razorpay_payment_id, status)
        VALUES ($1, $2, 'topup', $3, $4, $5, 'completed')
    `, uuid.New(), riderID, amount, newBalance, paymentID)
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
	if _, err := tx.Exec(ctx, `UPDATE riders SET wallet_balance=$1 WHERE id=$2`, newBalance, riderID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "credited"})
}

// GET /gogoo/wallet/ledger — JWT-scoped balance + transaction history.
func GetWalletLedger(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	riderID, err := riderIDForUser(ctx, pool, c.GetString("user_id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "rider profile not found"})
		return
	}

	rows, err := pool.Query(ctx, `
        SELECT id, type, amount, balance_after, razorpay_payment_id, booking_id, status, created_at
        FROM wallet_ledger WHERE rider_id=$1 ORDER BY created_at DESC LIMIT 200
    `, riderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	out := []gin.H{}
	for rows.Next() {
		var id, typ, status string
		var amount, balanceAfter float64
		var razorpayPaymentID, bookingID *string
		var createdAt time.Time
		if rows.Scan(&id, &typ, &amount, &balanceAfter, &razorpayPaymentID, &bookingID, &status, &createdAt) != nil {
			continue
		}
		out = append(out, gin.H{
			"id": id, "type": typ, "amount": amount, "balance_after": balanceAfter,
			"razorpay_payment_id": razorpayPaymentID, "booking_id": bookingID,
			"status": status, "created_at": createdAt,
		})
	}

	var balance float64
	pool.QueryRow(ctx, `SELECT COALESCE(wallet_balance,0) FROM riders WHERE id=$1`, riderID).Scan(&balance)

	c.JSON(http.StatusOK, gin.H{
		"balance":            balance,
		"payments_available": rzp != nil,
		"ledger":             out,
	})
}

// debitWalletForRide atomically checks-and-debits a rider's wallet for a
// completed ride's fare. Returns false (no-op, nothing written) on
// insufficient balance so the caller can fall back to cash — this never
// partially debits.
func debitWalletForRide(ctx context.Context, pool *pgxpool.Pool, riderID, bookingID string, fare float64) bool {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false
	}
	defer tx.Rollback(ctx)

	var balance float64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(wallet_balance,0) FROM riders WHERE id=$1 FOR UPDATE`, riderID).Scan(&balance); err != nil {
		return false
	}
	if balance < fare {
		return false
	}
	newBalance := balance - fare

	if _, err := tx.Exec(ctx, `
        INSERT INTO wallet_ledger (id, rider_id, type, amount, balance_after, booking_id, status)
        VALUES ($1, $2, 'ride_payment', $3, $4, $5, 'completed')
    `, uuid.New(), riderID, -fare, newBalance, bookingID); err != nil {
		return false
	}
	if _, err := tx.Exec(ctx, `UPDATE riders SET wallet_balance=$1 WHERE id=$2`, newBalance, riderID); err != nil {
		return false
	}
	return tx.Commit(ctx) == nil
}

// refundWalletToRider credits amount back to a rider's wallet as a
// 'refund' ledger row tied to bookingID — same ledger-as-source-of-truth
// pattern as debitWalletForRide, just the other direction.
func refundWalletToRider(ctx context.Context, pool *pgxpool.Pool, riderID, bookingID string, amount float64) bool {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false
	}
	defer tx.Rollback(ctx)

	var balance float64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(wallet_balance,0) FROM riders WHERE id=$1 FOR UPDATE`, riderID).Scan(&balance); err != nil {
		return false
	}
	newBalance := balance + amount

	if _, err := tx.Exec(ctx, `
        INSERT INTO wallet_ledger (id, rider_id, type, amount, balance_after, booking_id, status)
        VALUES ($1, $2, 'refund', $3, $4, $5, 'completed')
    `, uuid.New(), riderID, amount, newBalance, bookingID); err != nil {
		return false
	}
	if _, err := tx.Exec(ctx, `UPDATE riders SET wallet_balance=$1 WHERE id=$2`, newBalance, riderID); err != nil {
		return false
	}
	return tx.Commit(ctx) == nil
}
