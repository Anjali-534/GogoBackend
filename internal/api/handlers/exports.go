package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/deploykit/backend/internal/dateutil"
	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
)

// ============================================================
// GET /gogoo/riders?range=&from=&to=&sort= — list of all riders (dashboard)
// ============================================================
func ListRiders(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	query := `
		SELECT r.id, COALESCE(r.user_id::text,''), u.name, u.email, r.phone,
		       r.rating, r.total_rides, r.created_at
		FROM riders r JOIN users u ON u.id = r.user_id`
	args := []interface{}{}
	if rangeKey := c.Query("range"); rangeKey != "" {
		_, dr := dateutil.Resolve(rangeKey, time.Time{}, c.Query("from"), c.Query("to"))
		args = append(args, dr.Start, dr.End)
		query += " WHERE r.created_at >= $1 AND r.created_at <= $2"
	}
	query += " ORDER BY r.created_at " + dateutil.ParseSort(c.Query("sort")) + " LIMIT 500"

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	var riders []map[string]interface{}
	for rows.Next() {
		var id, userID, name, email, phone string
		var rating float64
		var totalRides int
		var createdAt time.Time
		rows.Scan(&id, &userID, &name, &email, &phone, &rating, &totalRides, &createdAt)
		riders = append(riders, map[string]interface{}{
			"id": id, "user_id": userID, "name": name, "email": email, "phone": phone,
			"rating": rating, "total_rides": totalRides, "created_at": createdAt,
		})
	}
	c.JSON(http.StatusOK, riders)
}

// writeXLSX streams an excelize file to the HTTP response as a download.
func writeXLSX(c *gin.Context, f *excelize.File, filename string) {
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	if err := f.Write(c.Writer); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to write excel"})
	}
}

// headerStyle returns a bold, filled header style id for a sheet.
func headerStyle(f *excelize.File) int {
	style, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"FF6B2B"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center"},
	})
	return style
}

// ============================================================
// GET /gogoo/export/drivers.xlsx
// ============================================================
func ExportDriversXLSX(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT u.name, COALESCE(u.email,''), COALESCE(d.phone,''),
		       COALESCE(d.vehicle_category,''), COALESCE(d.vehicle_type,''),
		       COALESCE(d.vehicle_number,''), COALESCE(d.vehicle_model,''),
		       COALESCE(d.license_number,''),
		       d.is_verified, d.is_online,
		       COALESCE(d.rating,0), COALESCE(d.total_rides,0), COALESCE(d.total_earnings,0),
		       COALESCE(d.bank_account_holder,''), COALESCE(d.bank_account_number,''),
		       COALESCE(d.bank_ifsc,''), COALESCE(d.upi_id,''),
		       d.created_at
		FROM drivers d JOIN users u ON u.id = d.user_id
		ORDER BY d.created_at DESC
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	f := excelize.NewFile()
	sheet := "Drivers"
	f.SetSheetName("Sheet1", sheet)

	headers := []string{
		"Name", "Email", "Mobile No.", "Category", "Vehicle Type",
		"Vehicle Number", "Vehicle Model", "License No.",
		"Verified", "Online", "Rating", "Total Rides", "Total Earnings (₹)",
		"Bank A/C Holder", "Bank A/C Number", "IFSC", "UPI ID", "Joined On",
	}
	hs := headerStyle(f)
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sheet, cell, h)
		f.SetCellStyle(sheet, cell, cell, hs)
	}

	rowIdx := 2
	for rows.Next() {
		var name, email, phone, vCat, vType, vNum, vModel, license string
		var verified, online bool
		var rating, earnings float64
		var totalRides int
		var bankHolder, bankNum, ifsc, upi string
		var createdAt time.Time
		rows.Scan(&name, &email, &phone, &vCat, &vType, &vNum, &vModel, &license,
			&verified, &online, &rating, &totalRides, &earnings,
			&bankHolder, &bankNum, &ifsc, &upi, &createdAt)

		vals := []interface{}{
			name, email, phone, vCat, vType, vNum, vModel, license,
			yesNo(verified), yesNo(online), rating, totalRides, earnings,
			bankHolder, bankNum, ifsc, upi, createdAt.Format("2006-01-02 15:04"),
		}
		for i, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(i+1, rowIdx)
			f.SetCellValue(sheet, cell, v)
		}
		rowIdx++
	}

	// Reasonable column widths.
	f.SetColWidth(sheet, "A", "A", 22)
	f.SetColWidth(sheet, "B", "B", 28)
	f.SetColWidth(sheet, "C", "C", 16)
	f.SetColWidth(sheet, "D", "R", 16)

	writeXLSX(c, f, fmt.Sprintf("bogie-drivers-%s.xlsx", time.Now().Format("2006-01-02")))
}

// ============================================================
// GET /gogoo/export/users.xlsx  (riders)
// ============================================================
func ExportUsersXLSX(c *gin.Context) {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT u.name, COALESCE(u.email,''), COALESCE(r.phone,''),
		       COALESCE(r.rating,0), COALESCE(r.total_rides,0), r.created_at
		FROM riders r JOIN users u ON u.id = r.user_id
		ORDER BY r.created_at DESC
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	f := excelize.NewFile()
	sheet := "Users"
	f.SetSheetName("Sheet1", sheet)

	headers := []string{"Name", "Email", "Mobile No.", "Rating", "Total Rides", "Joined On"}
	hs := headerStyle(f)
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sheet, cell, h)
		f.SetCellStyle(sheet, cell, cell, hs)
	}

	rowIdx := 2
	for rows.Next() {
		var name, email, phone string
		var rating float64
		var totalRides int
		var createdAt time.Time
		rows.Scan(&name, &email, &phone, &rating, &totalRides, &createdAt)
		vals := []interface{}{name, email, phone, rating, totalRides, createdAt.Format("2006-01-02 15:04")}
		for i, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(i+1, rowIdx)
			f.SetCellValue(sheet, cell, v)
		}
		rowIdx++
	}

	f.SetColWidth(sheet, "A", "A", 22)
	f.SetColWidth(sheet, "B", "B", 28)
	f.SetColWidth(sheet, "C", "C", 16)
	f.SetColWidth(sheet, "D", "F", 16)

	writeXLSX(c, f, fmt.Sprintf("bogie-users-%s.xlsx", time.Now().Format("2006-01-02")))
}

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}
