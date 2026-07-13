package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
)

// GET /gogoo/bookings/pending
// Driver feed: all unassigned ride requests, newest first.
// (MVP: returns all 'searching' bookings. Geo-filtering by driver
// proximity can be layered on later using current_lat/current_lng.)
func ListPendingBookings(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// rider_phone is deliberately NOT in this feed: it goes to every driver
	// for every un-accepted booking. Drivers get the phone from GetBooking
	// after accepting, which is the only point they need it.
	rows, err := pool.Query(ctx, `
    SELECT b.id, b.rider_id, b.service_type_id, b.status,
           b.pickup_lat, b.pickup_lng, b.pickup_address,
           b.drop_lat, b.drop_lng, b.drop_address,
           COALESCE(b.estimated_fare,0), COALESCE(b.distance_km,0),
           COALESCE(u.name,''),
           COALESCE(st.name,''), b.requested_at
    FROM bookings b
    JOIN riders r ON r.id = b.rider_id
    JOIN users u  ON u.id = r.user_id
    LEFT JOIN service_types st ON st.id = b.service_type_id
    WHERE b.status = 'searching'
    ORDER BY b.requested_at DESC
    LIMIT 50
`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	var out []gin.H
	for rows.Next() {
		var id, riderID, serviceTypeID, status string
		var pLat, pLng, dLat, dLng, fare, dist float64
		var pAddr, dAddr, riderName, serviceName string
		var requestedAt interface{}
		if err := rows.Scan(&id, &riderID, &serviceTypeID, &status,
			&pLat, &pLng, &pAddr, &dLat, &dLng, &dAddr,
			&fare, &dist, &riderName, &serviceName, &requestedAt); err != nil {
			continue
		}
		out = append(out, gin.H{
			"id":              id,
			"rider_id":        riderID,
			"service_type_id": serviceTypeID,
			"service_name":    serviceName,
			"status":          status,
			"pickup":          gin.H{"lat": pLat, "lng": pLng, "address": pAddr},
			"drop":            gin.H{"lat": dLat, "lng": dLng, "address": dAddr},
			"estimated_fare":  fare,
			"distance_km":     dist,
			"rider_name":      riderName,
			"requested_at":    requestedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"bookings": out})
}

// POST /gogoo/drivers/:id/location
// Driver pushes its live GPS. Writes to the driver row and, if the driver
// has an active booking, mirrors the position onto that booking so the
// rider's single poll call gets it.
func UpdateDriverLocation(c *gin.Context) {
	driverID := c.Param("id")
	var req struct {
		Lat     float64 `json:"lat" binding:"required"`
		Lng     float64 `json:"lng" binding:"required"`
		Heading float64 `json:"heading"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// A driver can only push their own GPS position — otherwise any
	// authenticated account could spoof another driver's live location
	// (which mirrors onto that driver's active booking and the rider's map).
	if !isDriverOwner(ctx, pool, driverID, c.GetString("user_id")) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	if _, err := pool.Exec(ctx,
		`UPDATE drivers SET current_lat=$1, current_lng=$2, location_updated_at=NOW()
		 WHERE id=$3`,
		req.Lat, req.Lng, driverID,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update location"})
		return
	}

	// Mirror onto any active booking for this driver.
	pool.Exec(ctx,
		`UPDATE bookings
		   SET driver_lat=$1, driver_lng=$2, driver_heading=$3, driver_updated_at=NOW()
		 WHERE driver_id=$4
		   AND status IN ('accepted','arriving','in_progress')`,
		req.Lat, req.Lng, req.Heading, driverID,
	)

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// GET /gogoo/bookings/:id
// Rider poll: full booking status + live driver position + driver details.
func GetBooking(c *gin.Context) {
	bookingID := c.Param("id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Ownership check: this response carries both parties' phones, patient
	// name, live driver GPS, and the ride OTP, so it is limited to the
	// booking's own rider or assigned driver plus oversight tokens
	// (master admin and the support/category panels). Everyone else gets a
	// bare 403 — including for booking IDs that don't exist, so the
	// endpoint can't be used to probe which UUIDs are real.
	if c.GetString("role") != "master_admin" {
		switch c.GetString("panel") {
		case "support", "cab", "truck", "ambulance":
			// panel staff: allowed
		default:
			if _, _, ok := bookingCallerRole(ctx, pool, bookingID, c.GetString("user_id")); !ok {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
		}
	}

	var (
		id, riderID, status                              string
		driverID                                         *string
		pLat, pLng, dLat, dLng                           float64
		pAddr, dAddr                                     string
		fare, dist                                       float64
		driverLat, driverLng, driverHeading              *float64
		driverUpdatedAt                                  *time.Time
		driverName, driverPhone, vehicleNumber, vehModel *string
		driverRating                                     *float64
		riderName, riderPhone, serviceName               string
		rideOTP                                          *string
		finalFare                                        *float64
		startedAt, completedAt                           *time.Time
		// Ambulance fields
		hospitalID, hospitalName, ambulanceSubType, purposeType, patientName *string
		isFreeAmbulance                                                       bool
		// Cancellation + scheduling
		vehicleCategory                          string
		cancellationFee                          float64
		cancelledBy, cancelReason                 string
		cancelledAt                               *time.Time
		isScheduled                               bool
		scheduledAt                               *time.Time
		// Receiver details (truck/parcel deliveries)
		receiverName, receiverPhone *string
	)

	err := pool.QueryRow(ctx, `
		SELECT b.id, b.rider_id, b.status, b.driver_id,
		       b.pickup_lat, b.pickup_lng, b.pickup_address,
		       b.drop_lat, b.drop_lng, b.drop_address,
		       COALESCE(b.estimated_fare,0), COALESCE(b.distance_km,0),
		       b.driver_lat, b.driver_lng, b.driver_heading, b.driver_updated_at,
		       du.name, d.phone, d.vehicle_number, d.vehicle_model, d.rating,
		       COALESCE(u_r.name,'') AS rider_name,
		       COALESCE(r.phone,'')  AS rider_phone,
		       COALESCE(st.name,'')  AS service_name,
		       b.ride_otp,
		       b.final_fare,
		       b.started_at,
		       b.completed_at,
		       b.hospital_id::TEXT,
		       b.hospital_name,
		       b.ambulance_sub_type,
		       COALESCE(b.is_free_ambulance, FALSE),
		       b.purpose_type,
		       b.patient_name,
		       COALESCE(st.category,''),
		       COALESCE(b.cancellation_fee,0),
		       COALESCE(b.cancelled_by,''),
		       COALESCE(b.cancel_reason,''),
		       b.cancelled_at,
		       COALESCE(b.is_scheduled,false),
		       b.scheduled_at,
		       b.receiver_name,
		       b.receiver_phone
		FROM bookings b
		LEFT JOIN drivers      d   ON d.id    = b.driver_id
		LEFT JOIN users        du  ON du.id   = d.user_id
		LEFT JOIN riders       r   ON r.id    = b.rider_id
		LEFT JOIN users        u_r ON u_r.id  = r.user_id
		LEFT JOIN service_types st ON st.id   = b.service_type_id
		WHERE b.id = $1
	`, bookingID).Scan(
		&id, &riderID, &status, &driverID,
		&pLat, &pLng, &pAddr, &dLat, &dLng, &dAddr,
		&fare, &dist,
		&driverLat, &driverLng, &driverHeading, &driverUpdatedAt,
		&driverName, &driverPhone, &vehicleNumber, &vehModel, &driverRating,
		&riderName, &riderPhone, &serviceName,
		&rideOTP,
		&finalFare, &startedAt, &completedAt,
		&hospitalID, &hospitalName, &ambulanceSubType,
		&isFreeAmbulance, &purposeType, &patientName,
		&vehicleCategory, &cancellationFee, &cancelledBy, &cancelReason, &cancelledAt,
		&isScheduled, &scheduledAt,
		&receiverName, &receiverPhone,
	)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
		return
	}

	resp := gin.H{
		"id":                 id,
		"rider_id":           riderID,
		"status":             status,
		"pickup":             gin.H{"lat": pLat, "lng": pLng, "address": pAddr},
		"drop":               gin.H{"lat": dLat, "lng": dLng, "address": dAddr},
		"estimated_fare":     fare,
		"distance_km":        dist,
		"rider_name":         riderName,
		"rider_phone":        riderPhone,
		"service_name":       serviceName,
		"final_fare":         finalFare,
		"started_at":         startedAt,
		"completed_at":       completedAt,
		"hospital_id":        hospitalID,
		"hospital_name":      hospitalName,
		"ambulance_sub_type": ambulanceSubType,
		"is_free_ambulance":  isFreeAmbulance,
		"purpose_type":       purposeType,
		"patient_name":       patientName,
		"vehicle_category":   vehicleCategory,
		"cancellation_fee":   cancellationFee,
		"cancelled_by":       cancelledBy,
		"cancel_reason":      cancelReason,
		"cancelled_at":       cancelledAt,
		"is_scheduled":       isScheduled,
		"scheduled_at":       scheduledAt,
		"receiver_name":      receiverName,
		"receiver_phone":     receiverPhone,
	}
	// Expose OTP once driver has arrived so the rider can read it aloud.
	if status == "arriving" && rideOTP != nil {
		resp["ride_otp"] = *rideOTP
	}

	// Ride-chat unread badge — count messages from the OTHER party relative
	// to whoever is polling (rider sees unread driver messages, and vice versa).
	callerUserID := c.GetString("user_id")
	otherSenderType := "driver"
	if driverID != nil {
		var isDriverCaller bool
		pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM drivers WHERE id = $1 AND user_id = $2::uuid)`, *driverID, callerUserID).Scan(&isDriverCaller)
		if isDriverCaller {
			otherSenderType = "rider"
		}
	}
	var unreadCount int
	pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM ride_messages
		WHERE booking_id = $1 AND sender_type = $2 AND is_read = FALSE
	`, bookingID, otherSenderType).Scan(&unreadCount)
	resp["unread_message_count"] = unreadCount

	if driverID != nil {
		driver := gin.H{"id": *driverID}
		if driverName != nil {
			driver["name"] = *driverName
		}
		if driverPhone != nil {
			driver["phone"] = *driverPhone
		}
		if vehicleNumber != nil {
			driver["vehicle_number"] = *vehicleNumber
		}
		if vehModel != nil {
			driver["vehicle_model"] = *vehModel
		}
		if driverRating != nil {
			driver["rating"] = *driverRating
		}
		if driverLat != nil && driverLng != nil {
			driver["lat"] = *driverLat
			driver["lng"] = *driverLng
			if driverHeading != nil {
				driver["heading"] = *driverHeading
			}
			if driverUpdatedAt != nil {
				driver["updated_at"] = *driverUpdatedAt
			}
		}
		resp["driver"] = driver
	}

	c.JSON(http.StatusOK, resp)
}
