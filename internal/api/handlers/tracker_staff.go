package handlers

// Bogie Tracker — owner-only staff-login management. Staff logins share the
// owner's full company-scoped access everywhere else (see tracker.go's
// scoping comment) — these are the only routes gated additionally by
// middleware.RequireTrackerOwner(), since managing other logins is the one
// thing staff can't do.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/deploykit/backend/internal/db"
	"github.com/deploykit/backend/internal/services/trackerbilling"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

type trackerStaffUser struct {
	ID         string     `json:"id"`
	Email      string     `json:"email"`
	CreatedAt  time.Time  `json:"created_at"`
	DisabledAt *time.Time `json:"disabled_at"`
}

// GET /gogoo/tracker/staff
func ListTrackerStaffUsers(c *gin.Context) {
	companyID := c.GetString("company_id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var currentPlan *string
	if err := pool.QueryRow(ctx,
		`SELECT current_plan FROM tracker_companies WHERE id=$1`, companyID,
	).Scan(&currentPlan); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}

	rows, err := pool.Query(ctx, `
		SELECT id, email, created_at, disabled_at FROM tracker_staff_users
		WHERE company_id = $1 ORDER BY created_at ASC
	`, companyID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	defer rows.Close()

	staff := []trackerStaffUser{}
	activeCount := 0
	for rows.Next() {
		var s trackerStaffUser
		if err := rows.Scan(&s.ID, &s.Email, &s.CreatedAt, &s.DisabledAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
			return
		}
		if s.DisabledAt == nil {
			activeCount++
		}
		staff = append(staff, s)
	}

	resp := gin.H{"staff": staff, "count": activeCount}
	if currentPlan != nil {
		if cap, unlimited, ok := trackerbilling.PanelLoginStaffCap(*currentPlan); ok {
			resp["unlimited"] = unlimited
			if !unlimited {
				resp["limit"] = cap
			}
		}
	}
	c.JSON(http.StatusOK, resp)
}

// POST /gogoo/tracker/staff
func CreateTrackerStaffUser(c *gin.Context) {
	companyID := c.GetString("company_id")

	var req struct {
		Email    string `json:"email" binding:"required,email"`
		Password string `json:"password" binding:"required,min=8"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var currentPlan *string
	if err := pool.QueryRow(ctx,
		`SELECT current_plan FROM tracker_companies WHERE id=$1`, companyID,
	).Scan(&currentPlan); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	if currentPlan == nil {
		c.JSON(http.StatusConflict, gin.H{
			"error": "your plan doesn't include additional logins — upgrade to add team members",
			"code":  "staff_limit_reached",
		})
		return
	}

	cap, unlimited, ok := trackerbilling.PanelLoginStaffCap(*currentPlan)
	if ok && !unlimited {
		var count int
		if err := pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM tracker_staff_users WHERE company_id=$1 AND disabled_at IS NULL`, companyID,
		).Scan(&count); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
			return
		}
		if count >= cap {
			msg := "your plan doesn't include additional logins — upgrade to add team members"
			if cap > 0 {
				msg = fmt.Sprintf("your plan allows up to %d staff login(s) — upgrade to add more", cap)
			}
			c.JSON(http.StatusConflict, gin.H{"error": msg, "code": "staff_limit_reached"})
			return
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}

	var staff trackerStaffUser
	err = pool.QueryRow(ctx, `
		INSERT INTO tracker_staff_users (company_id, email, password_hash, created_by)
		VALUES ($1, $2, $3, $1)
		RETURNING id, email, created_at
	`, companyID, req.Email, string(hash)).Scan(&staff.ID, &staff.Email, &staff.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "23505") {
			c.JSON(http.StatusConflict, gin.H{"error": "a staff login with this email already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}

	c.JSON(http.StatusCreated, staff)
}

// POST /gogoo/tracker/staff/:id/reactivate — re-enables a staff login that
// was auto-disabled by a plan downgrade (see disableExcessTrackerStaff).
// Never automatic: the owner must explicitly do this, and only once there's
// a free seat under the current plan again.
func ReactivateTrackerStaffUser(c *gin.Context) {
	companyID := c.GetString("company_id")
	staffID := c.Param("id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var currentPlan *string
	if err := pool.QueryRow(ctx,
		`SELECT current_plan FROM tracker_companies WHERE id=$1`, companyID,
	).Scan(&currentPlan); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}

	if currentPlan != nil {
		if cap, unlimited, ok := trackerbilling.PanelLoginStaffCap(*currentPlan); ok && !unlimited {
			var count int
			if err := pool.QueryRow(ctx,
				`SELECT COUNT(*) FROM tracker_staff_users WHERE company_id=$1 AND disabled_at IS NULL`, companyID,
			).Scan(&count); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
				return
			}
			if count >= cap {
				c.JSON(http.StatusConflict, gin.H{
					"error": "your plan doesn't have a free seat for this login — upgrade or remove another staff login first",
					"code":  "staff_limit_reached",
				})
				return
			}
		}
	}

	tag, err := pool.Exec(ctx,
		`UPDATE tracker_staff_users SET disabled_at = NULL WHERE id=$1 AND company_id=$2`, staffID, companyID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "staff login not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "staff login reactivated"})
}

// DELETE /gogoo/tracker/staff/:id
func DeleteTrackerStaffUser(c *gin.Context) {
	companyID := c.GetString("company_id")
	staffID := c.Param("id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	tag, err := pool.Exec(ctx,
		`DELETE FROM tracker_staff_users WHERE id=$1 AND company_id=$2`, staffID, companyID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error: " + err.Error()})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "staff login not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "staff login removed"})
}
