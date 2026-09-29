package invoicemail

import (
	"context"
	"time"

	"github.com/deploykit/backend/internal/services/bookinginvoice"
	"github.com/jackc/pgx/v5/pgxpool"
)

// bookingEmail is one invoiced booking plus what the send path needs beyond
// the PDF itself.
type bookingEmail struct {
	Invoice     *bookinginvoice.Invoice
	RiderEmail  string
	UserDeleted bool
}

// loadBookingEmail reads a completed, invoiced booking into the
// bookinginvoice.Invoice the PDF is rendered from. Read-only.
func loadBookingEmail(ctx context.Context, pool *pgxpool.Pool, bookingID string) (*bookingEmail, error) {
	var (
		inv                   bookinginvoice.Invoice
		category              string
		issuedAt              *time.Time
		tripFare              *float64
		riderEmail, riderName string
		userDeleted           bool
	)
	err := pool.QueryRow(ctx, `
		SELECT b.id::text, b.invoice_number, b.invoice_issued_at,
		       COALESCE(st.category, ''), COALESCE(st.name, ''),
		       COALESCE(d.vehicle_model, ''), COALESCE(d.vehicle_number, ''), COALESCE(du.name, ''),
		       COALESCE(b.payment_method, 'cash'),
		       COALESCE(b.pickup_address, ''), COALESCE(b.drop_address, ''),
		       b.started_at, b.completed_at, COALESCE(b.distance_km, 0)::float8,
		       COALESCE(b.receiver_name, ''), COALESCE(b.receiver_phone, ''),
		       b.trip_fare::float8, COALESCE(b.discount_amount, 0)::float8, COALESCE(b.promo_code, ''),
		       COALESCE(b.carried_cancellation_fee, 0)::float8, COALESCE(b.final_fare, 0)::float8,
		       COALESCE(ru.name, ''), COALESCE(ru.email, ''), ru.deleted_at IS NOT NULL
		FROM bookings b
		JOIN service_types st ON st.id = b.service_type_id
		JOIN riders r ON r.id = b.rider_id
		JOIN users ru ON ru.id = r.user_id
		LEFT JOIN drivers d ON d.id = b.driver_id
		LEFT JOIN users du ON du.id = d.user_id
		WHERE b.id = $1 AND b.invoice_number IS NOT NULL
	`, bookingID).Scan(
		&inv.BookingID, &inv.Number, &issuedAt,
		&category, &inv.ServiceName,
		&inv.VehicleType, &inv.VehicleNumber, &inv.DriverName,
		&inv.PaymentMethod,
		&inv.PickupAddress, &inv.DropAddress,
		&inv.StartedAt, &inv.CompletedAt, &inv.DistanceKm,
		&inv.ConsigneeName, &inv.ConsigneePhone,
		&tripFare, &inv.Discount, &inv.PromoCode,
		&inv.CarriedCancellationFee, &inv.TotalAmount,
		&riderName, &riderEmail, &userDeleted,
	)
	if err != nil {
		return nil, err
	}

	inv.Kind = bookinginvoice.KindForCategory(category)
	inv.RiderName = riderName
	if issuedAt != nil {
		inv.IssuedAt = *issuedAt
	} else if inv.CompletedAt != nil {
		inv.IssuedAt = *inv.CompletedAt
	}
	// trip_fare is NULL for a booking created before migration 063 but
	// completed after it. Derive it so the fare table's Sub Total equals
	// the amount charged instead of showing a Rs. 0 trip fare with the
	// whole fare as "Rounding".
	if tripFare != nil {
		inv.TripFare = *tripFare
	} else {
		inv.TripFare = inv.TotalAmount + inv.Discount - inv.CarriedCancellationFee
	}

	return &bookingEmail{Invoice: &inv, RiderEmail: riderEmail, UserDeleted: userDeleted}, nil
}
