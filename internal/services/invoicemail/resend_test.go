package invoicemail

import (
	"testing"
	"time"
)

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"anjali@gmail.com": "a•••@gmail.com",
		"a@b.co":           "a•••@b.co",
		"":                 "•••",
		"@nolocal.com":     "•••",
	}
	for in, want := range cases {
		if got := maskEmail(in); got != want {
			t.Errorf("maskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResendIdempotencyKeyDistinct(t *testing.T) {
	id := "3f2a9c1e-0000-4000-8000-000000000000"
	t0 := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	k1 := resendIdempotencyKey(id, t0)
	k2 := resendIdempotencyKey(id, t0.Add(time.Microsecond))
	if k1 == k2 {
		t.Fatalf("resend keys collide: %q", k1)
	}
	if k1 == idempotencyKey(id) {
		t.Fatalf("resend key reuses the automatic send's key: %q", k1)
	}
	if len(k1) > 256 {
		t.Fatalf("key too long for Resend: %d", len(k1))
	}
}
