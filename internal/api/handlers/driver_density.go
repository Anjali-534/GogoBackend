package handlers

import (
	"context"
	"math"
	"net/http"
	"strconv"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
)

// GET /gogoo/drivers/nearby-count?lat&lng&category=cab&radius=5000
// Rider-facing "how many drivers nearby" density feed for the searching-for-
// driver screen. Deliberately never returns individual driver identities or
// exact coordinates — positions are bucketed onto a coarse grid so this
// reads as a heatmap density, not a live-tracking feed (that stays a
// panel-only privilege via /gogoo/live/drivers).
func GetNearbyDriverCount(c *gin.Context) {
	lat, latErr := strconv.ParseFloat(c.Query("lat"), 64)
	lng, lngErr := strconv.ParseFloat(c.Query("lng"), 64)
	if latErr != nil || lngErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "lat and lng required"})
		return
	}
	category := c.DefaultQuery("category", "cab")
	radiusM, err := strconv.ParseFloat(c.DefaultQuery("radius", "5000"), 64)
	if err != nil || radiusM <= 0 {
		radiusM = 5000
	}
	radiusKm := radiusM / 1000.0

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT ROUND(current_lat::numeric, 2), ROUND(current_lng::numeric, 2)
		FROM drivers
		WHERE COALESCE(is_online, FALSE) = TRUE
		  AND COALESCE(is_blocked, FALSE) = FALSE
		  AND vehicle_category = $1
		  AND current_lat IS NOT NULL AND current_lng IS NOT NULL
		  AND (
			6371 * acos(
				LEAST(1.0, cos(radians($2::float)) * cos(radians(current_lat)) *
				cos(radians(current_lng) - radians($3::float)) +
				sin(radians($2::float)) * sin(radians(current_lat)))
			)
		  ) <= $4
	`, category, lat, lng, radiusKm)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	defer rows.Close()

	type cell struct {
		lat, lng float64
	}
	grid := map[cell]int{}
	total := 0
	for rows.Next() {
		var gLat, gLng float64
		if err := rows.Scan(&gLat, &gLng); err != nil {
			continue
		}
		total++
		grid[cell{lat: gLat, lng: gLng}]++
	}

	gridOut := make([]gin.H, 0, len(grid))
	for c, count := range grid {
		gridOut = append(gridOut, gin.H{
			"lat":   math.Round(c.lat*100) / 100,
			"lng":   math.Round(c.lng*100) / 100,
			"count": count,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"total_nearby": total,
		"grid":         gridOut,
	})
}
