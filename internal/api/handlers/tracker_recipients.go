package handlers

// Bogie Tracker — saved recipients ("bank beneficiaries" for dispatch
// orders). Company-scoped and shared across owner + all staff logins: routes
// are gated by RequireTrackerCompany only, NOT RequireTrackerOwner — this is
// operational data, not administrative (see migration 041).
//
// Orders never reference these rows. Selecting a recipient pre-fills the
// order form client-side and the order stores plain field values as always;
// CreateTrackerCompanyOrder's optional saved_recipient_id only bumps
// use_count/last_used_at, which power the most-used-first list ordering.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

type trackerSavedRecipient struct {
	ID    string `json:"id"`
	Label string `json:"label"`

	BookedForCompanyName string  `json:"booked_for_company_name"`
	BookedForPhone       string  `json:"booked_for_phone"`
	BookedForEmail       *string `json:"booked_for_email"`
	BookedForGstin       *string `json:"booked_for_gstin"`
	BookedForState       *string `json:"booked_for_state"`

	ConsigneeName  *string `json:"consignee_name"`
	ConsigneeEmail *string `json:"consignee_email"`
	ConsigneeGstin *string `json:"consignee_gstin"`
	ConsigneeState *string `json:"consignee_state"`

	DispatchTo    *string  `json:"dispatch_to"`
	DispatchToLat *float64 `json:"dispatch_to_lat"`
	DispatchToLng *float64 `json:"dispatch_to_lng"`

	UseCount   int        `json:"use_count"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

// trackerSavedRecipientReq is the shared create/update payload. Label,
// booked-for name and phone are the only required pieces — everything else
// mirrors the optional order-form fields it pre-fills.
type trackerSavedRecipientReq struct {
	Label                string `json:"label" binding:"required"`
	BookedForCompanyName string `json:"booked_for_company_name" binding:"required"`
	BookedForPhone       string `json:"booked_for_phone" binding:"required"`
	BookedForEmail       string `json:"booked_for_email" binding:"omitempty,email"`
	BookedForGstin       string `json:"booked_for_gstin"`
	BookedForState       string `json:"booked_for_state"`

	ConsigneeName  string `json:"consignee_name"`
	ConsigneeEmail string `json:"consignee_email" binding:"omitempty,email"`
	ConsigneeGstin string `json:"consignee_gstin"`
	ConsigneeState string `json:"consignee_state"`

	DispatchTo    string   `json:"dispatch_to"`
	DispatchToLat *float64 `json:"dispatch_to_lat"`
	DispatchToLng *float64 `json:"dispatch_to_lng"`
}

const trackerSavedRecipientCols = `
	id, label,
	booked_for_company_name, booked_for_phone, booked_for_email,
	booked_for_gstin, booked_for_state,
	consignee_name, consignee_email, consignee_gstin, consignee_state,
	dispatch_to, dispatch_to_lat, dispatch_to_lng,
	use_count, last_used_at, created_at`

func scanTrackerSavedRecipient(row interface{ Scan(...any) error }) (trackerSavedRecipient, error) {
	var r trackerSavedRecipient
	err := row.Scan(
		&r.ID, &r.Label,
		&r.BookedForCompanyName, &r.BookedForPhone, &r.BookedForEmail,
		&r.BookedForGstin, &r.BookedForState,
		&r.ConsigneeName, &r.ConsigneeEmail, &r.ConsigneeGstin, &r.ConsigneeState,
		&r.DispatchTo, &r.DispatchToLat, &r.DispatchToLng,
		&r.UseCount, &r.LastUsedAt, &r.CreatedAt,
	)
	return r, err
}

// GET /gogoo/tracker/recipients — most-used-first, so the picker surfaces
// the recipients a company actually dispatches to.
func ListTrackerSavedRecipients(c *gin.Context) {
	companyID := c.GetString("company_id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT `+trackerSavedRecipientCols+`
		FROM tracker_saved_recipients
		WHERE company_id = $1
		ORDER BY use_count DESC, last_used_at DESC NULLS LAST, created_at DESC
	`, companyID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	defer rows.Close()

	recipients := []trackerSavedRecipient{}
	for rows.Next() {
		r, err := scanTrackerSavedRecipient(rows)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
			return
		}
		recipients = append(recipients, r)
	}
	c.JSON(http.StatusOK, recipients)
}

// POST /gogoo/tracker/recipients
func CreateTrackerSavedRecipient(c *gin.Context) {
	companyID := c.GetString("company_id")

	var req trackerSavedRecipientReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	row := pool.QueryRow(ctx, `
		INSERT INTO tracker_saved_recipients
			(company_id, label,
			 booked_for_company_name, booked_for_phone, booked_for_email,
			 booked_for_gstin, booked_for_state,
			 consignee_name, consignee_email, consignee_gstin, consignee_state,
			 dispatch_to, dispatch_to_lat, dispatch_to_lng)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING `+trackerSavedRecipientCols,
		companyID, req.Label,
		req.BookedForCompanyName, req.BookedForPhone, nullIfEmpty(req.BookedForEmail),
		nullIfEmpty(req.BookedForGstin), nullIfEmpty(req.BookedForState),
		nullIfEmpty(req.ConsigneeName), nullIfEmpty(req.ConsigneeEmail),
		nullIfEmpty(req.ConsigneeGstin), nullIfEmpty(req.ConsigneeState),
		nullIfEmpty(req.DispatchTo), req.DispatchToLat, req.DispatchToLng)

	r, err := scanTrackerSavedRecipient(row)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	c.JSON(http.StatusCreated, r)
}

// PATCH /gogoo/tracker/recipients/:id — full-payload update (the manage UI
// always sends every field, same shape as create).
func UpdateTrackerSavedRecipient(c *gin.Context) {
	companyID := c.GetString("company_id")
	recipientID := c.Param("id")

	var req trackerSavedRecipientReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	row := pool.QueryRow(ctx, `
		UPDATE tracker_saved_recipients SET
			label=$1,
			booked_for_company_name=$2, booked_for_phone=$3, booked_for_email=$4,
			booked_for_gstin=$5, booked_for_state=$6,
			consignee_name=$7, consignee_email=$8, consignee_gstin=$9, consignee_state=$10,
			dispatch_to=$11, dispatch_to_lat=$12, dispatch_to_lng=$13,
			updated_at=NOW()
		WHERE id=$14 AND company_id=$15
		RETURNING `+trackerSavedRecipientCols,
		req.Label,
		req.BookedForCompanyName, req.BookedForPhone, nullIfEmpty(req.BookedForEmail),
		nullIfEmpty(req.BookedForGstin), nullIfEmpty(req.BookedForState),
		nullIfEmpty(req.ConsigneeName), nullIfEmpty(req.ConsigneeEmail),
		nullIfEmpty(req.ConsigneeGstin), nullIfEmpty(req.ConsigneeState),
		nullIfEmpty(req.DispatchTo), req.DispatchToLat, req.DispatchToLng,
		recipientID, companyID)

	r, err := scanTrackerSavedRecipient(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "recipient not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, r)
}

// DELETE /gogoo/tracker/recipients/:id
func DeleteTrackerSavedRecipient(c *gin.Context) {
	companyID := c.GetString("company_id")
	recipientID := c.Param("id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	tag, err := pool.Exec(ctx, `
		DELETE FROM tracker_saved_recipients WHERE id=$1 AND company_id=$2
	`, recipientID, companyID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "recipient not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "recipient removed"})
}
