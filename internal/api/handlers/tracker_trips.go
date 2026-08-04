package handlers

// Bogie Tracker — multi-drop / part-shipment trips (migration 055). A trip
// is one truck+driver run that can carry multiple stops (tracker_orders
// rows) in sequence; see CreateTrackerCompanyOrder for how a trip gets
// created (implicitly, on first stop) or extended (client passes trip_id).

import (
	"context"
	"net/http"
	"time"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TrackerTrip is the company-scoped view of a trip — includes
// driver_tracking_token since the company is the one who needs to share
// that link with the driver. Never sent on the public trip page (see
// GetPublicTrackerTrip, which builds its own trimmed response).
type TrackerTrip struct {
	ID                  string            `json:"id"`
	CompanyID           string            `json:"company_id"`
	DispatchFrom        string            `json:"dispatch_from"`
	VehicleNumber       string            `json:"vehicle_number"`
	DriverID            *string           `json:"driver_id"`
	DriverName          *string           `json:"driver_name"`
	DriverPhone         *string           `json:"driver_phone"`
	OverallStatus       string            `json:"overall_status"`
	DriverTrackingToken *string           `json:"driver_tracking_token"`
	LastLat             *float64          `json:"last_lat"`
	LastLng             *float64          `json:"last_lng"`
	LastLocationAt      *time.Time        `json:"last_location_at"`
	PublicTrackingToken string            `json:"public_tracking_token"`
	CreatedAt           time.Time         `json:"created_at"`
	CompletedAt         *time.Time        `json:"completed_at"`
	Stops               []TrackerTripStop `json:"stops"`
}

// TrackerTripStop is one stop's summary within a trip — enough for the
// trip-level views (company detail + public page) to list every stop and
// link out to each stop's own per-order page, without pulling that order's
// full field set.
type TrackerTripStop struct {
	OrderID             string  `json:"order_id"`
	StopSequence        int     `json:"stop_sequence"`
	Status              string  `json:"status"`
	ConsigneeName       *string `json:"consignee_name"`
	DispatchTo          string  `json:"dispatch_to"`
	PublicTrackingToken string  `json:"public_tracking_token"`
}

// fetchTrackerTripStops is shared by GetTrackerCompanyTrip and
// GetPublicTrackerTrip — caller has already resolved/authorized the tripID.
func fetchTrackerTripStops(ctx context.Context, pool *pgxpool.Pool, tripID string) ([]TrackerTripStop, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, stop_sequence, status, consignee_name, dispatch_to, public_tracking_token
		FROM tracker_orders
		WHERE trip_id = $1
		ORDER BY stop_sequence ASC
	`, tripID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stops := []TrackerTripStop{}
	for rows.Next() {
		var s TrackerTripStop
		if err := rows.Scan(&s.OrderID, &s.StopSequence, &s.Status, &s.ConsigneeName, &s.DispatchTo, &s.PublicTrackingToken); err != nil {
			continue
		}
		stops = append(stops, s)
	}
	return stops, nil
}

// GET /gogoo/tracker/trips/:id — company-scoped. Powers the "Add another
// stop" prefill (vehicle/driver/dispatch_from) and will power a future
// trip-detail panel view.
func GetTrackerCompanyTrip(c *gin.Context) {
	companyID := c.GetString("company_id")
	tripID := c.Param("id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var t TrackerTrip
	err := pool.QueryRow(ctx, `
		SELECT id, company_id, dispatch_from, vehicle_number,
		       driver_id::text, driver_name, driver_phone,
		       overall_status, driver_tracking_token,
		       last_lat, last_lng, last_location_at,
		       public_tracking_token, created_at, completed_at
		FROM tracker_trips WHERE id = $1 AND company_id = $2
	`, tripID, companyID).Scan(
		&t.ID, &t.CompanyID, &t.DispatchFrom, &t.VehicleNumber,
		&t.DriverID, &t.DriverName, &t.DriverPhone,
		&t.OverallStatus, &t.DriverTrackingToken,
		&t.LastLat, &t.LastLng, &t.LastLocationAt,
		&t.PublicTrackingToken, &t.CreatedAt, &t.CompletedAt,
	)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "trip not found"})
		return
	}

	stops, err := fetchTrackerTripStops(ctx, pool, t.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	t.Stops = stops

	c.JSON(http.StatusOK, t)
}

// GET /gogoo/public/tracker/trips/:token — unauthenticated, looked up by
// public_tracking_token. Read-only: no receipt-confirmation actions here,
// those stay on each stop's own per-order public page
// (GetPublicTrackerOrder). Deliberately omits company_id/driver_id/
// driver_tracking_token — same "only what the page needs to render, nothing
// that could be replayed to impersonate the driver" rule GetPublicTrackerOrder
// already follows.
func GetPublicTrackerTrip(c *gin.Context) {
	token := c.Param("token")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var tripID, dispatchFrom, vehicleNumber, overallStatus, companyName string
	var driverName, driverPhone *string
	var lastLat, lastLng *float64
	var lastLocationAt *time.Time
	var createdAt time.Time
	var completedAt *time.Time
	var companyLogoURL *string
	err := pool.QueryRow(ctx, `
		SELECT t.id, t.dispatch_from, t.vehicle_number, t.driver_name, t.driver_phone,
		       t.overall_status, t.last_lat, t.last_lng, t.last_location_at,
		       t.created_at, t.completed_at, c.company_name, c.logo_url
		FROM tracker_trips t
		JOIN tracker_companies c ON c.id = t.company_id
		WHERE t.public_tracking_token = $1
	`, token).Scan(
		&tripID, &dispatchFrom, &vehicleNumber, &driverName, &driverPhone,
		&overallStatus, &lastLat, &lastLng, &lastLocationAt,
		&createdAt, &completedAt, &companyName, &companyLogoURL,
	)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "tracking link not found"})
		return
	}

	stops, err := fetchTrackerTripStops(ctx, pool, tripID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"dispatch_from":    dispatchFrom,
		"vehicle_number":   vehicleNumber,
		"driver_name":      driverName,
		"driver_phone":     driverPhone,
		"overall_status":   overallStatus,
		"last_lat":         lastLat,
		"last_lng":         lastLng,
		"last_location_at": lastLocationAt,
		"created_at":       createdAt,
		"completed_at":     completedAt,
		"company_name":     companyName,
		"company_logo_url": companyLogoURL,
		"stops":            stops,
	})
}
