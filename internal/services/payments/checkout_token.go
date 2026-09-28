// checkout_token.go signs the short-lived token that authorizes GET
// /gogoo/wallet/topup/checkout — that route is opened in an external mobile
// browser (Linking.openURL from the app), which never carries the rider's
// JWT, so the token itself is the only thing standing between "anyone who
// guesses this URL" and rendering someone else's checkout page. It is
// deliberately domain-separated from the JWT secret (a different env var,
// WALLET_CHECKOUT_SECRET) so a leak of one never compromises the other.
package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// checkoutSecret returns WALLET_CHECKOUT_SECRET, read fresh on every call
// (same convention as RAZORPAY_KEY_ID/SECRET) rather than cached at boot.
func checkoutSecret() string {
	return strings.TrimSpace(os.Getenv("WALLET_CHECKOUT_SECRET"))
}

// GenerateCheckoutToken signs (orderID, riderID, amountPaise) with an
// expiry, so the checkout page and its amount can be reconstructed from the
// token alone — no DB round trip, and the amount rendered on the page is
// exactly the one CreateWalletTopupOrder already validated/clamped and used
// to create the Razorpay order, never anything re-derived from the URL.
// Returns an error if WALLET_CHECKOUT_SECRET is unset — callers must treat
// that as "checkout not configured", the same way a nil RazorpayClient is
// treated.
func GenerateCheckoutToken(orderID, riderID string, amountPaise int64, ttl time.Duration) (string, error) {
	secret := checkoutSecret()
	if secret == "" {
		return "", fmt.Errorf("WALLET_CHECKOUT_SECRET not configured")
	}
	if orderID == "" || riderID == "" || amountPaise <= 0 {
		return "", fmt.Errorf("invalid token payload")
	}
	exp := time.Now().Add(ttl).Unix()
	payload := fmt.Sprintf("%s|%s|%d|%d", orderID, riderID, amountPaise, exp)
	return signPayload(secret, payload), nil
}

// VerifyCheckoutToken checks the signature (constant-time) and expiry, and
// returns the embedded fields only when both hold. A tampered payload, a
// signature that doesn't match, or an expired token all fail the same way —
// callers don't get to distinguish "expired" from "forged" from the return
// value alone, which is deliberate (nothing about *why* a token is invalid
// is useful to whoever is holding an invalid one).
func VerifyCheckoutToken(token string) (orderID, riderID string, amountPaise int64, ok bool) {
	secret := checkoutSecret()
	if secret == "" {
		return "", "", 0, false
	}
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", "", 0, false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", "", 0, false
	}
	payload := string(payloadBytes)

	// signPayload re-derives the same base64(payload)+"."+hmac string
	// deterministically from the decoded payload — comparing the whole
	// reconstructed token against the one presented (constant-time) is both
	// the signature check and the "wasn't otherwise mangled" check in one.
	expected := signPayload(secret, payload)
	if !hmac.Equal([]byte(token), []byte(expected)) {
		return "", "", 0, false
	}

	fields := strings.Split(payload, "|")
	if len(fields) != 4 {
		return "", "", 0, false
	}
	amt, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return "", "", 0, false
	}
	exp, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return "", "", 0, false
	}
	if time.Now().Unix() > exp {
		return "", "", 0, false
	}
	return fields[0], fields[1], amt, true
}

func signPayload(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
}
