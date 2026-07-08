package handlers

import (
	"fmt"
	"html"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

// GET /driver-app — landing page behind the user app's "Become a Driver"
// CTA. Tries to open the driver app via its custom scheme; falls back to a
// download button (or a "coming soon" hint if no APK URL is configured yet)
// for anyone who doesn't have it installed. Same smart-redirect pattern as
// the /r/:code and /dr/:code referral landing pages.
func DriverAppLanding(c *gin.Context) {
	const schemeURL = "gogoodriver://"
	apkURL := os.Getenv("DRIVER_APP_APK_URL")

	downloadBlock := `<p class="hint">Download link coming soon &mdash; check back shortly.</p>`
	if apkURL != "" {
		downloadBlock = fmt.Sprintf(`<a class="btn" id="dl" href="%s">&#11015;&#65039; Download Driver App</a>`, html.EscapeString(apkURL))
	}

	page := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>bogie Driver &mdash; Drive with us</title>
<style>
  body { font-family: -apple-system, sans-serif; background:#FFF8F5; margin:0; padding:24px;
    display:flex; flex-direction:column; align-items:center; justify-content:center;
    min-height:90vh; text-align:center; }
  .logo { font-size:42px; font-weight:800; color:#FF6B2B; }
  .heading { font-size:20px; font-weight:800; color:#111; margin:14px 0 6px; }
  .bullets { background:#FFF; border-radius:14px; padding:18px 22px 18px 38px; margin:14px 0; text-align:left; }
  .bullets li { font-size:14px; color:#374151; margin-bottom:8px; }
  .btn { background:#FF6B2B; color:#FFF; padding:16px 32px; border-radius:14px; font-weight:700;
    text-decoration:none; font-size:16px; margin-top:10px; display:inline-block; }
  .hint { color:#9CA3AF; font-size:13px; margin-top:16px; }
</style>
</head>
<body>
  <div class="logo">bogie</div>
  <div class="heading">&#128663; Drive with bogie</div>
  <ul class="bullets">
    <li>Zero commission on ambulance rides</li>
    <li>Low commission on all other rides</li>
    <li>Daily payouts</li>
    <li>Flexible hours &mdash; be your own boss</li>
  </ul>
  %s
  <p class="hint">Already installed? <a href="%s">Open the app</a></p>
  <script>
    window.location = "%s";
  </script>
</body>
</html>`, downloadBlock, schemeURL, schemeURL)

	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(page))
}
