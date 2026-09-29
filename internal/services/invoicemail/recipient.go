package invoicemail

import (
	"net/mail"
	"strings"
)

// Addresses the backend itself writes into users.email that must never be
// mailed:
//   - deleted-<userID>@bogie.in — rider and driver account deletion
//     (handlers/account_deletion.go, handlers/driver_account_deletion.go)
//   - tracker-company-<companyID>@synthetic.gogoo.internal — a Bogie Tracker
//     company's synthetic rider (services/trackerrider)
//
// Matched a little more broadly than the exact formats, so a future variant
// of either still never gets mailed.
const (
	deletedPrefix   = "deleted-"
	deletedDomain   = "@bogie.in"
	syntheticDomain = "@synthetic.gogoo.internal"
)

// skipReason reports why addr must not be emailed, or "" when it may be.
// userDeleted is users.deleted_at IS NOT NULL — checked on its own as well,
// in case a deleted account's email was ever left un-anonymized. Reasons are
// stored in bookings.invoice_last_error, so they never include the address.
func skipReason(addr string, userDeleted bool) string {
	if userDeleted {
		return "rider account deleted"
	}
	lower := strings.ToLower(strings.TrimSpace(addr))
	switch {
	case lower == "":
		return "no email on file"
	case strings.HasPrefix(lower, deletedPrefix) && strings.HasSuffix(lower, deletedDomain):
		return "deleted-account placeholder address"
	case strings.HasSuffix(lower, syntheticDomain):
		return "synthetic tracker-company address"
	case !plausibleAddress(addr):
		return "implausible email address"
	}
	return ""
}

// plausibleAddress is a basic format check, not verification: one bare
// address (no display name) with a non-empty local part and a dotted domain.
// A comma is rejected outright because mail.Send splits To on commas — a
// crafted "a@x.com,b@y.com" would otherwise fan out to two recipients.
func plausibleAddress(addr string) bool {
	if addr != strings.TrimSpace(addr) || len(addr) > 254 || strings.ContainsAny(addr, ", ;") {
		return false
	}
	parsed, err := mail.ParseAddress(addr)
	if err != nil || parsed.Address != addr || parsed.Name != "" {
		return false
	}
	at := strings.LastIndex(addr, "@")
	if at < 1 {
		return false
	}
	domain := addr[at+1:]
	dot := strings.LastIndex(domain, ".")
	return dot > 0 && dot < len(domain)-1
}
