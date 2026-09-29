package invoicemail

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/deploykit/backend/internal/mail"
	"github.com/deploykit/backend/internal/services/bookinginvoice"
)

func TestSkipReason(t *testing.T) {
	cases := []struct {
		addr    string
		deleted bool
		skip    bool
	}{
		{"rider@example.com", false, false},
		{"first.last+tag@mail.example.co.in", false, false},
		{"rider@example.com", true, true}, // deleted user, real-looking address
		{"deleted-3f2a9c1e-0000-4000-8000-000000000000@bogie.in", false, true},
		{"DELETED-abc@Bogie.in", false, true},
		{"tracker-company-3f2a9c1e@synthetic.gogoo.internal", false, true},
		{"anything@synthetic.gogoo.internal", false, true},
		{"support@bogie.in", false, false}, // real bogie.in address is fine
		{"", false, true},
		{"not-an-email", false, true},
		{"a@b", false, true},              // no dotted domain
		{"a@b.", false, true},             // empty TLD
		{"@example.com", false, true},     // empty local part
		{"a@x.com,b@y.com", false, true},  // would fan out in mail.Send
		{"a@x.com; b@y.com", false, true}, // ditto
		{"Name <a@x.com>", false, true},   // display name, not a bare address
		{" a@x.com", false, true},         // surrounding whitespace
		{"a b@x.com", false, true},
		{strings.Repeat("a", 250) + "@x.com", false, true},
	}
	for _, tc := range cases {
		got := skipReason(tc.addr, tc.deleted)
		if (got != "") != tc.skip {
			t.Errorf("skipReason(%q, %v) = %q, want skip=%v", tc.addr, tc.deleted, got, tc.skip)
		}
		if got != "" && tc.addr != "" && strings.Contains(got, tc.addr) {
			t.Errorf("skip reason %q leaks the address", got)
		}
	}
}

func sampleInvoice(kind bookinginvoice.Kind) *bookinginvoice.Invoice {
	completed := time.Date(2026, 9, 29, 20, 0, 0, 0, time.UTC) // 30 Sep 01:30 IST
	return &bookinginvoice.Invoice{
		Number:        "BGR/2627/00042",
		IssuedAt:      completed,
		CompletedAt:   &completed,
		Kind:          kind,
		RiderName:     `<script>alert("x")</script> & Co`,
		PickupAddress: "12 <b>Bold</b> St,\nLine 2",
		DropAddress:   `"Quoted" & 'single'`,
		TotalAmount:   292,
	}
}

var completeIssuer = bookinginvoice.Issuer{LegalName: "L", Address: "A", State: "S", GSTIN: "G", SupportEmail: "help<x>@example.com"}

func TestSubjectAndAttachmentFollowTitle(t *testing.T) {
	cases := []struct {
		kind            bookinginvoice.Kind
		issuer          bookinginvoice.Issuer
		subject, attach string
	}{
		{bookinginvoice.KindRide, completeIssuer,
			"Your Bogie invoice BGR/2627/00042 for your ride on 30 Sep 2026", "bogie-invoice-BGR-2627-00042.pdf"},
		{bookinginvoice.KindGoods, completeIssuer,
			"Your Bogie bill of supply BGR/2627/00042 for your booking on 30 Sep 2026", "bogie-bill-of-supply-BGR-2627-00042.pdf"},
		{bookinginvoice.KindRide, bookinginvoice.Issuer{},
			"Your Bogie receipt BGR/2627/00042 for your ride on 30 Sep 2026", "bogie-receipt-BGR-2627-00042.pdf"},
		{bookinginvoice.KindGoods, bookinginvoice.Issuer{},
			"Your Bogie receipt BGR/2627/00042 for your booking on 30 Sep 2026", "bogie-receipt-BGR-2627-00042.pdf"},
	}
	for _, tc := range cases {
		msg := buildMessage(sampleInvoice(tc.kind), tc.issuer, "rider@example.com", []byte("%PDF"))
		if msg.Subject != tc.subject {
			t.Errorf("subject = %q, want %q", msg.Subject, tc.subject)
		}
		if len(msg.Attachments) != 1 || msg.Attachments[0].Filename != tc.attach {
			t.Errorf("attachments = %+v, want one named %q", msg.Attachments, tc.attach)
		}
		if msg.Attachments[0].ContentType != "application/pdf" {
			t.Errorf("content type = %q", msg.Attachments[0].ContentType)
		}
		if msg.IdempotencyKey != "" {
			t.Errorf("buildMessage must leave IdempotencyKey to the caller")
		}
	}
}

func TestHTMLBodyEscapesUserText(t *testing.T) {
	msg := buildMessage(sampleInvoice(bookinginvoice.KindRide), completeIssuer, "rider@example.com", nil)

	for _, raw := range []string{"<script>", "<b>", `"Quoted"`, "'single'", "help<x>"} {
		if strings.Contains(msg.HTMLBody, raw) {
			t.Errorf("HTML body contains unescaped %q", raw)
		}
	}
	for _, escaped := range []string{"&lt;script&gt;", "&lt;b&gt;Bold&lt;/b&gt;", "&#34;Quoted&#34; &amp; &#39;single&#39;", "help&lt;x&gt;"} {
		if !strings.Contains(msg.HTMLBody, escaped) {
			t.Errorf("HTML body missing escaped %q", escaped)
		}
	}
	// Only the template's own tags may appear.
	allowed := map[string]bool{"div": true, "p": true, "strong": true, "table": true, "tr": true, "td": true}
	for _, m := range regexp.MustCompile(`</?([a-zA-Z]+)`).FindAllStringSubmatch(msg.HTMLBody, -1) {
		if !allowed[m[1]] {
			t.Errorf("unexpected tag <%s> in HTML body", m[1])
		}
	}

	if !strings.Contains(msg.Body, "Rs. 292.00") || !strings.Contains(msg.Body, "12 <b>Bold</b> St, Line 2") {
		t.Errorf("plain-text body missing summary: %q", msg.Body)
	}
}

func TestShortErrorNeverLeaksAPIBodyOrRecipient(t *testing.T) {
	apiErr := &mail.APIError{StatusCode: 422, Name: "validation_error", Body: `{"message":"Invalid to: rider@example.com"}`}
	if got := shortError(fmt.Errorf("wrapped: %w", apiErr), "rider@example.com"); got != "resend status 422 validation_error" {
		t.Errorf("shortError(api) = %q", got)
	}
	if got := shortError(errors.New("dial failed for rider@example.com"), "rider@example.com"); strings.Contains(got, "rider@example.com") {
		t.Errorf("shortError leaked recipient: %q", got)
	}
	if got := shortError(errors.New(strings.Repeat("x", 500)), ""); len(got) > 210 {
		t.Errorf("shortError not truncated: %d chars", len(got))
	}
}

func TestAPIErrorKeepsLegacyText(t *testing.T) {
	err := &mail.APIError{StatusCode: 409, Name: "invalid_idempotent_request", Body: `{"name":"invalid_idempotent_request"}`}
	want := `resend API error (status 409): {"name":"invalid_idempotent_request"}`
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// TestBackoffSQLMatches keeps the SQL CASE used by claim and the sweeper in
// step with retryBackoff.
func TestBackoffSQLMatches(t *testing.T) {
	if len(retryBackoff) != MaxAttempts-1 {
		t.Fatalf("retryBackoff has %d entries, want MaxAttempts-1 = %d", len(retryBackoff), MaxAttempts-1)
	}
	sqlUnits := map[string]time.Duration{"minutes": time.Minute, "hours": time.Hour}
	re := regexp.MustCompile(`(?:WHEN (\d+)|ELSE) THEN INTERVAL '(\d+) (minutes|hours)'|ELSE INTERVAL '(\d+) (minutes|hours)'`)
	var got []time.Duration
	for _, m := range re.FindAllStringSubmatch(backoffCaseSQL, -1) {
		n, unit := m[2], m[3]
		if n == "" {
			n, unit = m[4], m[5]
		}
		var v int
		fmt.Sscan(n, &v)
		got = append(got, time.Duration(v)*sqlUnits[unit])
	}
	if fmt.Sprint(got) != fmt.Sprint(retryBackoff) {
		t.Errorf("backoffCaseSQL = %v, retryBackoff = %v", got, retryBackoff)
	}
}

func TestIdempotencyKey(t *testing.T) {
	k := idempotencyKey("3f2a9c1e-0000-4000-8000-000000000000")
	if k != "invoice-email/3f2a9c1e-0000-4000-8000-000000000000" || len(k) > 256 {
		t.Errorf("idempotencyKey = %q", k)
	}
}
