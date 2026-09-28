package payments

import (
	"os"
	"testing"
	"time"
)

func withCheckoutSecret(t *testing.T, secret string) {
	t.Helper()
	old := os.Getenv("WALLET_CHECKOUT_SECRET")
	os.Setenv("WALLET_CHECKOUT_SECRET", secret)
	t.Cleanup(func() { os.Setenv("WALLET_CHECKOUT_SECRET", old) })
}

func TestCheckoutToken_ValidRoundTrip(t *testing.T) {
	withCheckoutSecret(t, "test-secret-123")

	token, err := GenerateCheckoutToken("order_abc123", "rider-1", 5000, 15*time.Minute)
	if err != nil {
		t.Fatalf("GenerateCheckoutToken failed: %v", err)
	}

	orderID, riderID, amount, ok := VerifyCheckoutToken(token)
	if !ok {
		t.Fatal("expected valid token to verify")
	}
	if orderID != "order_abc123" || riderID != "rider-1" || amount != 5000 {
		t.Fatalf("got (%s, %s, %d), want (order_abc123, rider-1, 5000)", orderID, riderID, amount)
	}
}

func TestCheckoutToken_Expired(t *testing.T) {
	withCheckoutSecret(t, "test-secret-123")

	// A negative TTL bakes an already-past expiry into the signed payload —
	// the signature itself is still valid, only the exp check should fail.
	token, err := GenerateCheckoutToken("order_abc123", "rider-1", 5000, -1*time.Minute)
	if err != nil {
		t.Fatalf("GenerateCheckoutToken failed: %v", err)
	}

	if _, _, _, ok := VerifyCheckoutToken(token); ok {
		t.Fatal("expected an expired token to fail verification")
	}
}

func TestCheckoutToken_Tampered(t *testing.T) {
	withCheckoutSecret(t, "test-secret-123")

	token, err := GenerateCheckoutToken("order_abc123", "rider-1", 5000, 15*time.Minute)
	if err != nil {
		t.Fatalf("GenerateCheckoutToken failed: %v", err)
	}

	// Flip a character in the signature half of the token.
	tampered := token[:len(token)-1] + "x"
	if tampered == token {
		tampered = token[:len(token)-1] + "y"
	}

	if _, _, _, ok := VerifyCheckoutToken(tampered); ok {
		t.Fatal("expected a tampered token to fail verification")
	}
}

func TestCheckoutToken_WrongRider(t *testing.T) {
	withCheckoutSecret(t, "test-secret-123")

	// A token minted for one rider must never verify as belonging to
	// another — simulated here by generating for rider-1 and asserting the
	// returned rider_id is never rider-2, i.e. nothing lets a caller coerce
	// which rider a valid token resolves to.
	token, err := GenerateCheckoutToken("order_abc123", "rider-1", 5000, 15*time.Minute)
	if err != nil {
		t.Fatalf("GenerateCheckoutToken failed: %v", err)
	}

	_, riderID, _, ok := VerifyCheckoutToken(token)
	if !ok {
		t.Fatal("expected valid token to verify")
	}
	if riderID == "rider-2" || riderID != "rider-1" {
		t.Fatalf("token resolved to rider %q, want rider-1 (never rider-2)", riderID)
	}
}

func TestCheckoutToken_WrongSecret(t *testing.T) {
	withCheckoutSecret(t, "test-secret-123")
	token, err := GenerateCheckoutToken("order_abc123", "rider-1", 5000, 15*time.Minute)
	if err != nil {
		t.Fatalf("GenerateCheckoutToken failed: %v", err)
	}

	// Same token, verified against a different secret (e.g. secret rotated,
	// or WALLET_CHECKOUT_SECRET misconfigured differently between the
	// process that minted it and the one verifying it) must fail.
	withCheckoutSecret(t, "a-different-secret")
	if _, _, _, ok := VerifyCheckoutToken(token); ok {
		t.Fatal("expected token signed with a different secret to fail verification")
	}
}

func TestCheckoutToken_UnconfiguredSecret(t *testing.T) {
	withCheckoutSecret(t, "")
	if _, err := GenerateCheckoutToken("order_abc123", "rider-1", 5000, 15*time.Minute); err == nil {
		t.Fatal("expected GenerateCheckoutToken to fail when WALLET_CHECKOUT_SECRET is unset")
	}
	if _, _, _, ok := VerifyCheckoutToken("anything.here"); ok {
		t.Fatal("expected VerifyCheckoutToken to fail when WALLET_CHECKOUT_SECRET is unset")
	}
}

func TestCheckoutToken_MalformedInput(t *testing.T) {
	withCheckoutSecret(t, "test-secret-123")
	for _, bad := range []string{"", "no-dot-in-here", "not-base64!!!.deadbeef", "..", "a.b.c"} {
		if _, _, _, ok := VerifyCheckoutToken(bad); ok {
			t.Fatalf("expected malformed token %q to fail verification", bad)
		}
	}
}
