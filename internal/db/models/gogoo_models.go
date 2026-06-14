package models

import (
	"time"
	"github.com/google/uuid"
)

// DriverStatus for real-time
type Driver struct {
	ID              uuid.UUID  `db:"id" json:"id"`
	UserID          uuid.UUID  `db:"user_id" json:"user_id"`
	Phone           string     `db:"phone" json:"phone"`
	LicenseNumber   string     `db:"license_number" json:"license_number"`
	VehicleType     string     `db:"vehicle_type" json:"vehicle_type"`
	VehicleNumber   string     `db:"vehicle_number" json:"vehicle_number"`
	VehicleModel    string     `db:"vehicle_model" json:"vehicle_model"`
	VehicleColor    string     `db:"vehicle_color" json:"vehicle_color"`
	ProfilePhotoURL *string    `db:"profile_photo_url" json:"profile_photo_url,omitempty"`
	IsVerified      bool       `db:"is_verified" json:"is_verified"`
	IsOnline        bool       `db:"is_online" json:"is_online"`
	IsActive        bool       `db:"is_active" json:"is_active"`
	CurrentLat      *float64   `db:"current_lat" json:"current_lat,omitempty"`
	CurrentLng      *float64   `db:"current_lng" json:"current_lng,omitempty"`
	Rating          float64    `db:"rating" json:"rating"`
	TotalRides      int        `db:"total_rides" json:"total_rides"`
	TotalEarnings   float64    `db:"total_earnings" json:"total_earnings"`
	CreatedAt       time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time  `db:"updated_at" json:"updated_at"`
	// Joined
	Name            string     `db:"name" json:"name,omitempty"`
	Email           string     `db:"email" json:"email,omitempty"`
}

type Rider struct {
	ID               uuid.UUID `db:"id" json:"id"`
	UserID           uuid.UUID `db:"user_id" json:"user_id"`
	Phone            string    `db:"phone" json:"phone"`
	ProfilePhotoURL  *string   `db:"profile_photo_url" json:"profile_photo_url,omitempty"`
	Rating           float64   `db:"rating" json:"rating"`
	TotalRides       int       `db:"total_rides" json:"total_rides"`
	CreatedAt        time.Time `db:"created_at" json:"created_at"`
	// Joined
	Name             string    `db:"name" json:"name,omitempty"`
	Email            string    `db:"email" json:"email,omitempty"`
}

type ServiceType struct {
	ID              uuid.UUID `db:"id" json:"id"`
	Name            string    `db:"name" json:"name"`
	Slug            string    `db:"slug" json:"slug"`
	VehicleType     string    `db:"vehicle_type" json:"vehicle_type"`
	BaseFare        float64   `db:"base_fare" json:"base_fare"`
	PerKmRate       float64   `db:"per_km_rate" json:"per_km_rate"`
	PerMinRate      float64   `db:"per_min_rate" json:"per_min_rate"`
	SurgeMultiplier float64   `db:"surge_multiplier" json:"surge_multiplier"`
	Capacity        int       `db:"capacity" json:"capacity"`
	IsActive        bool      `db:"is_active" json:"is_active"`
}

type Booking struct {
	ID              uuid.UUID  `db:"id" json:"id"`
	RiderID         uuid.UUID  `db:"rider_id" json:"rider_id"`
	DriverID        *uuid.UUID `db:"driver_id" json:"driver_id,omitempty"`
	ServiceTypeID   uuid.UUID  `db:"service_type_id" json:"service_type_id"`
	Status          string     `db:"status" json:"status"`
	PickupLat       float64    `db:"pickup_lat" json:"pickup_lat"`
	PickupLng       float64    `db:"pickup_lng" json:"pickup_lng"`
	PickupAddress   string     `db:"pickup_address" json:"pickup_address"`
	DropLat         float64    `db:"drop_lat" json:"drop_lat"`
	DropLng         float64    `db:"drop_lng" json:"drop_lng"`
	DropAddress     string     `db:"drop_address" json:"drop_address"`
	DistanceKm      *float64   `db:"distance_km" json:"distance_km,omitempty"`
	DurationMins    *int       `db:"duration_mins" json:"duration_mins,omitempty"`
	EstimatedFare   *float64   `db:"estimated_fare" json:"estimated_fare,omitempty"`
	FinalFare       *float64   `db:"final_fare" json:"final_fare,omitempty"`
	SurgeMultiplier float64    `db:"surge_multiplier" json:"surge_multiplier"`
	DiscountAmount  float64    `db:"discount_amount" json:"discount_amount"`
	RequestedAt     time.Time  `db:"requested_at" json:"requested_at"`
	AcceptedAt      *time.Time `db:"accepted_at" json:"accepted_at,omitempty"`
	CompletedAt     *time.Time `db:"completed_at" json:"completed_at,omitempty"`
	CancelledAt     *time.Time `db:"cancelled_at" json:"cancelled_at,omitempty"`
	CancelReason    *string    `db:"cancel_reason" json:"cancel_reason,omitempty"`
	RiderRating     *int       `db:"rider_rating" json:"rider_rating,omitempty"`
	DriverRating    *int       `db:"driver_rating" json:"driver_rating,omitempty"`
	CreatedAt       time.Time  `db:"created_at" json:"created_at"`
	// Joined fields
	RiderName       string     `db:"rider_name" json:"rider_name,omitempty"`
	DriverName      string     `db:"driver_name" json:"driver_name,omitempty"`
	ServiceName     string     `db:"service_name" json:"service_name,omitempty"`
}

type Payment struct {
	ID             uuid.UUID  `db:"id" json:"id"`
	BookingID      uuid.UUID  `db:"booking_id" json:"booking_id"`
	RiderID        uuid.UUID  `db:"rider_id" json:"rider_id"`
	DriverID       *uuid.UUID `db:"driver_id" json:"driver_id,omitempty"`
	Amount         float64    `db:"amount" json:"amount"`
	PlatformFee    float64    `db:"platform_fee" json:"platform_fee"`
	DriverEarnings float64    `db:"driver_earnings" json:"driver_earnings"`
	Method         string     `db:"method" json:"method"`
	Status         string     `db:"status" json:"status"`
	TransactionID  *string    `db:"transaction_id" json:"transaction_id,omitempty"`
	CreatedAt      time.Time  `db:"created_at" json:"created_at"`
}
