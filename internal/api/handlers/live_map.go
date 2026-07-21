package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
)

// GET /gogoo/live/drivers?category=cab|truck|ambulance
// Online drivers with a live GPS fix, for admin-panel live maps.
func ListLiveDrivers(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	category := c.Query("category")

	rows, err := pool.Query(ctx, `
        SELECT
            d.id,
            COALESCE(u.name,'')          AS name,
            COALESCE(d.vehicle_type,'')  AS vehicle_type,
            COALESCE(d.vehicle_number,'') AS vehicle_number,
            d.current_lat,
            d.current_lng,
            d.location_updated_at,
            COALESCE(d.rating,      0) AS rating,
            COALESCE(d.total_rides, 0) AS total_rides,
            (SELECT id FROM bookings
              WHERE driver_id = d.id AND status NOT IN ('completed','cancelled')
              ORDER BY created_at DESC LIMIT 1) AS active_booking_id
        FROM drivers d
        LEFT JOIN users u ON u.id = d.user_id
        WHERE COALESCE(d.is_online, FALSE) = TRUE
          AND d.current_lat IS NOT NULL
          AND d.current_lng IS NOT NULL
        ORDER BY d.location_updated_at DESC NULLS LAST
        LIMIT 500`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	defer rows.Close()

	var out []gin.H
	for rows.Next() {
		var id, name, vType, vNum string
		var lat, lng *float64
		var locUpdatedAt *time.Time
		var rating float64
		var totalRides int
		var activeBookingID *string
		if err := rows.Scan(&id, &name, &vType, &vNum, &lat, &lng, &locUpdatedAt, &rating, &totalRides, &activeBookingID); err != nil {
			continue
		}
		cat := vehicleCategoryFromType(vType)
		if category != "" && cat != category {
			continue
		}
		out = append(out, gin.H{
			"id":                  id,
			"name":                name,
			"vehicle_type":        vType,
			"vehicle_category":    cat,
			"vehicle_number":      vNum,
			"current_lat":         lat,
			"current_lng":         lng,
			"location_updated_at": locUpdatedAt,
			"rating":              rating,
			"total_rides":         totalRides,
			"active_booking_id":   activeBookingID,
		})
	}
	if out == nil {
		out = []gin.H{}
	}
	c.JSON(http.StatusOK, out)
}

// GET /gogoo/live/bookings?category=cab|truck|ambulance
// Active bookings (searching/accepted/arriving/in_progress), for admin-panel live maps.
func ListLiveBookings(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()
	category := c.Query("category")

	args := []interface{}{}
	catFilter := ""
	if category != "" {
		args = append(args, category)
		catFilter = "AND COALESCE(st.category,'') = $1"
	}

	rows, err := pool.Query(ctx, `
        SELECT b.id, b.status,
               b.pickup_lat, b.pickup_lng, b.pickup_address,
               b.drop_lat, b.drop_lng, b.drop_address,
               b.driver_lat, b.driver_lng, b.driver_updated_at,
               COALESCE(u_r.name,'') AS rider_name,
               COALESCE(du.name,'')  AS driver_name,
               COALESCE(st.name,'')  AS service_name,
               COALESCE(st.category,'') AS category
        FROM bookings b
        LEFT JOIN riders  r   ON r.id  = b.rider_id
        LEFT JOIN users   u_r ON u_r.id = r.user_id
        LEFT JOIN drivers d   ON d.id  = b.driver_id
        LEFT JOIN users   du  ON du.id = d.user_id
        LEFT JOIN service_types st ON st.id = b.service_type_id
        WHERE b.status IN ('searching','accepted','arriving','in_progress')
        `+catFilter+`
        ORDER BY b.requested_at DESC
        LIMIT 200`, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	defer rows.Close()

	var out []gin.H
	for rows.Next() {
		var id, status, pAddr, dAddr, riderName, driverName, serviceName, cat string
		var pLat, pLng, dLat, dLng float64
		var driverLat, driverLng *float64
		var driverUpdatedAt *time.Time
		if err := rows.Scan(&id, &status, &pLat, &pLng, &pAddr, &dLat, &dLng, &dAddr,
			&driverLat, &driverLng, &driverUpdatedAt, &riderName, &driverName, &serviceName, &cat); err != nil {
			continue
		}
		item := gin.H{
			"id":           id,
			"status":       status,
			"pickup":       gin.H{"lat": pLat, "lng": pLng, "address": pAddr},
			"drop":         gin.H{"lat": dLat, "lng": dLng, "address": dAddr},
			"rider_name":   riderName,
			"driver_name":  driverName,
			"service_name": serviceName,
			"category":     cat,
		}
		if driverLat != nil && driverLng != nil {
			driverInfo := gin.H{"lat": *driverLat, "lng": *driverLng}
			if driverUpdatedAt != nil {
				driverInfo["updated_at"] = *driverUpdatedAt
			}
			item["driver"] = driverInfo
		}
		out = append(out, item)
	}
	if out == nil {
		out = []gin.H{}
	}
	c.JSON(http.StatusOK, gin.H{"bookings": out})
}

// fetchOlaDirections calls Ola Maps directions server-side (OLA_MAPS_KEY) and
// returns the encoded overview polyline plus distance/duration. Shared by the
// ProxyOlaRoute endpoint and the tracker's route-cache-on-create (tracker.go),
// which stores the result on the order so the unauthenticated tracking pages
// never need a directions call of their own.
func fetchOlaDirections(from, to string) (polyline string, distanceKm float64, durationMins int, err error) {
	if from == "" || to == "" || !isLatLng(from) || !isLatLng(to) {
		return "", 0, 0, fmt.Errorf("invalid from/to coordinates")
	}

	apiKey := os.Getenv("OLA_MAPS_KEY")
	if apiKey == "" {
		return "", 0, 0, fmt.Errorf("OLA_MAPS_KEY not set")
	}

	url := "https://api.olamaps.io/routing/v1/directions?origin=" + from +
		"&destination=" + to + "&api_key=" + apiKey

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(nil))
	if err != nil {
		return "", 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", 0, 0, fmt.Errorf("ola directions returned status %d", resp.StatusCode)
	}

	var result struct {
		Routes []struct {
			OverviewPolyline string `json:"overview_polyline"`
			Legs             []struct {
				Distance float64 `json:"distance"`
				Duration float64 `json:"duration"`
			} `json:"legs"`
		} `json:"routes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", 0, 0, err
	}
	if len(result.Routes) == 0 {
		return "", 0, 0, fmt.Errorf("ola directions returned no routes")
	}

	route := result.Routes[0]
	if len(route.Legs) > 0 {
		distanceKm = route.Legs[0].Distance / 1000
		durationMins = int(route.Legs[0].Duration / 60)
	}
	return route.OverviewPolyline, distanceKm, durationMins, nil
}

// GET /gogoo/route?from=lat,lng&to=lat,lng
// Server-side proxy to Ola Maps directions so the Ola key never reaches panel frontends.
// Always returns 200 — degrades to an empty polyline on any failure so callers can
// fall back to a straight line without treating this as an error.
func ProxyOlaRoute(c *gin.Context) {
	polyline, distanceKm, durationMins, err := fetchOlaDirections(c.Query("from"), c.Query("to"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"polyline": "", "distance_km": 0.0, "duration_mins": 0})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"polyline":      polyline,
		"distance_km":   distanceKm,
		"duration_mins": durationMins,
	})
}

// ── Reverse geocode cache ────────────────────────────────────────────────
// Keyed by lat/lng rounded to 3 decimals (~100m), so a driver drifting
// slightly within the same block reuses one Ola lookup instead of firing a
// new one per popup open. Reset wholesale once it hits the cap — panel
// traffic is low enough that a simple reset beats tracking LRU order.
var (
	geocodeCacheMu sync.Mutex
	geocodeCache   = map[string]string{}
)

const geocodeCacheMax = 5000

// GET /gogoo/geocode/reverse?lat=..&lng=..
// Server-side proxy to Ola Maps reverse geocoding so the Ola key never
// reaches panel frontends, and so per-marker-click traffic across every
// panel doesn't turn into direct, unbounded Ola API spend.
// Always returns 200 — degrades to an empty address on any failure so
// callers can show "Location unavailable" without treating this as an error.
func ReverseGeocodeProxy(c *gin.Context) {
	empty := gin.H{"address": ""}

	lat, errLat := strconv.ParseFloat(c.Query("lat"), 64)
	lng, errLng := strconv.ParseFloat(c.Query("lng"), 64)
	if errLat != nil || errLng != nil {
		c.JSON(http.StatusOK, empty)
		return
	}

	cacheKey := fmt.Sprintf("%.3f,%.3f", lat, lng)

	geocodeCacheMu.Lock()
	cached, ok := geocodeCache[cacheKey]
	geocodeCacheMu.Unlock()
	if ok {
		c.JSON(http.StatusOK, gin.H{"address": cached})
		return
	}

	apiKey := os.Getenv("OLA_MAPS_KEY")
	if apiKey == "" {
		c.JSON(http.StatusOK, empty)
		return
	}

	url := fmt.Sprintf("https://api.olamaps.io/places/v1/reverse-geocode?latlng=%f,%f&api_key=%s", lat, lng, apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		c.JSON(http.StatusOK, empty)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		c.JSON(http.StatusOK, empty)
		return
	}

	var result struct {
		Results []struct {
			FormattedAddress string `json:"formatted_address"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result.Results) == 0 {
		c.JSON(http.StatusOK, empty)
		return
	}

	address := result.Results[0].FormattedAddress

	geocodeCacheMu.Lock()
	if len(geocodeCache) >= geocodeCacheMax {
		geocodeCache = map[string]string{}
	}
	geocodeCache[cacheKey] = address
	geocodeCacheMu.Unlock()

	c.JSON(http.StatusOK, gin.H{"address": address})
}

// fetchOlaForwardGeocode calls Ola Maps forward geocoding server-side
// (OLA_MAPS_KEY) and returns the first result's coordinates. Shared by
// ForwardGeocodeProxy and the tracker's geocode-on-create fallback
// (tracker.go), same split as fetchOlaDirections/ProxyOlaRoute above.
func fetchOlaForwardGeocode(address string) (lat, lng float64, err error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return 0, 0, fmt.Errorf("empty address")
	}

	apiKey := os.Getenv("OLA_MAPS_KEY")
	if apiKey == "" {
		return 0, 0, fmt.Errorf("OLA_MAPS_KEY not set")
	}

	reqURL := "https://api.olamaps.io/places/v1/geocode?address=" + url.QueryEscape(address) + "&api_key=" + apiKey

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(reqURL)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0, 0, fmt.Errorf("ola geocode returned status %d", resp.StatusCode)
	}

	var result struct {
		GeocodingResults []struct {
			Geometry struct {
				Location struct {
					Lat float64 `json:"lat"`
					Lng float64 `json:"lng"`
				} `json:"location"`
			} `json:"geometry"`
		} `json:"geocodingResults"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, 0, err
	}
	if len(result.GeocodingResults) == 0 {
		return 0, 0, fmt.Errorf("ola geocode returned no results")
	}

	loc := result.GeocodingResults[0].Geometry.Location
	return loc.Lat, loc.Lng, nil
}

// GET /gogoo/geocode/forward?address=<text>
// Server-side proxy to Ola Maps forward geocoding so the Ola key never
// reaches panel frontends. No cache here unlike the reverse-geocode proxy
// above — that one absorbs repeated marker clicks on the same handful of
// driver locations; free-text addresses rarely repeat, so a cache would
// just grow unbounded for no hit-rate benefit.
// Always returns 200 — degrades to null lat/lng on any failure so callers
// can treat "not found" the same as "geocoding unavailable" without a
// separate error path.
func ForwardGeocodeProxy(c *gin.Context) {
	lat, lng, err := fetchOlaForwardGeocode(c.Query("address"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"lat": nil, "lng": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"lat": lat, "lng": lng})
}

// isLatLng validates a "lat,lng" query param before it's interpolated into an outbound URL.
func isLatLng(s string) bool {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		if _, err := strconv.ParseFloat(strings.TrimSpace(p), 64); err != nil {
			return false
		}
	}
	return true
}
