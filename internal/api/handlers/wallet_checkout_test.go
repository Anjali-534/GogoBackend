package handlers

import (
	"bytes"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRiderOnlinePaymentsEnabled(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"false", false},
		{"0", false},
		{"no", false},
		{"true", true},
		{"1", true},
		{"  true  ", true},
	}
	for _, tc := range cases {
		old := os.Getenv("RIDER_ONLINE_PAYMENTS_ENABLED")
		os.Setenv("RIDER_ONLINE_PAYMENTS_ENABLED", tc.val)
		got := riderOnlinePaymentsEnabled()
		os.Setenv("RIDER_ONLINE_PAYMENTS_ENABLED", old)
		if got != tc.want {
			t.Errorf("riderOnlinePaymentsEnabled() with env=%q = %v, want %v", tc.val, got, tc.want)
		}
	}
}

func TestRazorpayOrderIDPattern(t *testing.T) {
	valid := []string{"order_9A33XWu170gUtm", "order_ABC123", "order_a"}
	invalid := []string{
		"", "order_", "ORDER_abc", "order_abc def", "order_abc;drop table",
		"order_abc\n", "order_../../etc/passwd", "pay_abc123", "order_abc'--",
	}
	for _, v := range valid {
		if !razorpayOrderIDPattern.MatchString(v) {
			t.Errorf("expected %q to match order-id pattern", v)
		}
	}
	for _, v := range invalid {
		if razorpayOrderIDPattern.MatchString(v) {
			t.Errorf("expected %q to NOT match order-id pattern", v)
		}
	}
}

// TestCheckoutPageTemplate_EscapesUntrustedValues renders the checkout page
// template with a deliberately hostile rider name (the one field a rider
// controls that ends up in the prefill) and confirms the rendered HTML
// contains no raw "</script>" breakout and no unescaped quote that could
// terminate the JS string/object literal early. json.Marshal's default
// HTML-escaping (of '<', '>', '&') is what's supposed to make this safe —
// this test is what actually proves it, rather than trusting the doc comment.
func TestCheckoutPageTemplate_EscapesUntrustedValues(t *testing.T) {
	hostileName := `</script><script>alert(document.cookie)</script>`
	opts := checkoutOptions{
		Key:         "rzp_test_abc",
		Amount:      5000,
		Currency:    "INR",
		Name:        "Bogie",
		OrderID:     "order_abc123",
		CallbackURL: "https://example.com/return",
		Redirect:    true,
		Prefill:     map[string]string{"name": hostileName},
		Theme:       map[string]string{"color": "#FF6B2B"},
	}
	optionsJSON, err := json.Marshal(opts)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if bytes.Contains(optionsJSON, []byte("</script>")) {
		t.Fatalf("json.Marshal output contains a raw </script>: %s", optionsJSON)
	}

	cancelURL := "gogoo://wallet-topup-return?status=cancelled&order_id=order_abc123"
	var buf bytes.Buffer
	if err := checkoutTmpl.Execute(&buf, map[string]interface{}{
		"OptionsJSON": template.JS(optionsJSON),
		"CancelURL":   template.URL(cancelURL),
	}); err != nil {
		t.Fatalf("template execute failed: %v", err)
	}
	rendered := buf.String()

	// The ONLY </script> in the whole page must be the two legitimate
	// closing tags for the checkout.js loader and the inline options
	// script — never a third one injected via the hostile name.
	if strings.Count(rendered, "</script>") != 2 {
		t.Fatalf("expected exactly 2 legitimate </script> closes, got %d in:\n%s",
			strings.Count(rendered, "</script>"), rendered)
	}
	if strings.Contains(rendered, "<script>alert(") {
		t.Fatalf("hostile inline script survived template rendering:\n%s", rendered)
	}

	// Regression check: html/template's default HTML-attribute escaper
	// silently rewrites any non-http(s)/mailto URL scheme to "#ZgotmplZ" —
	// caught by hand while rendering a sample page, since it doesn't error,
	// it just quietly breaks the deep link. Passing CancelURL as
	// template.URL (not a plain string) is what avoids this.
	if strings.Contains(rendered, "ZgotmplZ") {
		t.Fatalf("gogoo:// cancel link was sanitized away — got #ZgotmplZ instead:\n%s", rendered)
	}
	// '&' is correctly HTML-entity-escaped to '&amp;' inside the href
	// attribute — that's standard, valid escaping, not the bug being
	// guarded against here, so check for the scheme/prefix surviving
	// rather than a byte-for-byte match against the raw URL.
	if !strings.Contains(rendered, `href="gogoo://wallet-topup-return?status=cancelled`) {
		t.Fatalf("expected the gogoo:// cancel link to survive rendering:\n%s", rendered)
	}
}

func TestCheckoutReturnTemplate_EscapesOrderID(t *testing.T) {
	// orderID here is already validated by razorpayOrderIDPattern before the
	// handler ever builds this URL, but the template itself should still
	// not choke on/mis-render an unexpected value passed to it directly.
	returnURL := `gogoo://wallet-topup-return?status=success&order_id=order_abc"><script>alert(1)</script>`
	returnURLJSON, _ := json.Marshal(returnURL)

	var buf bytes.Buffer
	if err := checkoutReturnTmpl.Execute(&buf, map[string]interface{}{
		"ReturnURL":   template.URL(returnURL),
		"ReturnURLJS": template.JS(returnURLJSON),
	}); err != nil {
		t.Fatalf("template execute failed: %v", err)
	}
	rendered := buf.String()
	if strings.Contains(rendered, `<script>alert(1)</script>`) {
		t.Fatalf("hostile content survived template rendering unescaped:\n%s", rendered)
	}
}

func TestCheckoutReturnTemplate_PreservesCustomScheme(t *testing.T) {
	returnURL := "gogoo://wallet-topup-return?status=success&order_id=order_abc123"
	returnURLJSON, _ := json.Marshal(returnURL)

	var buf bytes.Buffer
	if err := checkoutReturnTmpl.Execute(&buf, map[string]interface{}{
		"ReturnURL":   template.URL(returnURL),
		"ReturnURLJS": template.JS(returnURLJSON),
	}); err != nil {
		t.Fatalf("template execute failed: %v", err)
	}
	rendered := buf.String()
	if strings.Contains(rendered, "ZgotmplZ") {
		t.Fatalf("gogoo:// return link was sanitized away — got #ZgotmplZ instead:\n%s", rendered)
	}
	if !strings.Contains(rendered, `href="gogoo://wallet-topup-return?status=success`) {
		t.Fatalf("expected the gogoo:// return link to survive rendering:\n%s", rendered)
	}
}

// TestWebhookNotGatedByPaymentsFlag proves RIDER_ONLINE_PAYMENTS_ENABLED
// being off does not short-circuit WalletTopupWebhook before it reaches
// signature verification. This only exercises something meaningful when rzp
// is non-nil, which is fixed at process init from RAZORPAY_KEY_ID/SECRET —
// t.Setenv here is too late to affect that package-level var, so this test
// requires those two env vars to already be set in the shell that invokes
// `go test` (see the Phase 1A report for the exact command). Skips instead
// of failing when they aren't, so a plain `go test ./...` run elsewhere
// doesn't spuriously fail over an environment precondition it can't control.
func TestWebhookNotGatedByPaymentsFlag(t *testing.T) {
	if rzp == nil {
		t.Skip("rzp is nil — run with RAZORPAY_KEY_ID/RAZORPAY_KEY_SECRET set in the environment to exercise this test")
	}

	old := os.Getenv("RIDER_ONLINE_PAYMENTS_ENABLED")
	os.Setenv("RIDER_ONLINE_PAYMENTS_ENABLED", "false")
	defer os.Setenv("RIDER_ONLINE_PAYMENTS_ENABLED", old)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/gogoo/wallet/topup/webhook", bytes.NewBufferString(`{}`))
	c.Request.Header.Set("X-Razorpay-Signature", "definitely-not-valid")

	WalletTopupWebhook(c)

	// A flag-gated handler would return 503 here (same as
	// CreateWalletTopupOrder does when the flag is off). Reaching the
	// signature check instead (401) proves the flag was never consulted.
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 (invalid signature) proving the webhook ignores the payments flag, got %d: %s", w.Code, w.Body.String())
	}
}
