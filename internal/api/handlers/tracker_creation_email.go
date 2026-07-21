package handlers

// Bogie Tracker — shipment-creation summary email (Phase 3). Sent to Booked
// For + Contact Person + all CC/BCC addresses, with the order's uploaded
// documents genuinely attached (not just linked) wherever they fit within
// Resend's size cap.
//
// This does NOT fire from inside CreateTrackerCompanyOrder. Documents are
// staged client-side and uploaded via separate POST .../documents calls
// AFTER the order already exists (see orders/new/page.tsx), so at the
// moment CreateTrackerCompanyOrder commits, the document set isn't known
// yet. The frontend instead calls this endpoint once, after its upload
// loop finishes (successes and failures alike — a failed document upload
// shouldn't also suppress the email for the documents that did make it).

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
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
func SendTrackerOrderCreationEmail(c *gin.Context) {
	companyID := c.GetString("company_id")
	orderID := c.Param("id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var o TrackerOrder
	var companyName string
	err := pool.QueryRow(ctx, `
		SELECT o.id, o.dispatch_from, o.dispatch_to,
		       o.material, o.priority, o.internal_reference,
		       o.contact_person_name, o.contact_person_phone, o.contact_person_email, o.contact_person_designation,
		       o.booked_for_email, o.public_tracking_token,
		       c.company_name
		FROM tracker_orders o
		JOIN tracker_companies c ON c.id = o.company_id
		WHERE o.id = $1 AND o.company_id = $2
	`, orderID, companyID).Scan(
		&o.ID, &o.DispatchFrom, &o.DispatchTo,
		&o.Material, &o.Priority, &o.InternalReference,
		&o.ContactPersonName, &o.ContactPersonPhone, &o.ContactPersonEmail, &o.ContactPersonDesignation,
		&o.BookedForEmail, &o.PublicTrackingToken,
		&companyName,
	)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	ccEmails, bccEmails, err := fetchTrackerOrderCCEmails(ctx, pool, orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}

	docs, err := fetchTrackerOrderDocuments(ctx, pool, orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}

	cfg := c.MustGet("config").(*config.Config)
	sendTrackerOrderCreationEmail(cfg, o, companyName, ccEmails, bccEmails, docs)

	c.JSON(http.StatusOK, gin.H{"message": "creation email queued"})
}

// sendTrackerOrderCreationEmail is fire-and-forget, same pattern as
// tracker_mail.go's lifecycle emails — creating a shipment (or this
// follow-up call) must never fail, or surface an error to the company,
// just because Resend hiccuped or a Cloudinary fetch timed out.
func sendTrackerOrderCreationEmail(cfg *config.Config, o TrackerOrder, companyName string, ccEmails, bccEmails []string, docs []TrackerOrderDocument) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("tracker creation email: recovered from panic: %v", r)
			}
		}()
		if !mail.IsConfigured(cfg) {
			return
		}

		var toList []string
		if o.BookedForEmail != nil && *o.BookedForEmail != "" {
			toList = append(toList, *o.BookedForEmail)
		}
		if o.ContactPersonEmail != nil && *o.ContactPersonEmail != "" &&
			(o.BookedForEmail == nil || *o.ContactPersonEmail != *o.BookedForEmail) {
			toList = append(toList, *o.ContactPersonEmail)
		}
		if len(toList) == 0 && len(ccEmails) == 0 && len(bccEmails) == 0 {
			return // nobody to send to
		}

		attachments, skipped := buildTrackerCreationEmailAttachments(docs)

		trackingLink := strings.TrimRight(cfg.TrackerPanelURL, "/") + "/track/" + o.PublicTrackingToken

		subjectRef := o.ID
		if o.InternalReference != nil && *o.InternalReference != "" {
			subjectRef = *o.InternalReference
		}
		subject := fmt.Sprintf("Shipment Details — %s from %s", subjectRef, companyName)

		if err := mail.Send(cfg, mail.Message{
			To:          strings.Join(toList, ","),
			CC:          strings.Join(ccEmails, ","),
			BCC:         strings.Join(bccEmails, ","),
			Subject:     subject,
			Body:        buildTrackerCreationEmailBody(o, companyName, trackingLink, skipped),
			Attachments: attachments,
			FromName:    companyName + " via Bogie Tracker",
		}); err != nil {
			log.Printf("tracker creation email: send failed for order=%s: %v", o.ID, err)
		}
	}()
}

// buildTrackerCreationEmailAttachments fetches each document's bytes from
// Cloudinary and greedily packs them into the raw-byte budget in upload
// order, skipping (not aborting on) anything that doesn't fit — so one
// large early document can't crowd out smaller ones that come after it.
// Returns the attachments that fit and the display labels of ones that
// didn't (or that failed to fetch), for the body's fallback note.
func buildTrackerCreationEmailAttachments(docs []TrackerOrderDocument) ([]mail.Attachment, []string) {
	var attachments []mail.Attachment
	var skipped []string
	var total int64

	client := &http.Client{Timeout: 20 * time.Second}
	for _, d := range docs {
		resp, err := client.Get(d.FileURL)
		if err != nil {
			skipped = append(skipped, trackerDocDisplayLabel(d))
			continue
		}
		// LimitReader+1 guards against a mis-sized/backfilled row (upload-
		// time validation already caps new uploads at maxFileSize) without
		// ever buffering more than one byte past the cap.
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxFileSize+1))
		resp.Body.Close()
		if readErr != nil || int64(len(data)) > maxFileSize {
			skipped = append(skipped, trackerDocDisplayLabel(d))
			continue
		}
		if total+int64(len(data)) > trackerEmailAttachmentBudgetBytes {
			skipped = append(skipped, trackerDocDisplayLabel(d))
			continue
		}
		total += int64(len(data))
		attachments = append(attachments, mail.Attachment{
			Filename:    trackerDocFilename(d),
			ContentType: trackerDocContentType(d.FileURL),
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

func trackerDocFilename(d TrackerOrderDocument) string {
	ext := trackerDocFileExt(d.FileURL)
	if ext == "" {
		ext = ".pdf"
	}
	return trackerDocDisplayLabel(d) + ext
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

func buildTrackerCreationEmailBody(o TrackerOrder, companyName, trackingLink string, skippedDocs []string) string {
	material := "—"
	if o.Material != nil && *o.Material != "" {
		material = *o.Material
	}

	contact := "—"
	if o.ContactPersonName != nil && *o.ContactPersonName != "" {
		contact = *o.ContactPersonName
		if o.ContactPersonDesignation != nil && *o.ContactPersonDesignation != "" {
			contact += " (" + *o.ContactPersonDesignation + ")"
		}
		if o.ContactPersonPhone != nil && *o.ContactPersonPhone != "" {
			contact += " · " + *o.ContactPersonPhone
		}
	}

	priorityLabel, ok := map[string]string{"normal": "Normal", "urgent": "Urgent", "same_day": "Same-day"}[o.Priority]
	if !ok {
		priorityLabel = "Normal"
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("A new shipment has been created by %s.\n\n", companyName))
	b.WriteString("SHIPMENT SUMMARY\n")
	b.WriteString("=================\n\n")
	b.WriteString(fmt.Sprintf("Route: %s -> %s\n", o.DispatchFrom, o.DispatchTo))
	b.WriteString(fmt.Sprintf("Material Description: %s\n", material))
	b.WriteString(fmt.Sprintf("Priority: %s\n", priorityLabel))
	b.WriteString(fmt.Sprintf("Contact Person: %s\n", contact))
	if o.InternalReference != nil && *o.InternalReference != "" {
		b.WriteString(fmt.Sprintf("Internal Reference: %s\n", *o.InternalReference))
	}
	b.WriteString(fmt.Sprintf("\nTrack this shipment live: %s\n", trackingLink))

	if len(skippedDocs) > 0 {
		pronoun := "them"
		if len(skippedDocs) == 1 {
			pronoun = "it"
		}
		b.WriteString(fmt.Sprintf(
			"\nNote: %s exceeded email size limits and could not be attached. You can view and download %s from the tracking page above.\n",
			strings.Join(skippedDocs, ", "), pronoun,
		))
	}

	b.WriteString("\nThis is an automated shipment notification sent via Bogie Tracker.\n")
	return b.String()
}
