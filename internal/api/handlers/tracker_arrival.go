package handlers

import (
	"context"
	"errors"
	"log"
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

// ─── Driver token → the stop a driver action applies to ────────────────────

// trackerDriverStop is the tracker_orders row a driver action applies to.
type trackerDriverStop struct {
	OrderID      string
	CompanyID    string
	Status       string
	ToLat        *float64
	ToLng        *float64
	HasSignature bool
	TripID       string // "" for a single-order token
}

// trackerDriverAction is what the driver is doing; the status rules differ.
type trackerDriverAction int

const (
	// trackerActionReached: the 'reached' event.
	trackerActionReached trackerDriverAction = iota
	// trackerActionDeliver: 'delivery_claimed' and the signature upload.
	trackerActionDeliver
)

// trackerDriverError is a failed driver request: Status/Msg/Code go to the
// client, Err (if any) only to the log.
type trackerDriverError struct {
	Status int
	Msg    string
	Code   string
	Err    error
}

func (e *trackerDriverError) Error() string {
	if e.Err != nil {
		return e.Msg + ": " + e.Err.Error()
	}
	return e.Msg
}

func trackerDriverDBError(err error) *trackerDriverError {
	return &trackerDriverError{Status: http.StatusInternalServerError, Msg: "database error", Err: err}
}

var errTrackerStopNotOnLink = &trackerDriverError{Status: http.StatusNotFound, Code: "stop_not_on_link",
	Msg: "this stop is not part of this tracking link"}

const trackerStopColumns = `id::text, company_id::text, status, dispatch_to_lat, dispatch_to_lng, signature_url IS NOT NULL`

func scanTrackerStop(row pgx.Row, stop *trackerDriverStop) error {
	return row.Scan(&stop.OrderID, &stop.CompanyID, &stop.Status, &stop.ToLat, &stop.ToLng, &stop.HasSignature)
}

// resolveTrackerDriverTarget finds the order a driver action applies to.
//
// orderID is the stop the drive page was showing when the driver acted
// (order_id from GetTrackerDriverOrder). It is never swapped for "the
// current active stop": a trip advances as soon as a consignee confirms
// receipt, and re-resolving then sent stop 1's claim + signature to stop 2.
//
//   - Trip token + orderID: the order must belong to that trip.
//   - Trip token, no orderID: allowed only for 'reached' (uses the active
//     stop, as before); claim/signature must name the stop.
//   - Single-order token: orderID is optional, but must match if sent.
//
// Then the action's status rules apply (checkTrackerDriverAction).
func resolveTrackerDriverTarget(ctx context.Context, q querier, token, orderID string, action trackerDriverAction) (trackerDriverStop, *trackerDriverError) {
	var stop trackerDriverStop

	var tripID, tripStatus string
	err := q.QueryRow(ctx, `
		SELECT id::text, overall_status FROM tracker_trips WHERE driver_tracking_token = $1
	`, token).Scan(&tripID, &tripStatus)
	switch {
	case err == nil:
		if tripStatus == "cancelled" {
			return stop, &trackerDriverError{Status: http.StatusBadRequest, Msg: "this trip has been cancelled"}
		}
		if orderID == "" {
			if action == trackerActionDeliver {
				return stop, &trackerDriverError{Status: http.StatusBadRequest, Code: "stop_id_required",
					Msg: "this page is out of date — please reload it and try again"}
			}
			err = scanTrackerStop(q.QueryRow(ctx, `
				SELECT `+trackerStopColumns+`
				FROM tracker_orders
				WHERE trip_id = $1 AND status NOT IN ('delivered','cancelled')
				ORDER BY stop_sequence ASC
				LIMIT 1
			`, tripID), &stop)
			if errors.Is(err, pgx.ErrNoRows) {
				return stop, &trackerDriverError{Status: http.StatusBadRequest, Msg: "trip has no remaining stops to deliver"}
			}
		} else {
			if uuid.Validate(orderID) != nil {
				return stop, errTrackerStopNotOnLink
			}
			err = scanTrackerStop(q.QueryRow(ctx, `
				SELECT `+trackerStopColumns+` FROM tracker_orders WHERE id = $1 AND trip_id = $2
			`, orderID, tripID), &stop)
			if errors.Is(err, pgx.ErrNoRows) {
				return stop, errTrackerStopNotOnLink
			}
		}
		if err != nil {
			return stop, trackerDriverDBError(err)
		}
		stop.TripID = tripID
	case errors.Is(err, pgx.ErrNoRows):
		err = scanTrackerStop(q.QueryRow(ctx, `
			SELECT `+trackerStopColumns+` FROM tracker_orders WHERE driver_tracking_token = $1
		`, token), &stop)
		if errors.Is(err, pgx.ErrNoRows) {
			return stop, &trackerDriverError{Status: http.StatusNotFound, Msg: "tracking link not found"}
		}
		if err != nil {
			return stop, trackerDriverDBError(err)
		}
		if orderID != "" && orderID != stop.OrderID {
			return stop, errTrackerStopNotOnLink
		}
	default:
		return stop, trackerDriverDBError(err)
	}

	if e := checkTrackerDriverAction(stop, action); e != nil {
		return stop, e
	}
	return stop, nil
}

// checkTrackerDriverAction applies the status rules:
//   - cancelled: nothing is accepted.
//   - 'reached': not on a delivered order.
//   - claim/signature: also accepted on a delivered order that has no
//     signature yet. The consignee's receipt confirmation completes the
//     delivery on its own (tracker_receipt.go), often before the driver
//     signs, and the driver's proof of delivery must still be taken. Once
//     the signature is on file a delivered order accepts nothing more.
func checkTrackerDriverAction(stop trackerDriverStop, action trackerDriverAction) *trackerDriverError {
	switch {
	case stop.Status == "cancelled":
		return &trackerDriverError{Status: http.StatusBadRequest, Code: "cancelled", Msg: "this order has been cancelled"}
	case stop.Status != "delivered":
		return nil
	case action == trackerActionReached:
		return &trackerDriverError{Status: http.StatusBadRequest, Code: "already_delivered", Msg: "this order has already been delivered"}
	case stop.HasSignature:
		return &trackerDriverError{Status: http.StatusConflict, Code: "already_signed",
			Msg: "delivery and signature are already recorded for this order"}
	}
	return nil
}

// saveTrackerDriverSignature stores the signature URL, re-checking the
// status rules in the UPDATE itself so a concurrent cancel or a second
// signature on a delivered order can't slip in between check and write.
// false means the row no longer accepts a signature.
func saveTrackerDriverSignature(ctx context.Context, q querier, orderID, fileURL string) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE tracker_orders SET signature_url = $1, updated_at = NOW()
		WHERE id = $2 AND status <> 'cancelled' AND NOT (status = 'delivered' AND signature_url IS NOT NULL)
	`, fileURL, orderID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// trackerDriverFail logs every rejected driver request with its cause, so
// Railway shows why (the drive page only sees Msg), then responds. extra
// adds fields to the JSON body (e.g. distance_m on a 422).
func trackerDriverFail(c *gin.Context, handler, orderID string, e *trackerDriverError, extra gin.H) {
	tok := c.Param("driver_token")
	if len(tok) > 6 {
		tok = tok[:6] + "…"
	}
	if orderID == "" {
		orderID = "-"
	}
	log.Printf("%s: %d token=%s order=%s: %v", handler, e.Status, tok, orderID, e)
	body := gin.H{"error": e.Msg}
	if e.Code != "" {
		body["code"] = e.Code
	}
	for k, v := range extra {
		body[k] = v
	}
	c.JSON(e.Status, body)
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
	Kind string `json:"kind" binding:"required"`
	// OrderID is the stop the drive page is acting on (order_id from
	// GetTrackerDriverOrder); required for delivery_claimed on a trip token.
	OrderID   string   `json:"order_id"`
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
