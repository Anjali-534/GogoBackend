package handlers

import (
	"math"
	"net/http"
	"testing"
)

var testArrivalCfg = trackerArrivalConfig{RadiusM: 500, AccuracyCapM: 200}

func TestTrackerArrivalCheck(t *testing.T) {
	cases := []struct {
		name               string
		distance, accuracy float64
		want               bool
	}{
		{"at drop point", 0, 5, true},
		{"well inside", 300, 10, true},
		{"exactly on radius", 500, 0, true},
		{"just outside, no accuracy", 500.5, 0, false},
		{"620 m with 150 m accuracy", 620, 150, true},
		{"650 m with 150 m accuracy, edge", 650, 150, true},
		{"651 m with 150 m accuracy", 651, 150, false},
		{"accuracy capped at 200: 700 m / 900 m acc", 700, 900, true},
		{"accuracy capped at 200: 701 m / 900 m acc", 701, 900, false},
		{"negative accuracy is no credit", 520, -50, false},
		{"NaN accuracy is no credit", 520, math.NaN(), false},
		{"Inf accuracy is no credit", 520, math.Inf(1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := trackerArrivalCheck(tc.distance, tc.accuracy, testArrivalCfg); got != tc.want {
				t.Fatalf("trackerArrivalCheck(%v, %v) = %v, want %v", tc.distance, tc.accuracy, got, tc.want)
			}
		})
	}
}

func f64(v float64) *float64 { return &v }

// Drop point in Connaught Place; offsets below are along a meridian, where
// 1e-5 degree of latitude is ~1.11 m.
var testStop = trackerDriverStop{OrderID: "o1", Status: "in_transit", ToLat: f64(28.6315), ToLng: f64(77.2167)}

// latNorth returns a latitude ~meters north of the test drop point.
func latNorth(meters float64) *float64 {
	return f64(*testStop.ToLat + meters/111195.0)
}

func TestAssessTrackerDriverArrival(t *testing.T) {
	t.Run("no coordinates: accepted and flagged no_location", func(t *testing.T) {
		a, rej := assessTrackerDriverArrival(trackerDriverEventRequest{Kind: "reached"}, testStop, testArrivalCfg)
		if rej != nil || !a.NoLocation || a.OutsideRadius || a.DistanceM != nil {
			t.Fatalf("got %+v rej=%+v", a, rej)
		}
	})

	t.Run("only lat sent counts as no location", func(t *testing.T) {
		a, rej := assessTrackerDriverArrival(trackerDriverEventRequest{Kind: "reached", Lat: f64(28.6)}, testStop, testArrivalCfg)
		if rej != nil || !a.NoLocation {
			t.Fatalf("got %+v rej=%+v", a, rej)
		}
	})

	t.Run("out-of-range coordinates rejected", func(t *testing.T) {
		_, rej := assessTrackerDriverArrival(trackerDriverEventRequest{Kind: "reached", Lat: f64(95), Lng: f64(77)}, testStop, testArrivalCfg)
		if rej == nil || rej.Status != http.StatusBadRequest {
			t.Fatalf("want 400, got %+v", rej)
		}
	})

	t.Run("inside radius accepted with distance recorded", func(t *testing.T) {
		a, rej := assessTrackerDriverArrival(trackerDriverEventRequest{Kind: "reached", Lat: latNorth(300), Lng: testStop.ToLng, AccuracyM: f64(12)}, testStop, testArrivalCfg)
		if rej != nil || a.OutsideRadius || a.NoLocation {
			t.Fatalf("got %+v rej=%+v", a, rej)
		}
		if a.DistanceM == nil || math.Abs(*a.DistanceM-300) > 2 {
			t.Fatalf("distance = %v, want ~300", a.DistanceM)
		}
		if a.AccuracyM == nil || *a.AccuracyM != 12 {
			t.Fatalf("accuracy = %v, want 12", a.AccuracyM)
		}
	})

	t.Run("accuracy credit lets 620 m through", func(t *testing.T) {
		a, rej := assessTrackerDriverArrival(trackerDriverEventRequest{Kind: "reached", Lat: latNorth(620), Lng: testStop.ToLng, AccuracyM: f64(150)}, testStop, testArrivalCfg)
		if rej != nil || a.OutsideRadius {
			t.Fatalf("got %+v rej=%+v", a, rej)
		}
	})

	t.Run("reached outside radius without override: 422 with distance", func(t *testing.T) {
		_, rej := assessTrackerDriverArrival(trackerDriverEventRequest{Kind: "reached", Lat: latNorth(700), Lng: testStop.ToLng, AccuracyM: f64(10)}, testStop, testArrivalCfg)
		if rej == nil || rej.Status != http.StatusUnprocessableEntity {
			t.Fatalf("want 422, got %+v", rej)
		}
		if d, _ := rej.Body["distance_m"].(float64); math.Abs(d-700) > 2 {
			t.Fatalf("distance_m = %v, want ~700", rej.Body["distance_m"])
		}
		if rej.Body["code"] != "outside_arrival_radius" {
			t.Fatalf("code = %v", rej.Body["code"])
		}
	})

	t.Run("override needs a reason", func(t *testing.T) {
		_, rej := assessTrackerDriverArrival(trackerDriverEventRequest{Kind: "reached", Lat: latNorth(700), Lng: testStop.ToLng, Override: true, Reason: "  ok "}, testStop, testArrivalCfg)
		if rej == nil || rej.Status != http.StatusBadRequest {
			t.Fatalf("want 400, got %+v", rej)
		}
	})

	t.Run("override with reason accepted and flagged", func(t *testing.T) {
		a, rej := assessTrackerDriverArrival(trackerDriverEventRequest{Kind: "reached", Lat: latNorth(700), Lng: testStop.ToLng, Override: true, Reason: "  Gate 3 of the campus  "}, testStop, testArrivalCfg)
		if rej != nil || !a.OutsideRadius || a.Reason != "Gate 3 of the campus" {
			t.Fatalf("got %+v rej=%+v", a, rej)
		}
	})

	t.Run("delivery_claimed outside radius is recorded, never rejected", func(t *testing.T) {
		a, rej := assessTrackerDriverArrival(trackerDriverEventRequest{Kind: "delivery_claimed", Lat: latNorth(900), Lng: testStop.ToLng}, testStop, testArrivalCfg)
		if rej != nil || !a.OutsideRadius || a.DistanceM == nil {
			t.Fatalf("got %+v rej=%+v", a, rej)
		}
	})

	t.Run("stop without drop coordinates: accepted, no distance", func(t *testing.T) {
		stop := trackerDriverStop{OrderID: "o2", Status: "in_transit"}
		a, rej := assessTrackerDriverArrival(trackerDriverEventRequest{Kind: "reached", Lat: f64(28.6), Lng: f64(77.2)}, stop, testArrivalCfg)
		if rej != nil || a.OutsideRadius || a.NoLocation || a.DistanceM != nil || a.Lat == nil {
			t.Fatalf("got %+v rej=%+v", a, rej)
		}
	})

	t.Run("invalid accuracy stored as null", func(t *testing.T) {
		a, _ := assessTrackerDriverArrival(trackerDriverEventRequest{Kind: "reached", Lat: latNorth(10), Lng: testStop.ToLng, AccuracyM: f64(-1)}, testStop, testArrivalCfg)
		if a.AccuracyM != nil {
			t.Fatalf("accuracy = %v, want nil", *a.AccuracyM)
		}
	})
}
