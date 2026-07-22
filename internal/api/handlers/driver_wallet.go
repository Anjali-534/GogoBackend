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

// payoutClient is the one instance every driver-withdrawal handler shares.
// It is nil whenever RAZORPAYX_KEY_ID/SECRET/ACCOUNT_NUMBER are unset —
// server startup never depends on it, handlers check for nil and return 503
// instead ("payouts not yet configured").
var payoutClient = payments.NewRazorpayXClient()

const (
	driverTopupMin    = 50.00
	driverTopupMax    = 10000.00
	driverWithdrawMin = 100.00
	driverWalletFloor = 500.00 // same reserve GetDriverWallet already enforces
)

func driverIDForUser(ctx context.Context, pool *pgxpool.Pool, userID string) (string, error) {
	var driverID string
	err := pool.QueryRow(ctx, `SELECT id FROM drivers WHERE user_id=$1`, userID).Scan(&driverID)
	return driverID, err
}

// POST /gogoo/driver/wallet/topup/create-order — driver funding their own
// wallet (e.g. to pay down a negative registration-fee balance). Regular
// Razorpay Payments, same gateway the rider top-up already uses.
func CreateDriverWalletTopupOrder(c *gin.Context) {
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
	if req.Amount < driverTopupMin || req.Amount > driverTopupMax {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("amount must be between ₹%.0f and ₹%.0f", driverTopupMin, driverTopupMax)})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()
	driverID, err := driverIDForUser(ctx, pool, c.GetString("user_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "driver profile not found"})
		return
	}

	receipt := fmt.Sprintf("driver-topup-%s-%d", driverID, time.Now().UnixNano())
	amountPaise := int64(math.Round(req.Amount * 100))
	orderID, err := rzp.CreateOrder(ctx, amountPaise, receipt, map[string]string{
		"driver_id": driverID,
		"purpose":   "driver_wallet_topup",
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

// POST /gogoo/driver/wallet/topup/webhook — public (Razorpay calling us
// directly, no JWT). Nothing in the payload is trusted until the signature
// verifies. This is the ONLY path that ever credits a driver top-up.
func DriverWalletTopupWebhook(c *gin.Context) {
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
	if payload.Event != "payment.captured" || entity.Notes["purpose"] != "driver_wallet_topup" {
		c.JSON(http.StatusOK, gin.H{"status": "ignored"})
		return
	}
	paymentID := entity.ID
	driverID := entity.Notes["driver_id"]
	amount := float64(entity.Amount) / 100.0
	if paymentID == "" || driverID == "" || amount <= 0 {
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
	if err := tx.QueryRow(ctx, `SELECT COALESCE(wallet_balance,-700.00) FROM drivers WHERE id=$1 FOR UPDATE`, driverID).Scan(&balance); err != nil {
		c.JSON(http.StatusOK, gin.H{"status": "driver not found"})
		return
	}
	newBalance := balance + amount

	_, err = tx.Exec(ctx, `
        INSERT INTO driver_earnings (id, driver_id, amount, type, description, is_debit, status, razorpay_payment_id, created_at)
        VALUES ($1, $2, $3, 'topup', 'Wallet top-up', false, 'completed', $4, NOW())
    `, uuid.New(), driverID, amount, paymentID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Duplicate webhook delivery for a payment already credited — the
			// unique index on razorpay_payment_id is what makes this safe.
			c.JSON(http.StatusOK, gin.H{"status": "already processed"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE drivers SET wallet_balance=$1 WHERE id=$2`, newBalance, driverID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "credited"})
}

// POST /gogoo/driver/wallet/withdraw — the funds are reserved (debited from
// wallet_balance) the instant the request is accepted, in the same
// transaction as the pending ledger row. This is what makes it impossible to
// fire two withdrawals against the same balance before either payout
// completes — the same race the row lock below closes.
func RequestDriverWithdrawal(c *gin.Context) {
	var req struct {
		Amount float64 `json:"amount"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.Amount < driverWithdrawMin {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("minimum withdrawal is ₹%.0f", driverWithdrawMin)})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()
	driverID, err := driverIDForUser(ctx, pool, c.GetString("user_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "driver profile not found"})
		return
	}

	var (
		bankHolder, bankNumber, bankIFSC, upiVPA *string
	)
	if err := pool.QueryRow(ctx, `
        SELECT bank_account_holder, bank_account_number, bank_ifsc, upi_id
        FROM drivers WHERE id=$1
    `, driverID).Scan(&bankHolder, &bankNumber, &bankIFSC, &upiVPA); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	hasBank := bankHolder != nil && *bankHolder != "" && bankNumber != nil && *bankNumber != "" && bankIFSC != nil && *bankIFSC != ""
	hasUPI := upiVPA != nil && *upiVPA != ""
	if !hasBank && !hasUPI {
		c.JSON(http.StatusBadRequest, gin.H{"error": "add your bank account or UPI ID in profile before withdrawing", "missing_payout_details": true})
		return
	}

	if payoutClient == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "payouts not yet configured"})
		return
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer tx.Rollback(ctx)

	var balance float64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(wallet_balance,-700.00) FROM drivers WHERE id=$1 FOR UPDATE`, driverID).Scan(&balance); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	withdrawable := balance - driverWalletFloor
	if req.Amount > withdrawable {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("amount exceeds withdrawable balance of ₹%.2f", math.Max(withdrawable, 0))})
		return
	}
	newBalance := balance - req.Amount

	ledgerID := uuid.New()
	_, err = tx.Exec(ctx, `
        INSERT INTO driver_earnings (id, driver_id, amount, type, description, is_debit, status, created_at)
        VALUES ($1, $2, $3, 'withdrawal', 'Withdrawal to bank/UPI', true, 'pending', NOW())
    `, ledgerID, driverID, req.Amount)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE drivers SET wallet_balance=$1 WHERE id=$2`, newBalance, driverID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	// The external payout call happens after commit, deliberately outside
	// the DB transaction/row lock — an HTTP call has no business holding a
	// lock on the driver's balance row.
	fundAccountID, err := payoutClient.CreateFundAccount(ctx, driverID, driverDisplayName(bankHolder), bankFromFields(hasBank, bankHolder, bankNumber, bankIFSC), stringOrEmpty(upiVPA))
	if err != nil {
		markWithdrawalFailed(ctx, pool, ledgerID.String(), driverID, req.Amount, "failed to register payout destination")
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to initiate payout"})
		return
	}
	payoutID, err := payoutClient.CreatePayout(ctx, fundAccountID, int64(math.Round(req.Amount*100)), "payout", ledgerID.String())
	if err != nil {
		markWithdrawalFailed(ctx, pool, ledgerID.String(), driverID, req.Amount, "payout initiation failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to initiate payout"})
		return
	}
	pool.Exec(ctx, `UPDATE driver_earnings SET razorpayx_payout_id=$1 WHERE id=$2`, payoutID, ledgerID)

	c.JSON(http.StatusOK, gin.H{
		"status":     "pending",
		"ledger_id":  ledgerID,
		"payout_id":  payoutID,
		"amount":     req.Amount,
		"new_balance": newBalance,
	})
}

func driverDisplayName(bankHolder *string) string {
	if bankHolder != nil && *bankHolder != "" {
		return *bankHolder
	}
	return "Driver"
}

func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func bankFromFields(hasBank bool, holder, number, ifsc *string) *payments.BankAccount {
	if !hasBank {
		return nil
	}
	return &payments.BankAccount{
		AccountHolder: *holder,
		AccountNumber: *number,
		IFSC:          *ifsc,
	}
}

// markWithdrawalFailed flips the pending row to 'failed' and inserts a
// separate compensating credit row crediting the amount back — never
// mutating the original row's stored amount/effect, so the ledger's
// sum-equals-balance invariant (which BuildStatement/GetDriverLedger rely
// on) always holds, and the full audit trail (attempt + reversal) stays
// visible.
func markWithdrawalFailed(ctx context.Context, pool *pgxpool.Pool, ledgerID, driverID string, amount float64, reason string) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `UPDATE driver_earnings SET status='failed' WHERE id=$1`, ledgerID); err != nil {
		return
	}
	var balance float64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(wallet_balance,-700.00) FROM drivers WHERE id=$1 FOR UPDATE`, driverID).Scan(&balance); err != nil {
		return
	}
	newBalance := balance + amount
	if _, err := tx.Exec(ctx, `
        INSERT INTO driver_earnings (id, driver_id, amount, type, description, is_debit, status, created_at)
        VALUES ($1, $2, $3, 'withdrawal', $4, false, 'completed', NOW())
    `, uuid.New(), driverID, amount, "Withdrawal reversed — "+reason); err != nil {
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE drivers SET wallet_balance=$1 WHERE id=$2`, newBalance, driverID); err != nil {
		return
	}
	tx.Commit(ctx)
}

// POST /gogoo/driver/wallet/payout-webhook — public (RazorpayX calling us
// directly, no JWT). Verifies signature, then either confirms the pending
// row (balance already applied at request time, no change needed) or runs
// the same two-row reversal as a synchronous payout-creation failure.
func DriverPayoutWebhook(c *gin.Context) {
	if payoutClient == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "payouts not yet configured"})
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if !payoutClient.VerifyWebhookSignature(body, c.GetHeader("X-Razorpay-Signature")) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid signature"})
		return
	}

	var payload struct {
		Event   string `json:"event"`
		Payload struct {
			Payout struct {
				Entity struct {
					ID          string `json:"id"`
					Status      string `json:"status"`
					ReferenceID string `json:"reference_id"`
				} `json:"entity"`
			} `json:"payout"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	entity := payload.Payload.Payout.Entity
	if entity.ID == "" || entity.ReferenceID == "" {
		c.JSON(http.StatusOK, gin.H{"status": "ignored"})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var (
		status, driverID string
		amount           float64
	)
	err = pool.QueryRow(ctx, `
        SELECT status, driver_id, amount FROM driver_earnings WHERE id=$1
    `, entity.ReferenceID).Scan(&status, &driverID, &amount)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"status": "not found"})
		return
	}
	if status != "pending" {
		// Already settled by a previous delivery of this (or a later) webhook
		// event — RazorpayX can fire more than once per payout.
		c.JSON(http.StatusOK, gin.H{"status": "already processed"})
		return
	}

	switch entity.Status {
	case "processed":
		if _, err := pool.Exec(ctx, `UPDATE driver_earnings SET status='completed', razorpayx_payout_id=$1 WHERE id=$2`, entity.ID, entity.ReferenceID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
	case "failed", "rejected", "cancelled", "reversed":
		markWithdrawalFailed(ctx, pool, entity.ReferenceID, driverID, amount, "payout "+entity.Status)
	default:
		// processing/queued — still in flight, nothing to settle yet.
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
