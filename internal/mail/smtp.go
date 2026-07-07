// Package mail provides a minimal SMTP client capable of sending an email
// with a single binary attachment (e.g. a PDF statement). It intentionally
// avoids a third-party mail library — net/smtp plus a hand-built MIME
// multipart body is enough for transactional mail and keeps the dependency
// footprint small.
package mail

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime"
	"net/smtp"
	"strings"

	"github.com/deploykit/backend/internal/config"
)

// Attachment is a single file to attach to an outgoing email.
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// Message is a plain-text transactional email, optionally with attachments.
type Message struct {
	To          string
	Subject     string
	Body        string
	Attachments []Attachment
}

// IsConfigured reports whether enough SMTP settings are present to attempt
// sending. Callers should treat a false return as "skip silently" rather
// than an error — SMTP is optional deploy-time configuration.
func IsConfigured(cfg *config.Config) bool {
	return cfg.SMTPHost != "" && cfg.SMTPFromEmail != ""
}

// Send delivers msg via the configured SMTP relay. It builds the MIME
// message by hand (net/smtp has no attachment support) using a simple
// multipart/mixed body: one text/plain part, one part per attachment,
// each base64-encoded.
func Send(cfg *config.Config, msg Message) error {
	if !IsConfigured(cfg) {
		return fmt.Errorf("smtp not configured (SMTP_HOST/SMTP_FROM_EMAIL missing)")
	}

	boundary := "gogoo-boundary-42"
	var buf bytes.Buffer

	from := cfg.SMTPFromEmail
	if cfg.SMTPFromName != "" {
		from = fmt.Sprintf("%s <%s>", cfg.SMTPFromName, cfg.SMTPFromEmail)
	}

	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", msg.To)
	fmt.Fprintf(&buf, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", msg.Subject))
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)

	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	fmt.Fprintf(&buf, "Content-Type: text/plain; charset=utf-8\r\n\r\n")
	buf.WriteString(msg.Body)
	buf.WriteString("\r\n")

	for _, a := range msg.Attachments {
		fmt.Fprintf(&buf, "--%s\r\n", boundary)
		fmt.Fprintf(&buf, "Content-Type: %s\r\n", a.ContentType)
		fmt.Fprintf(&buf, "Content-Transfer-Encoding: base64\r\n")
		fmt.Fprintf(&buf, "Content-Disposition: attachment; filename=%q\r\n\r\n", a.Filename)

		encoded := base64.StdEncoding.EncodeToString(a.Data)
		for i := 0; i < len(encoded); i += 76 {
			end := i + 76
			if end > len(encoded) {
				end = len(encoded)
			}
			buf.WriteString(encoded[i:end])
			buf.WriteString("\r\n")
		}
	}
	fmt.Fprintf(&buf, "--%s--\r\n", boundary)

	addr := fmt.Sprintf("%s:%d", cfg.SMTPHost, cfg.SMTPPort)
	var auth smtp.Auth
	if cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPassword, cfg.SMTPHost)
	}

	toAddrs := strings.Split(msg.To, ",")
	return smtp.SendMail(addr, auth, cfg.SMTPFromEmail, toAddrs, buf.Bytes())
}
