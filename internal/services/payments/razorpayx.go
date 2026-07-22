// PayoutClient is the swappable seam for driver withdrawals, the RazorpayX
// counterpart to RazorpayClient (which only ever handles incoming Payments).
// NewRazorpayXClient starts returning a working client the moment real
// RazorpayX keys land, with zero code changes anywhere else.
package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

// PayoutClient is the seam: driver withdrawal handlers depend on this
// interface, never on RazorpayX's HTTP API directly.
type PayoutClient interface {
	// CreateFundAccount registers a driver's bank account or UPI VPA as a
	// RazorpayX fund account (creating the underlying Contact first) and
	// returns the fund_account_id a payout is created against.
	CreateFundAccount(ctx context.Context, driverID, name string, bank *BankAccount, upiVPA string) (fundAccountID string, err error)

	// CreatePayout sends amountPaise to fundAccountID and returns RazorpayX's
	// payout_id, which the withdraw handler stores on the pending ledger row.
	CreatePayout(ctx context.Context, fundAccountID string, amountPaise int64, purpose, referenceID string) (payoutID string, err error)

	// VerifyWebhookSignature checks the X-Razorpay-Signature header against
	// the raw request body of a payout webhook. Same HMAC scheme as regular
	// Razorpay webhooks, verified against RAZORPAYX_WEBHOOK_SECRET.
	VerifyWebhookSignature(body []byte, signature string) bool
}

// BankAccount is the minimal bank-transfer fund-account shape RazorpayX
// requires; UPI withdrawals pass upiVPA to CreateFundAccount instead.
type BankAccount struct {
	AccountHolder string
	AccountNumber string
	IFSC          string
}

type razorpayXClient struct {
	keyID          string
	keySecret      string
	webhookSecret  string
	accountNumber  string // the business's RazorpayX account payouts are debited from
	httpClient     *http.Client
}

// NewRazorpayXClient returns nil when RAZORPAYX_KEY_ID/RAZORPAYX_KEY_SECRET/
// RAZORPAYX_ACCOUNT_NUMBER are unset or empty — the deliberate "payouts not
// yet configured" state this app runs in until a RazorpayX business account
// is approved. Callers must check for a nil client and return 503 rather
// than crash; server startup never depends on this.
func NewRazorpayXClient() PayoutClient {
	keyID := strings.TrimSpace(os.Getenv("RAZORPAYX_KEY_ID"))
	keySecret := strings.TrimSpace(os.Getenv("RAZORPAYX_KEY_SECRET"))
	accountNumber := strings.TrimSpace(os.Getenv("RAZORPAYX_ACCOUNT_NUMBER"))
	if keyID == "" || keySecret == "" || accountNumber == "" {
		return nil
	}
	webhookSecret := strings.TrimSpace(os.Getenv("RAZORPAYX_WEBHOOK_SECRET"))
	if webhookSecret == "" {
		webhookSecret = keySecret
	}
	return &razorpayXClient{
		keyID:         keyID,
		keySecret:     keySecret,
		webhookSecret: webhookSecret,
		accountNumber: accountNumber,
		httpClient:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (r *razorpayXClient) doJSON(ctx context.Context, method, url string, body interface{}, idempotencyKey string, out interface{}) error {
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	req.SetBasicAuth(r.keyID, r.keySecret)
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		// Mandatory since March 2025 on RazorpayX payout creation — prevents a
		// retried request (network timeout, etc.) from firing a duplicate payout.
		req.Header.Set("X-Payout-Idempotency", idempotencyKey)
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var errBody struct {
			Error *struct {
				Description string `json:"description"`
			} `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&errBody)
		if errBody.Error != nil && errBody.Error.Description != "" {
			return fmt.Errorf("razorpayx: %s", errBody.Error.Description)
		}
		return fmt.Errorf("razorpayx: request failed (status %d)", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (r *razorpayXClient) CreateFundAccount(ctx context.Context, driverID, name string, bank *BankAccount, upiVPA string) (string, error) {
	var contact struct {
		ID string `json:"id"`
	}
	err := r.doJSON(ctx, http.MethodPost, "https://api.razorpay.com/v1/contacts", map[string]interface{}{
		"name":           name,
		"type":           "vendor",
		"reference_id":   driverID,
	}, "", &contact)
	if err != nil {
		return "", fmt.Errorf("create contact: %w", err)
	}

	fundAccountReq := map[string]interface{}{
		"contact_id": contact.ID,
	}
	if bank != nil {
		fundAccountReq["account_type"] = "bank_account"
		fundAccountReq["bank_account"] = map[string]string{
			"name":           bank.AccountHolder,
			"ifsc":           bank.IFSC,
			"account_number": bank.AccountNumber,
		}
	} else {
		fundAccountReq["account_type"] = "vpa"
		fundAccountReq["vpa"] = map[string]string{"address": upiVPA}
	}

	var fundAccount struct {
		ID string `json:"id"`
	}
	if err := r.doJSON(ctx, http.MethodPost, "https://api.razorpay.com/v1/fund_accounts", fundAccountReq, "", &fundAccount); err != nil {
		return "", fmt.Errorf("create fund account: %w", err)
	}
	return fundAccount.ID, nil
}

func (r *razorpayXClient) CreatePayout(ctx context.Context, fundAccountID string, amountPaise int64, purpose, referenceID string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := r.doJSON(ctx, http.MethodPost, "https://api.razorpay.com/v1/payouts", map[string]interface{}{
		"account_number":  r.accountNumber,
		"fund_account_id": fundAccountID,
		"amount":          amountPaise,
		"currency":        "INR",
		"mode":            "IMPS",
		"purpose":         purpose,
		"queue_if_low_balance": true,
		"reference_id":    referenceID,
	}, uuid.New().String(), &out)
	if err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("razorpayx: payout creation returned no id")
	}
	return out.ID, nil
}

func (r *razorpayXClient) VerifyWebhookSignature(body []byte, signature string) bool {
	if signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(r.webhookSecret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}
