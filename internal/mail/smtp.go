// Package mail provides a minimal SMTP client capable of sending an email
// with a single binary attachment (e.g. a PDF statement). It intentionally
// avoids a third-party mail library — net/smtp plus a hand-built MIME
// multipart body is enough for transactional mail and keeps the dependency
// footprint small.
package mail

import (
	"bytes"
	"crypto/tls"
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

// Send delivers msg via the configured SMTP relay. Port 465 (Gmail's
// implicit-TLS port) is dialed with TLS from the first byte since
// net/smtp has no built-in support for that; any other port goes through
// net/smtp.SendMail, which upgrades via STARTTLS itself (the plain 587
// path — this is what a "connect: connection timed out" error on 587
// means the network/host is blocking, not a credentials problem).
func Send(cfg *config.Config, msg Message) error {
	if !IsConfigured(cfg) {
		return fmt.Errorf("smtp not configured (SMTP_HOST/SMTP_FROM_EMAIL missing)")
	}

	body := buildMIMEMessage(cfg, msg)
	toAddrs := strings.Split(msg.To, ",")

	if cfg.SMTPPort == 465 {
		return sendImplicitTLS(cfg, toAddrs, body)
	}

	addr := fmt.Sprintf("%s:%d", cfg.SMTPHost, cfg.SMTPPort)
	var auth smtp.Auth
	if cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPassword, cfg.SMTPHost)
	}
	return smtp.SendMail(addr, auth, cfg.SMTPFromEmail, toAddrs, body)
}

// buildMIMEMessage hand-builds the raw RFC 5322 message (net/smtp has no
// attachment support) as a simple multipart/mixed body: one text/plain
// part, one base64-encoded part per attachment. Shared by both the
// STARTTLS (587) and implicit-TLS (465) send paths — one place that knows
// what an outgoing message looks like.
func buildMIMEMessage(cfg *config.Config, msg Message) []byte {
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
	return buf.Bytes()
}

// sendImplicitTLS speaks SMTP over a connection that's already TLS from
// the first byte (port 465) — net/smtp.SendMail can't do this, it always
// connects plaintext and STARTTLS-upgrades, which is exactly the path that
// times out when a host blocks 587.
func sendImplicitTLS(cfg *config.Config, toAddrs []string, body []byte) error {
	addr := fmt.Sprintf("%s:%d", cfg.SMTPHost, cfg.SMTPPort)
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.SMTPHost})
	if err != nil {
		return fmt.Errorf("tls dial to %s failed: %w", addr, err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, cfg.SMTPHost)
	if err != nil {
		return fmt.Errorf("smtp client init failed: %w", err)
	}
	defer client.Close()

	if cfg.SMTPUser != "" {
		auth := smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPassword, cfg.SMTPHost)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth failed: %w", err)
		}
	}
	if err := client.Mail(cfg.SMTPFromEmail); err != nil {
		return fmt.Errorf("MAIL FROM failed: %w", err)
	}
	for _, a := range toAddrs {
		if err := client.Rcpt(strings.TrimSpace(a)); err != nil {
			return fmt.Errorf("RCPT TO failed for %s: %w", a, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA command failed: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("writing message body failed: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("closing DATA writer failed: %w", err)
	}
	return client.Quit()
}
