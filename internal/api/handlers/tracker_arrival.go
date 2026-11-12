package handlers

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/deploykit/backend/internal/config"
)

// Arrival check for the tracker drive page (/drive/[token] in
// bogie-tracker-panel). 'reached' unlocks the driver's Sign + Delivered
// flow; this file decides whether a reported position counts as "at the
// drop point", resolves a driver token to the stop it is currently
// delivering, and writes the per-event audit trail (migration 069).
//
// The coordinates are self-reported by the driver's phone, so this is a
// consistency check and an audit trail, not anti-fraud: a driver who fakes
// GPS can pass it. The value is that honest drivers are never stuck (the
// override path) and every out-of-radius or location-less arrival is
// flagged for the company to see.

// trackerArrivalConfig mirrors config.TrackerArrivalRadiusM /
// TrackerArrivalAccuracyCapM. The drive page is served these same two
// numbers by GetTrackerDriverOrder and applies the identical rule
// (lib/arrival.ts), so the server is the single source of truth.
type trackerArrivalConfig struct {
	RadiusM      float64
	AccuracyCapM float64
}

func trackerArrivalConfigFrom(cfg *config.Config) trackerArrivalConfig {
	return trackerArrivalConfig{
		RadiusM:      float64(cfg.TrackerArrivalRadiusM),
		AccuracyCapM: float64(cfg.TrackerArrivalAccuracyCapM),
	}
}

// effectiveTrackerAccuracy is how much GPS uncertainty is credited to the
// driver: the reported accuracy, capped, with missing/invalid values (NaN,
// Inf, negative) treated as zero, i.e. no credit.
func effectiveTrackerAccuracy(accuracyM, capM float64) float64 {
	if math.IsNaN(accuracyM) || math.IsInf(accuracyM, 0) || accuracyM < 0 {
		return 0
	}
	return math.Min(accuracyM, capM)
}

// trackerArrivalCheck: arrived when distance - min(accuracy, cap) <= radius.
func trackerArrivalCheck(distanceM, accuracyM float64, cfg trackerArrivalConfig) bool {
	return distanceM-effectiveTrackerAccuracy(accuracyM, cfg.AccuracyCapM) <= cfg.RadiusM
}

// ─── Driver token → current stop ───────────────────────────────────────────

var errTrackerLinkNotFound = errors.New("tracking link not found")

// trackerDriverStop is the tracker_orders row a driver token is currently
// delivering. For a multi-stop trip token (migration 055) that's the
// lowest stop_sequence not yet delivered/cancelled, the same "active stop"
// GetTrackerDriverOrder shows the driver; for a single-order token it's
// that order.
type trackerDriverStop struct {
	OrderID   string
	CompanyID string
	Status    string
	ToLat     *float64
	ToLng     *float64
	TripID    string // "" for a single-order token
}

// resolveTrackerDriverStop returns errTrackerLinkNotFound for an unknown
// token, and a non-empty terminalMsg (with a nil error) when the token is
// valid but there is nothing left to deliver.
func resolveTrackerDriverStop(ctx context.Context, q querier, token string) (stop trackerDriverStop, terminalMsg string, err error) {
	var tripID, tripStatus string
	err = q.QueryRow(ctx, `
		SELECT id::text, overall_status FROM tracker_trips WHERE driver_tracking_token = $1
	`, token).Scan(&tripID, &tripStatus)
	switch {
	case err == nil:
		if tripStatus == "completed" || tripStatus == "cancelled" {
			return stop, "trip is in a terminal state (" + tripStatus + ") and is no longer tracked", nil
		}
		err = q.QueryRow(ctx, `
			SELECT id::text, company_id::text, status, dispatch_to_lat, dispatch_to_lng
			FROM tracker_orders
			WHERE trip_id = $1 AND status NOT IN ('delivered','cancelled')
			ORDER BY stop_sequence ASC
			LIMIT 1
		`, tripID).Scan(&stop.OrderID, &stop.CompanyID, &stop.Status, &stop.ToLat, &stop.ToLng)
		if errors.Is(err, pgx.ErrNoRows) {
			return stop, "trip has no remaining stops to deliver", nil
		}
		if err != nil {
			return stop, "", err
		}
		stop.TripID = tripID
		return stop, "", nil
	case !errors.Is(err, pgx.ErrNoRows):
		return stop, "", err
	}

	err = q.QueryRow(ctx, `
		SELECT id::text, company_id::text, status, dispatch_to_lat, dispatch_to_lng
		FROM tracker_orders WHERE driver_tracking_token = $1
	`, token).Scan(&stop.OrderID, &stop.CompanyID, &stop.Status, &stop.ToLat, &stop.ToLng)
	if errors.Is(err, pgx.ErrNoRows) {
		return stop, "", errTrackerLinkNotFound
	}
	if err != nil {
		return stop, "", err
	}
	if terminalOrderStatuses[stop.Status] {
		return stop, "order is in a terminal state (" + stop.Status + ") and is no longer tracked", nil
	}
	return stop, "", nil
}

// respondTrackerDriverStopError writes the response for a failed
// resolveTrackerDriverStop and reports whether it did.
func respondTrackerDriverStopError(c *gin.Context, terminalMsg string, err error) bool {
	switch {
	case errors.Is(err, errTrackerLinkNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "tracking link not found"})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
	case terminalMsg != "":
		c.JSON(http.StatusBadRequest, gin.H{"error": terminalMsg})
	default:
		return false
	}
	return true
}

// trackerStopProgress is how far the driver has got on one stop, returned
// to the drive page as reached_recorded / delivery_claimed_recorded so a
// reloaded page resumes where it was instead of re-posting.
type trackerStopProgress struct {
	// Reached: 'reached' is on file (reached_at; backfilled from older
	// events by migration 069).
	Reached bool
	// DeliveryClaimed: the 'delivery_claimed' event AND the signature are
	// both on file. A claim whose signature upload failed doesn't count —
	// the page must still offer Delivered so the driver can sign again
	// (the repeated claim is then a no-op, see recordTrackerDriverEvent).
	DeliveryClaimed bool
}

func loadTrackerStopProgress(ctx context.Context, q querier, orderID string) (trackerStopProgress, error) {
	var p trackerStopProgress
	err := q.QueryRow(ctx, `
		SELECT reached_at IS NOT NULL,
		       delivery_claimed_at IS NOT NULL AND signature_url IS NOT NULL
		FROM tracker_orders WHERE id = $1
	`, orderID).Scan(&p.Reached, &p.DeliveryClaimed)
	return p, err
}

// ─── Event assessment + recording ──────────────────────────────────────────

type trackerDriverEventRequest struct {
	Kind      string   `json:"kind" binding:"required"`
	Note      string   `json:"note"`
	Lat       *float64 `json:"lat"`
	Lng       *float64 `json:"lng"`
	AccuracyM *float64 `json:"accuracy_m"`
	// Override is the drive page's "I'm at the drop location" path, taken
	// when the driver is outside the radius; it requires Reason.
	Override bool   `json:"override"`
	Reason   string `json:"reason"`
}

const (
	trackerOverrideReasonMinRunes = 5
	trackerOverrideReasonMaxRunes = 500
)

// trackerArrivalAssessment is what gets written to the event row and
// rolled up onto the order.
type trackerArrivalAssessment struct {
	Lat, Lng, AccuracyM, DistanceM *float64
	NoLocation                     bool
	OutsideRadius                  bool
	Reason                         string
}

// trackerArrivalError is a rejection to send back as-is.
type trackerArrivalError struct {
	Status int
	Body   gin.H
}

// assessTrackerDriverArrival applies the arrival rule to one driver event.
//
//   - No coordinates: accepted, flagged no_location. TODO: once every open
//     drive page sends coordinates, make lat/lng mandatory for 'reached'
//     and 'delivery_claimed' and reject here instead.
//   - Stop has no drop coordinates: accepted, nothing to compare against.
//   - Within the radius: accepted.
//   - 'reached' outside the radius: rejected (422, with the distance) unless
//     Override with a reason, which is accepted and flagged outside_radius.
//   - 'delivery_claimed' outside the radius: never rejected (the gate is
//     'reached'), just recorded and flagged.
func assessTrackerDriverArrival(req trackerDriverEventRequest, stop trackerDriverStop, cfg trackerArrivalConfig) (trackerArrivalAssessment, *trackerArrivalError) {
	var a trackerArrivalAssessment
	if req.Lat == nil || req.Lng == nil {
		a.NoLocation = true
		return a, nil
	}
	lat, lng := *req.Lat, *req.Lng
	if math.IsNaN(lat) || math.IsNaN(lng) || lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return a, &trackerArrivalError{http.StatusBadRequest, gin.H{"error": "lat/lng out of range"}}
	}
	a.Lat, a.Lng = &lat, &lng

	// Invalid accuracy is stored as NULL and credited as zero.
	accuracy := 0.0
	if req.AccuracyM != nil && !math.IsNaN(*req.AccuracyM) && !math.IsInf(*req.AccuracyM, 0) && *req.AccuracyM >= 0 {
		acc := *req.AccuracyM
		a.AccuracyM = &acc
		accuracy = acc
	}

	if stop.ToLat == nil || stop.ToLng == nil {
		return a, nil
	}
	distance := haversineKm(lat, lng, *stop.ToLat, *stop.ToLng) * 1000
	a.DistanceM = &distance
	if trackerArrivalCheck(distance, accuracy, cfg) {
		return a, nil
	}

	a.OutsideRadius = true
	reason := strings.TrimSpace(req.Reason)
	if utf8.RuneCountInString(reason) > trackerOverrideReasonMaxRunes {
		reason = string([]rune(reason)[:trackerOverrideReasonMaxRunes])
	}
	a.Reason = reason

	if req.Kind != "reached" {
		return a, nil
	}
	if !req.Override {
		return a, &trackerArrivalError{http.StatusUnprocessableEntity, gin.H{
			"error":      "you are not within the arrival radius of the drop location",
			"code":       "outside_arrival_radius",
			"distance_m": math.Round(distance),
			"radius_m":   cfg.RadiusM,
		}}
	}
	if utf8.RuneCountInString(reason) < trackerOverrideReasonMinRunes {
		return a, &trackerArrivalError{http.StatusBadRequest, gin.H{
			"error": "please give a short reason (at least 5 characters)",
			"code":  "override_reason_required",
		}}
	}
	return a, nil
}

// recordTrackerDriverEvent writes the event with its audit columns and
// rolls the flags up onto the stop's order row. A second 'reached' or a
// second 'delivery_claimed' for the same stop is a no-op (already=true):
// no event row, no flag changes. The row lock makes that hold even for two
// concurrent posts. Run it inside a transaction.
func recordTrackerDriverEvent(ctx context.Context, tx querier, stop trackerDriverStop, req trackerDriverEventRequest, a trackerArrivalAssessment) (already bool, err error) {
	var reachedAt, claimedAt *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT reached_at, delivery_claimed_at FROM tracker_orders WHERE id = $1 FOR UPDATE
	`, stop.OrderID).Scan(&reachedAt, &claimedAt); err != nil {
		return false, err
	}
	if (req.Kind == "reached" && reachedAt != nil) || (req.Kind == "delivery_claimed" && claimedAt != nil) {
		return true, nil
	}

	note := strings.TrimSpace(req.Note)
	if note == "" && a.OutsideRadius {
		note = a.Reason
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tracker_order_events
			(id, order_id, status, note, reported_by, event_kind, lat, lng, accuracy_m, distance_m, outside_radius, no_location)
		VALUES ($1,$2,$3,$4,'driver',$5,$6,$7,$8,$9,$10,$11)
	`, uuid.New(), stop.OrderID, stop.Status, nullIfEmpty(note), req.Kind,
		a.Lat, a.Lng, a.AccuracyM, a.DistanceM, a.OutsideRadius, a.NoLocation); err != nil {
		return false, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE tracker_orders SET
			reached_at = CASE WHEN $2::text = 'reached' THEN COALESCE(reached_at, NOW()) ELSE reached_at END,
			delivery_claimed_at = CASE WHEN $2::text = 'delivery_claimed' THEN COALESCE(delivery_claimed_at, NOW()) ELSE delivery_claimed_at END,
			arrived_outside_radius = arrived_outside_radius OR $3::boolean,
			arrival_no_location = arrival_no_location OR $4::boolean,
			arrival_distance_m = CASE WHEN $3::boolean THEN COALESCE(arrival_distance_m, $5::double precision) ELSE arrival_distance_m END,
			arrival_override_reason = COALESCE(arrival_override_reason, $6::text),
			updated_at = NOW()
		WHERE id = $1
	`, stop.OrderID, req.Kind, a.OutsideRadius, a.NoLocation, a.DistanceM, nullIfEmpty(a.Reason)); err != nil {
		return false, err
	}
	return false, nil
}
