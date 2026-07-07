package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/deploykit/backend/internal/db"
	"github.com/deploykit/backend/internal/services/ledger"
	"github.com/gin-gonic/gin"
)

// GET /gogoo/driver/ledger/pdf?month=YYYY-MM
// Driver identity always comes from the JWT (user_id -> drivers.id); a
// driver can never request another driver's statement by manipulating a
// param. Reuses the exact same Statement/PDF builders as the monthly
// automated emailer — one source of truth for what a statement looks like.
func GetDriverLedgerPDF(c *gin.Context) {
	userID := c.GetString("user_id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var driverID string
	if err := pool.QueryRow(ctx, `SELECT id FROM drivers WHERE user_id = $1`, userID).Scan(&driverID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "driver profile not found"})
		return
	}

	month := c.Query("month")
	if month == "" {
		month = time.Now().Format("2006-01")
	}
	if _, err := time.Parse("2006-01", month); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid month, expected YYYY-MM"})
		return
	}

	stmt, err := ledger.BuildStatement(ctx, pool, driverID, month)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build statement"})
		return
	}

	pdfBytes, err := ledger.GeneratePDF(stmt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate pdf"})
		return
	}

	filename := fmt.Sprintf("gogoo-ledger-%s-%s.pdf", driverID, month)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}
