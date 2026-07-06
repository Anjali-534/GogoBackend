package handlers

import (
	"context"
	"log"
	"time"

	"github.com/deploykit/backend/internal/db"
)

// StartScheduledDispatcher ticks every 60s and flips scheduled bookings whose
// pickup time is within 15 minutes into 'searching', handing them to the
// normal driver-matching flow (polling + push notifications) — no parallel
// dispatch path. Meant to be started once with `go handlers.StartScheduledDispatcher()`
// from main.go; a panic in one tick is recovered so it never kills dispatch
// for everyone else.
func StartScheduledDispatcher() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		dispatchDueScheduledBookings()
	}
}

func dispatchDueScheduledBookings() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("scheduled dispatcher: recovered from panic: %v", r)
		}
	}()

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT b.id, COALESCE(st.category,''), b.pickup_address, COALESCE(b.estimated_fare,0)
		FROM bookings b
		JOIN service_types st ON st.id = b.service_type_id
		WHERE b.status = 'scheduled'
		  AND b.scheduled_at <= NOW() + INTERVAL '15 minutes'
	`)
	if err != nil {
		log.Printf("scheduled dispatcher: query error: %v", err)
		return
	}

	type due struct {
		id, category, pickupAddress string
		fare                        float64
	}
	var toDispatch []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.category, &d.pickupAddress, &d.fare); err != nil {
			continue
		}
		toDispatch = append(toDispatch, d)
	}
	rows.Close()

	for _, d := range toDispatch {
		ct, err := pool.Exec(ctx, `UPDATE bookings SET status='searching', updated_at=NOW() WHERE id=$1 AND status='scheduled'`, d.id)
		if err != nil {
			log.Printf("scheduled dispatcher: failed to dispatch booking %s: %v", d.id, err)
			continue
		}
		if ct.RowsAffected() == 0 {
			continue // already dispatched/cancelled by someone else since the SELECT
		}
		log.Printf("scheduled dispatcher: dispatched booking %s (category=%s)", d.id, d.category)
		notifyDriversOfNewRide(d.id, d.category, d.pickupAddress, d.fare)
	}
}
