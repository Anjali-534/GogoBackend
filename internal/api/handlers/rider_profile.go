package handlers

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// querier is the part of pgx that *pgxpool.Pool, *pgx.Conn and pgx.Tx all
// share, so the rider-profile helpers below run against the production pool
// and against a test connection alike.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// errNoRiderProfile means the caller has no riders row and none may be
// created for it — only ever returned for panel tokens, whose user_id
// names a panel table's row rather than a users row.
var errNoRiderProfile = errors.New("rider profile not found")

// ensureRiderProfile returns userID's riders.id, creating a minimal riders
// row first if there isn't one. A users row can exist without a riders row
// — a driver who signed up in driver-app (password or Google) and then uses
// user-app with the same email, or an account from /auth/signup — and every
// rider endpoint keys off riders.user_id, so without this such a user can
// log in to user-app but never book. phone is left NULL (nullable since
// migration 059), same as a new Google sign-in.
//
// userID must name an existing, active users row; AuthMiddleware has
// already checked that for every blank-panel token. ON CONFLICT on the
// UNIQUE(user_id) constraint makes concurrent first calls safe: one insert
// wins, the other is a no-op, and both read back the same row.
func ensureRiderProfile(ctx context.Context, q querier, userID string) (string, error) {
	var riderID string
	err := q.QueryRow(ctx, `SELECT id FROM riders WHERE user_id=$1`, userID).Scan(&riderID)
	if err == nil {
		return riderID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}

	referralCode := generateReferralCodeWith(ctx, q, "riders", "GU")
	if _, err := q.Exec(ctx,
		`INSERT INTO riders (id, user_id, phone, referral_code) VALUES ($1, $2, NULL, $3)
		 ON CONFLICT (user_id) DO NOTHING`,
		uuid.New(), userID, referralCode,
	); err != nil {
		return "", err
	}
	if err := q.QueryRow(ctx, `SELECT id FROM riders WHERE user_id=$1`, userID).Scan(&riderID); err != nil {
		return "", err
	}
	return riderID, nil
}

// resolveBookingRider returns the riders.id a booking made by this token
// belongs to. Rider tokens (panel == "") get a riders row created on demand;
// panel tokens never do and get errNoRiderProfile if they have none.
func resolveBookingRider(ctx context.Context, q querier, userID, panel string) (string, error) {
	if panel == "" {
		return ensureRiderProfile(ctx, q, userID)
	}
	var riderID string
	err := q.QueryRow(ctx, `SELECT id FROM riders WHERE user_id=$1`, userID).Scan(&riderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errNoRiderProfile
	}
	return riderID, err
}

type riderProfile struct {
	RiderID                    string
	Phone                      string
	Rating                     float64
	TotalRides                 int
	WalletBalance              float64
	OutstandingCancellationFee float64
}

const riderProfileSQL = `SELECT r.id, COALESCE(r.phone,''), COALESCE(r.rating,0), COALESCE(r.total_rides,0), COALESCE(r.wallet_balance,0), COALESCE(r.outstanding_cancellation_fee,0) FROM riders r WHERE r.user_id=$1`

// loadRiderProfile reads the caller's rider profile, creating the riders
// row first for a rider token that doesn't have one yet (see
// ensureRiderProfile). Returns errNoRiderProfile only for a panel token
// with no row; any other error is a real database failure.
func loadRiderProfile(ctx context.Context, q querier, userID, panel string) (riderProfile, error) {
	var p riderProfile
	scan := func() error {
		return q.QueryRow(ctx, riderProfileSQL, userID).Scan(
			&p.RiderID, &p.Phone, &p.Rating, &p.TotalRides, &p.WalletBalance, &p.OutstandingCancellationFee)
	}
	err := scan()
	if !errors.Is(err, pgx.ErrNoRows) {
		return p, err
	}
	if panel != "" {
		return p, errNoRiderProfile
	}
	if _, err := ensureRiderProfile(ctx, q, userID); err != nil {
		return p, err
	}
	return p, scan()
}
