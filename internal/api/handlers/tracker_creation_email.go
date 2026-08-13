package handlers

// Bogie Tracker — shared document-attachment helpers for tracker order
// emails (Cloudinary fetch + byte-budget packing, doc-type display labels),
// used by tracker_notify.go's Dispatch Details email and the delivered
// status email's proof-of-delivery attachment. The "Shipment Created" email
// that used to live in this file has been retired — see
// SendTrackerOrderCreationEmail below.

import (
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/deploykit/backend/internal/mail"
	"github.com/gin-gonic/gin"
)

// trackerEmailAttachmentBudgetBytes is the RAW (pre-Base64) byte budget for
// a creation email's combined attachments. Resend caps at 40MB *after*
// Base64 encoding (https://resend.com/docs/api-reference/emails/send-email),
// and Base64 inflates raw bytes by ~4/3 — so the true break-even is ~30MB
// raw. 28MB leaves ~2MB of headroom for encoding rounding, since this is a
// hard API rejection if crossed, not a soft warning.
const trackerEmailAttachmentBudgetBytes = 28 * 1024 * 1024

var trackerDocTypeDisplayLabels = map[string]string{
	"coa":       "COA",
	"invoice":   "Invoice",
	"lr":        "LR",
	"eway_bill": "E-way Bill",
	"other":     "Other",
}

// POST /gogoo/tracker/orders/:id/creation-email
//
// Retired: order creation no longer sends its own "Shipment Created"
// stakeholder email — the first email a booked-for/contact-person/consignee
// now gets is the Dispatched status-change email (see
// tracker_status_email.go), which carries the same comprehensive
// FIELD/DETAILS table this endpoint used to send on its own. The frontend
// still calls this endpoint once after order creation + document upload
// finishes (see orders/new/page.tsx) as a fire-and-forget call whose result
// nothing depends on, so this stays a harmless 200 no-op rather than
// requiring a frontend change to stop calling it.
func SendTrackerOrderCreationEmail(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "creation email disabled"})
}

// trackerEmailAttachable is a genericized (label, file URL) pair — the
// common shape buildTrackerEmailAttachments needs regardless of whether the
// source is a tracker_order_documents row (creation email) or a single
// proof-of-delivery signature image (status-change email, see
// tracker_status_email.go).
type trackerEmailAttachable struct {
	Label   string
	FileURL string
}

// buildTrackerEmailAttachments fetches each file's bytes from Cloudinary and
// greedily packs them into the raw-byte budget in list order, skipping (not
// aborting on) anything that doesn't fit — so one large early file can't
// crowd out smaller ones that come after it. Returns the attachments that
// fit and the labels of ones that didn't (or that failed to fetch), for the
// body's fallback note.
func buildTrackerEmailAttachments(files []trackerEmailAttachable) ([]mail.Attachment, []string) {
	var attachments []mail.Attachment
	var skipped []string
	var total int64

	client := &http.Client{Timeout: 20 * time.Second}
	for _, f := range files {
		resp, err := client.Get(f.FileURL)
		if err != nil {
			skipped = append(skipped, f.Label)
			continue
		}
		// A non-200 (auth-gated storage, expired URL, permission change,
		// anything) must never fall through to the read below — Cloudinary in
		// particular returns 401 with an empty body for access-restricted
		// files, which would otherwise read as a "valid" 0-byte attachment
		// instead of being skipped. Read-and-discard the error body so the
		// connection can be reused, then skip.
		if resp.StatusCode != http.StatusOK {
			io.Copy(io.Discard, io.LimitReader(resp.Body, maxFileSize))
			resp.Body.Close()
			log.Printf("tracker email attachment: non-200 fetching %s (%s): %d", f.Label, f.FileURL, resp.StatusCode)
			skipped = append(skipped, f.Label)
			continue
		}
		// LimitReader+1 guards against a mis-sized/backfilled row (upload-
		// time validation already caps new uploads at maxFileSize) without
		// ever buffering more than one byte past the cap.
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxFileSize+1))
		resp.Body.Close()
		if readErr != nil || int64(len(data)) > maxFileSize {
			skipped = append(skipped, f.Label)
			continue
		}
		if total+int64(len(data)) > trackerEmailAttachmentBudgetBytes {
			skipped = append(skipped, f.Label)
			continue
		}
		total += int64(len(data))
		ext := trackerDocFileExt(f.FileURL)
		if ext == "" {
			ext = ".pdf"
		}
		attachments = append(attachments, mail.Attachment{
			Filename:    f.Label + ext,
			ContentType: trackerDocContentType(f.FileURL),
			Data:        data,
		})
	}
	return attachments, skipped
}

func trackerDocDisplayLabel(d TrackerOrderDocument) string {
	if d.DocType == "other" && d.CustomLabel != nil && *d.CustomLabel != "" {
		return *d.CustomLabel
	}
	if label, ok := trackerDocTypeDisplayLabels[d.DocType]; ok {
		return label
	}
	return d.DocType
}

func trackerDocContentType(fileURL string) string {
	switch trackerDocFileExt(fileURL) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	default:
		return "application/pdf"
	}
}

// trackerDocFileExt strips any Cloudinary query/version suffix before
// reading the extension, since file_url is a full URL, not a bare filename.
func trackerDocFileExt(fileURL string) string {
	ext := strings.ToLower(filepath.Ext(fileURL))
	if idx := strings.IndexAny(ext, "?#"); idx >= 0 {
		ext = ext[:idx]
	}
	return ext
}

