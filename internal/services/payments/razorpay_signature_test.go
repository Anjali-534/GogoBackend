package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// sign mirrors razorpayClient.VerifyPaymentSignature's own formula, so the
// test can construct a genuinely valid signature without reaching into the
// unexported client to do it.
func signOrderPayment(secret, orderID, paymentID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(orderID + "|" + paymentID))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyPaymentSignature(t *testing.T) {
	t.Setenv("RAZORPAY_KEY_ID", "test_key_id")
	t.Setenv("RAZORPAY_KEY_SECRET", "test_key_secret")
	client := NewRazorpayClient()
	if client == nil {
		t.Fatal("expected a non-nil client with test keys set")
	}

	validSig := signOrderPayment("test_key_secret", "order_abc", "pay_xyz")

	cases := []struct {
		name      string
		orderID   string
		paymentID string
		signature string
		want      bool
	}{
		{"valid", "order_abc", "pay_xyz", validSig, true},
		{"tampered signature", "order_abc", "pay_xyz", validSig[:len(validSig)-1] + "0", false},
		{"wrong order id", "order_other", "pay_xyz", validSig, false},
		{"wrong payment id", "order_abc", "pay_other", validSig, false},
		{"empty signature", "order_abc", "pay_xyz", "", false},
		{"empty order id", "", "pay_xyz", validSig, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := client.VerifyPaymentSignature(tc.orderID, tc.paymentID, tc.signature)
			if got != tc.want {
				t.Errorf("VerifyPaymentSignature(%q, %q, ...) = %v, want %v", tc.orderID, tc.paymentID, got, tc.want)
			}
		})
	}
}

func TestVerifyPaymentSignature_DifferentFromWebhookSignature(t *testing.T) {
	t.Setenv("RAZORPAY_KEY_ID", "test_key_id")
	t.Setenv("RAZORPAY_KEY_SECRET", "test_key_secret")
	t.Setenv("RAZORPAY_WEBHOOK_SECRET", "test_webhook_secret")
	client := NewRazorpayClient()
	if client == nil {
		t.Fatal("expected a non-nil client with test keys set")
	}

	// A signature computed the webhook way (HMAC of a raw body with the
	// webhook secret) must never validate as a payment-verification
	// signature (HMAC of "order|payment" with the key secret) — the two
	// formulas and secrets are deliberately not interchangeable.
	webhookStyleSig := signOrderPayment("test_webhook_secret", "order_abc", "pay_xyz")
	if client.VerifyPaymentSignature("order_abc", "pay_xyz", webhookStyleSig) {
		t.Fatal("a webhook-secret-signed value must not pass payment-signature verification")
	}
}
