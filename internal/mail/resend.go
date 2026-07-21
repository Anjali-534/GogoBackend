// Package mail sends transactional email (currently: monthly driver
// earnings statements) via Resend's HTTP API. Railway blocks outbound SMTP
// port 587 (confirmed: "dial tcp ...:587: connect: connection timed out"
// from production) and 465 wasn't worth betting on either — Resend's API
// runs over plain HTTPS (443), which is never blocked, so it replaces the
// raw net/smtp client entirely.
package mail

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/deploykit/backend/internal/config"
)

const resendEndpoint = "https://api.resend.com/emails"

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

	// CC and BCC are comma-joined address lists, same convention as To —
	// used by the shipment-creation email (see tracker_creation_email.go),
	// which fans out to a company's variable-length CC/BCC list.
	CC  string
	BCC string

	// FromName overrides cfg.SMTPFromName for this send only — e.g. dispatch
	// emails go out as "<company name> via Bogie Tracker" so the recipient
	// knows which company booked the shipment, while the underlying address
	// stays cfg.ResendFromEmail (SPF/DKIM verified; we never send as the
	// client's own domain).
	FromName string

	// ReplyTo, when set, routes recipient replies to that address instead of
	// cfg.ResendFromEmail — e.g. a company's notification_email.
	ReplyTo string
}

// IsConfigured reports whether enough settings are present to attempt
// sending. Callers should treat a false return as "skip silently" rather
// than an error — email is optional deploy-time configuration.
func IsConfigured(cfg *config.Config) bool {
	return cfg.ResendAPIKey != "" && cfg.ResendFromEmail != ""
}

type resendAttachment struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

type resendPayload struct {
	From        string             `json:"from"`
	To          []string           `json:"to,omitempty"`
	Cc          []string           `json:"cc,omitempty"`
	Bcc         []string           `json:"bcc,omitempty"`
	Subject     string             `json:"subject"`
	Text        string             `json:"text"`
	ReplyTo     string             `json:"reply_to,omitempty"`
	Attachments []resendAttachment `json:"attachments,omitempty"`
}

// Send delivers msg via the Resend HTTP API, sending from cfg.ResendFromEmail
// (an address on bogie.in, which is verified on Resend — that's what lets
// this deliver to arbitrary recipients instead of only the account owner's
// own address, which is all Resend's unverified sandbox domain allows).
//
// Resend caps total attachment size at 40MB *after* Base64 encoding (see
// https://resend.com/docs/api-reference/emails/send-email) — callers
// building Attachments from raw file bytes should budget conservatively
// below the ~30MB raw-byte break-even (Base64 inflates by ~4/3) to leave
// headroom for encoding rounding. See tracker_creation_email.go for the one
// caller that currently needs to reason about this.
func Send(cfg *config.Config, msg Message) error {
	if !IsConfigured(cfg) {
		return fmt.Errorf("resend not configured (RESEND_API_KEY/RESEND_FROM_EMAIL missing)")
	}

	fromName := cfg.SMTPFromName
	if msg.FromName != "" {
		fromName = msg.FromName
	}
	from := cfg.ResendFromEmail
	if fromName != "" {
		from = fmt.Sprintf("%s <%s>", fromName, cfg.ResendFromEmail)
	}

	payload := resendPayload{
		From:    from,
		Subject: msg.Subject,
		Text:    msg.Body,
		ReplyTo: msg.ReplyTo,
	}
	if msg.To != "" {
		payload.To = strings.Split(msg.To, ",")
	}
	if msg.CC != "" {
		payload.Cc = strings.Split(msg.CC, ",")
	}
	if msg.BCC != "" {
		payload.Bcc = strings.Split(msg.BCC, ",")
	}
	for _, a := range msg.Attachments {
		payload.Attachments = append(payload.Attachments, resendAttachment{
			Filename: a.Filename,
			Content:  base64.StdEncoding.EncodeToString(a.Data),
		})
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal resend payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, resendEndpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to build resend request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.ResendAPIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("resend request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("resend API error (status %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}
