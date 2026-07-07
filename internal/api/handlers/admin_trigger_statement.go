package handlers

import (
	"net/http"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/services/ledger"
	"github.com/gin-gonic/gin"
)

// POST /gogoo/admin/trigger-monthly-statements-test — master-admin-only,
// TEMPORARY. Runs the exact same send-one-statement path the monthly
// goroutine uses, on demand, for one driver, so we can verify PDF+email+
// idempotency end-to-end without waiting for the 1st of the month. Remove
// this endpoint once verified.
func TriggerMonthlyStatementTest(c *gin.Context) {
	var req struct {
		DriverID string `json:"driver_id"`
		Month    string `json:"month"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.DriverID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body must include a non-empty \"driver_id\""})
		return
	}

	month := req.Month
	if month == "" {
		month = time.Now().AddDate(0, -1, 0).Format("2006-01")
	}
	if _, err := time.Parse("2006-01", month); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid month, expected YYYY-MM"})
		return
	}

	cfg := c.MustGet("config").(*config.Config)
	sent, skipped, err := ledger.TriggerOneStatement(cfg, req.DriverID, month)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sent": sent, "skipped_already_sent": skipped, "month": month})
}
