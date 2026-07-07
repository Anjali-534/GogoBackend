package handlers

import (
	"fmt"
	"html"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// backendPublicURL is the base URL the referral landing pages (and the
// share_link returned by /gogoo/referral/my-code) are served from. bogie.in
// isn't wired up to host these yet, so the Railway backend itself acts as
// the link server — see referralLandingHTML. Once bogie.in (or a subdomain
// of it) points here, set GOGOO_BACKEND_URL rather than editing this default.
func backendPublicURL() string {
	if v := os.Getenv("GOGOO_BACKEND_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://gogobackend-production.up.railway.app"
}

// referralLandingHTML renders a small smart-redirect page: it immediately
// tries to open the target app via its custom scheme, and falls back to a
// download button (or a "coming soon" hint if no APK URL is configured yet)
// for anyone who doesn't have the app installed.
func referralLandingHTML(code, schemeURL, apkURL, referrerType string) string {
	safeCode := html.EscapeString(code)
	safeScheme := html.EscapeString(schemeURL)

	downloadBlock := `<p class="hint">Download link coming soon — ask the person who invited you for the app.</p>`
	if apkURL != "" {
		downloadBlock = fmt.Sprintf(`<a class="btn" id="dl" href="%s">&#11015;&#65039; Download gogoo</a>`, html.EscapeString(apkURL))
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>gogoo &mdash; You're invited!</title>
<style>
  body { font-family: -apple-system, sans-serif; background:#FFF8F5; margin:0; padding:24px;
    display:flex; flex-direction:column; align-items:center; justify-content:center;
    min-height:90vh; text-align:center; }
  .logo { font-size:42px; font-weight:800; color:#FF6B2B; }
  .code { background:#FFF; border:2px dashed #FF6B2B; border-radius:14px; padding:14px 26px;
    font-size:24px; font-weight:700; letter-spacing:2px; margin:18px 0; }
  .btn { background:#FF6B2B; color:#FFF; padding:16px 32px; border-radius:14px; font-weight:700;
    text-decoration:none; font-size:16px; margin-top:10px; display:inline-block; }
  .hint { color:#9CA3AF; font-size:13px; margin-top:16px; }
</style>
</head>
<body>
  <div class="logo">gogoo</div>
  <p>&#127873; <b>%s</b> invited you! Sign up with this code and ride:</p>
  <div class="code">%s</div>
  %s
  <p class="hint">Already installed? <a href="%s">Open the app</a></p>
  <script>
    window.location = "%s";
  </script>
</body>
</html>`, referrerType, safeCode, downloadBlock, safeScheme, safeScheme)
}

// GET /r/:code — public rider-referral landing page. Tries to open the
// user app (gogoo://referral?code=...); shows a download page otherwise.
func ReferralLandingUser(c *gin.Context) {
	code := strings.ToUpper(strings.TrimSpace(c.Param("code")))
	if code == "" {
		c.String(http.StatusNotFound, "not found")
		return
	}
	schemeURL := fmt.Sprintf("gogoo://referral?code=%s", code)
	c.Data(http.StatusOK, "text/html; charset=utf-8",
		[]byte(referralLandingHTML(code, schemeURL, os.Getenv("USER_APP_APK_URL"), "A friend")))
}

// GET /dr/:code — public driver-referral landing page. Tries to open the
// driver app (gogoodriver://referral?code=...); shows a download page otherwise.
func ReferralLandingDriver(c *gin.Context) {
	code := strings.ToUpper(strings.TrimSpace(c.Param("code")))
	if code == "" {
		c.String(http.StatusNotFound, "not found")
		return
	}
	schemeURL := fmt.Sprintf("gogoodriver://referral?code=%s", code)
	c.Data(http.StatusOK, "text/html; charset=utf-8",
		[]byte(referralLandingHTML(code, schemeURL, os.Getenv("DRIVER_APP_APK_URL"), "A gogoo driver")))
}
