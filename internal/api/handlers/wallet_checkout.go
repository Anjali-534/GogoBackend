package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/deploykit/backend/internal/db"
	"github.com/deploykit/backend/internal/services/payments"
	"github.com/gin-gonic/gin"
)

// riderOnlinePaymentsEnabled gates every rider-facing online-payment surface
// (CreateWalletTopupOrder, payments_available, the checkout page, the
// return endpoint) — but deliberately NOT the webhook, which must keep
// crediting a payment that was already captured even if this flag gets
// flipped off mid-flight. Read fresh via os.Getenv on every call (not
// through config.Config, which is only loaded once at boot) so it can be
// toggled in Railway without a redeploy.
func riderOnlinePaymentsEnabled() bool {
	v := strings.TrimSpace(os.Getenv("RIDER_ONLINE_PAYMENTS_ENABLED"))
	return v == "true" || v == "1"
}

// razorpayOrderIDPattern is Razorpay's own order-id shape. Anything not
// matching this is never logged or reflected anywhere past this check.
var razorpayOrderIDPattern = regexp.MustCompile(`^order_[A-Za-z0-9]+$`)

// checkoutOptions is what gets JSON-marshalled into the page's inline
// <script> block. json.Marshal HTML-escapes '<', '>' and '&' by default
// (encoding/json's documented behavior) — that's what makes it safe to drop
// straight into a <script> tag without a "</script>"-breakout or
// quote-injection risk, so this struct is marshalled with plain
// json.Marshal, never text/template string substitution.
type checkoutOptions struct {
	Key         string            `json:"key"`
	Amount      int64             `json:"amount"`
	Currency    string            `json:"currency"`
	Name        string            `json:"name"`
	OrderID     string            `json:"order_id"`
	CallbackURL string            `json:"callback_url"`
	Redirect    bool              `json:"redirect"`
	Prefill     map[string]string `json:"prefill,omitempty"`
	Theme       map[string]string `json:"theme"`
}

const checkoutPageTemplate = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Bogie Payment</title>
<style>
  body { font-family: -apple-system, Roboto, Helvetica, Arial, sans-serif; background: #FAFAFA; color: #111; display: flex; flex-direction: column; align-items: center; justify-content: center; min-height: 100vh; margin: 0; padding: 24px; box-sizing: border-box; text-align: center; }
  .card { background: #fff; border-radius: 16px; padding: 24px; max-width: 360px; box-shadow: 0 1px 3px rgba(0,0,0,0.08); }
  .cancel-link { display: inline-block; margin-top: 20px; color: #666; font-size: 14px; text-decoration: none; }
</style>
</head>
<body>
  <div class="card">
    <p>Opening secure payment…</p>
    <a class="cancel-link" href="{{.CancelURL}}">Cancel and return to app</a>
  </div>
  <script src="https://checkout.razorpay.com/v1/checkout.js"></script>
  <script>
    var options = {{.OptionsJSON}};
    var rzp1 = new Razorpay(options);
    rzp1.open();
  </script>
</body>
</html>`

var checkoutTmpl = template.Must(template.New("checkout").Parse(checkoutPageTemplate))

// WalletTopupCheckoutPage is GET /gogoo/wallet/topup/checkout?t=<token> —
// public (no JWT: this is opened in the rider's external browser via
// Linking.openURL, which carries no auth headers). The signed token IS the
// authentication: it names which order/rider/amount this page may render,
// and nothing here is trusted beyond what the token itself certifies.
// Amount and prefill never come from the URL — only from the verified
// token (amount) and a DB lookup keyed by the token's rider_id (name/email/
// phone), exactly as required.
func WalletTopupCheckoutPage(c *gin.Context) {
	if !riderOnlinePaymentsEnabled() {
		c.String(http.StatusServiceUnavailable, "Payments are not available right now.")
		return
	}

	token := c.Query("t")
	orderID, riderID, amountPaise, ok := payments.VerifyCheckoutToken(token)
	if !ok || !razorpayOrderIDPattern.MatchString(orderID) {
		c.String(http.StatusForbidden, "This payment link is invalid or has expired.")
		return
	}

	keyID := strings.TrimSpace(os.Getenv("RAZORPAY_KEY_ID"))
	if keyID == "" {
		// Checkout token verified but the gateway itself isn't configured —
		// shouldn't happen (CreateWalletTopupOrder wouldn't have minted a
		// token without rzp being non-nil), but never render a page with an
		// empty key rather than silently failing client-side.
		c.String(http.StatusServiceUnavailable, "Payments are not available right now.")
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()
	var name, email, phone string
	pool.QueryRow(ctx, `
		SELECT COALESCE(u.name,''), COALESCE(u.email,''), COALESCE(r.phone,'')
		FROM riders r JOIN users u ON u.id = r.user_id
		WHERE r.id = $1
	`, riderID).Scan(&name, &email, &phone)

	callbackURL := "https://" + c.Request.Host + "/gogoo/wallet/topup/checkout-return"
	opts := checkoutOptions{
		Key:         keyID,
		Amount:      amountPaise,
		Currency:    "INR",
		Name:        "Bogie",
		OrderID:     orderID,
		CallbackURL: callbackURL,
		Redirect:    true,
		Theme:       map[string]string{"color": "#FF6B2B"},
	}
	if name != "" || email != "" || phone != "" {
		opts.Prefill = map[string]string{}
		if name != "" {
			opts.Prefill["name"] = name
		}
		if email != "" {
			opts.Prefill["email"] = email
		}
		if phone != "" {
			opts.Prefill["contact"] = phone
		}
	}

	optionsJSON, err := json.Marshal(opts)
	if err != nil {
		log.Printf("wallet checkout page: failed to marshal options for order=%s: %v", orderID, err)
		c.String(http.StatusInternalServerError, "Something went wrong.")
		return
	}

	cancelURL := "gogoo://wallet-topup-return?status=cancelled&order_id=" + orderID

	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	// Scoped as tightly as checkout.js's documented script source allows —
	// api.razorpay.com is confirmed (it's the same host razorpay.go already
	// calls server-side), but Razorpay does not publicly document a full CSP
	// requirement list (checked: not in their integration/troubleshooting
	// docs). frame-src/connect-src here are a reasonable starting point, not
	// a verified-complete list — flagged for real-browser console testing
	// before this goes anywhere near production traffic.
	c.Header("Content-Security-Policy", strings.Join([]string{
		"default-src 'none'",
		"script-src https://checkout.razorpay.com",
		"connect-src https://api.razorpay.com https://checkout.razorpay.com",
		"frame-src https://api.razorpay.com https://checkout.razorpay.com",
		"img-src https://checkout.razorpay.com https://cdn.razorpay.com data:",
		"style-src 'unsafe-inline'",
		"form-action 'self' https://api.razorpay.com https://checkout.razorpay.com",
		"base-uri 'none'",
	}, "; "))

	var buf bytes.Buffer
	if err := checkoutTmpl.Execute(&buf, map[string]interface{}{
		"OptionsJSON": template.JS(optionsJSON),
		// html/template's default HTML-attribute escaper treats any
		// non-http(s)/mailto URL scheme as unsafe and silently replaces it
		// with "#ZgotmplZ" — including our own gogoo:// scheme. template.URL
		// opts out of that filtering, which is only safe here because
		// cancelURL is entirely server-built from orderID, and orderID was
		// just validated against razorpayOrderIDPattern above — nothing
		// user-supplied reaches this string unvalidated.
		"CancelURL": template.URL(cancelURL),
	}); err != nil {
		log.Printf("wallet checkout page: template render failed for order=%s: %v", orderID, err)
		c.String(http.StatusInternalServerError, "Something went wrong.")
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", buf.Bytes())
}

const checkoutReturnTemplate = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Bogie Payment</title>
<style>
  body { font-family: -apple-system, Roboto, Helvetica, Arial, sans-serif; background: #FAFAFA; color: #111; display: flex; flex-direction: column; align-items: center; justify-content: center; min-height: 100vh; margin: 0; padding: 24px; box-sizing: border-box; text-align: center; }
  .card { background: #fff; border-radius: 16px; padding: 24px; max-width: 360px; box-shadow: 0 1px 3px rgba(0,0,0,0.08); }
  .btn { display: inline-block; margin-top: 20px; background: #FF6B2B; color: #fff; text-decoration: none; font-weight: 700; padding: 14px 28px; border-radius: 12px; }
</style>
</head>
<body>
  <div class="card">
    <p>Payment received. Return to the Bogie app to see your updated balance.</p>
    <a class="btn" href="{{.ReturnURL}}">Return to Bogie</a>
  </div>
  <script>window.location.href = {{.ReturnURLJS}};</script>
</body>
</html>`

var checkoutReturnTmpl = template.Must(template.New("checkout-return").Parse(checkoutReturnTemplate))

// WalletTopupCheckoutReturn is POST /gogoo/wallet/topup/checkout-return —
// public (Razorpay's hosted Checkout page posts here directly from the
// rider's browser, no JWT available). This endpoint NEVER credits money —
// only the signature-verified webhook does that. Its only jobs are: (1)
// validate the order id shape before it touches a log line or a URL, (2)
// check the payment-verification signature for observability only, (3) hand
// the browser back to the app via the gogoo:// scheme.
func WalletTopupCheckoutReturn(c *gin.Context) {
	if !riderOnlinePaymentsEnabled() {
		c.String(http.StatusServiceUnavailable, "Payments are not available right now.")
		return
	}

	// Razorpay posts these as regular form fields (application/x-www-form-urlencoded).
	paymentID := c.PostForm("razorpay_payment_id")
	orderID := c.PostForm("razorpay_order_id")
	signature := c.PostForm("razorpay_signature")

	if !razorpayOrderIDPattern.MatchString(orderID) {
		log.Printf("wallet checkout return: rejected malformed order id")
		c.String(http.StatusBadRequest, "Invalid request.")
		return
	}

	sigValid := false
	if rzp != nil {
		sigValid = rzp.VerifyPaymentSignature(orderID, paymentID, signature)
	}
	// Logged for observability only — order id and a valid/invalid boolean,
	// never the payment id, the signature, or anything else from the form.
	log.Printf("wallet checkout return: order=%s signature_valid=%t", orderID, sigValid)

	returnURL := "gogoo://wallet-topup-return?status=success&order_id=" + orderID
	returnURLJSON, _ := json.Marshal(returnURL)

	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")

	var buf bytes.Buffer
	if err := checkoutReturnTmpl.Execute(&buf, map[string]interface{}{
		// template.URL for the same reason as the checkout page's CancelURL
		// — html/template would otherwise silently rewrite our own gogoo://
		// scheme to "#ZgotmplZ". Safe here because returnURL is entirely
		// server-built from orderID, already validated above.
		"ReturnURL":   template.URL(returnURL),
		"ReturnURLJS": template.JS(returnURLJSON),
	}); err != nil {
		log.Printf("wallet checkout return: template render failed: %v", err)
		c.String(http.StatusInternalServerError, "Something went wrong.")
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", buf.Bytes())
}
