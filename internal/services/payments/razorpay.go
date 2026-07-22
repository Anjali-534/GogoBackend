// Package payments is the swappable payment-gateway seam for Bogie Wallet.
// RazorpayClient is the only thing handlers ever talk to — when real keys
// arrive, NewRazorpayClient starts returning a working client automatically
// (env vars set), with zero code changes anywhere else.
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
)

// RazorpayClient is the seam: every wallet handler depends on this
// interface, never on Razorpay's SDK/HTTP API directly.
type RazorpayClient interface {
	// CreateOrder creates a Razorpay order for amountPaise (INR paise) and
	// returns its order_id. notes are echoed back verbatim on the webhook
	// payload, which is how the webhook learns which rider/purpose an order
	// was for without needing a pre-inserted DB row to match against.
	CreateOrder(ctx context.Context, amountPaise int64, receipt string, notes map[string]string) (orderID string, err error)

	// VerifyWebhookSignature checks the X-Razorpay-Signature header against
	// the raw request body. Nothing from a webhook payload is trusted until
	// this returns true.
	VerifyWebhookSignature(body []byte, signature string) bool
}

type razorpayClient struct {
	keyID         string
	keySecret     string
	webhookSecret string
	httpClient    *http.Client
}

// NewRazorpayClient returns nil when RAZORPAY_KEY_ID/RAZORPAY_KEY_SECRET are
// unset or empty — the deliberate "keys absent" state this app runs in
// until real keys are issued. Callers must check for a nil client and
// return a 503 rather than crash; server startup never depends on this.
func NewRazorpayClient() RazorpayClient {
	keyID := strings.TrimSpace(os.Getenv("RAZORPAY_KEY_ID"))
	keySecret := strings.TrimSpace(os.Getenv("RAZORPAY_KEY_SECRET"))
	if keyID == "" || keySecret == "" {
		return nil
	}
	// RAZORPAY_WEBHOOK_SECRET is the secret configured against the webhook
	// URL in the Razorpay dashboard (distinct from the API key secret).
	// Falls back to the key secret so a minimal single-secret setup still
	// works, but a dedicated webhook secret is the correct production config.
	webhookSecret := strings.TrimSpace(os.Getenv("RAZORPAY_WEBHOOK_SECRET"))
	if webhookSecret == "" {
		webhookSecret = keySecret
	}
	return &razorpayClient{
		keyID:         keyID,
		keySecret:     keySecret,
		webhookSecret: webhookSecret,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (r *razorpayClient) CreateOrder(ctx context.Context, amountPaise int64, receipt string, notes map[string]string) (string, error) {
	body, err := json.Marshal(map[string]interface{}{
		"amount":   amountPaise,
		"currency": "INR",
		"receipt":  receipt,
		"notes":    notes,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.razorpay.com/v1/orders", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(r.keyID, r.keySecret)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var out struct {
		ID    string `json:"id"`
		Error *struct {
			Description string `json:"description"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 || out.ID == "" {
		if out.Error != nil && out.Error.Description != "" {
			return "", fmt.Errorf("razorpay: %s", out.Error.Description)
		}
		return "", fmt.Errorf("razorpay: order creation failed (status %d)", resp.StatusCode)
	}
	return out.ID, nil
}

func (r *razorpayClient) VerifyWebhookSignature(body []byte, signature string) bool {
	if signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(r.webhookSecret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}
