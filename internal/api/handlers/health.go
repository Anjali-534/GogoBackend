package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/deploykit/backend/internal/db"
)

type HealthResponse struct {
	Status    string        `json:"status"`
	Timestamp time.Time     `json:"timestamp"`
	Database  string        `json:"database"`
	Version   string        `json:"version"`
}

// Health checks if the service is healthy
func Health(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pool := db.GetDB().GetPool()
	dbStatus := "healthy"

	if err := pool.Ping(ctx); err != nil {
		dbStatus = "unhealthy"
	}

	status := "healthy"
	if dbStatus != "healthy" {
		status = "degraded"
	}

	c.JSON(http.StatusOK, HealthResponse{
		Status:    status,
		Timestamp: time.Now(),
		Database:  dbStatus,
		Version:   "0.1.0",
	})
}

// Ready checks if the service is ready to handle requests
func Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pool := db.GetDB().GetPool()

	if err := pool.Ping(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}
