package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/deploykit/backend/internal/auth"
	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
)

// AuthMiddleware validates JWT tokens
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
			c.Abort()
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid authorization header"})
			c.Abort()
			return
		}

		token := parts[1]
		claims, err := auth.ValidateToken(token)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			c.Abort()
			return
		}

		c.Set("user_id", claims.UserID.String())
		c.Set("user_email", claims.Email)
		c.Set("user_name", claims.Name)
		c.Set("panel", claims.Panel)
		c.Set("role", claims.Role)
		c.Set("jwt_company_id", claims.CompanyID)
		c.Set("jwt_is_owner", claims.IsOwner)
		c.Next()
	}
}

// DownloadAuthMiddleware accepts the JWT either via the Authorization header
// or a ?token= query param, so plain browser download links (which can't set
// headers) still authenticate.
func DownloadAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := ""
		if h := c.GetHeader("Authorization"); h != "" {
			parts := strings.Split(h, " ")
			if len(parts) == 2 && parts[0] == "Bearer" {
				token = parts[1]
			}
		}
		if token == "" {
			token = c.Query("token")
		}
		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
			c.Abort()
			return
		}
		claims, err := auth.ValidateToken(token)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			c.Abort()
			return
		}
		c.Set("user_id", claims.UserID.String())
		c.Set("user_email", claims.Email)
		c.Set("user_name", claims.Name)
		c.Set("panel", claims.Panel)
		c.Set("role", claims.Role)
		c.Set("jwt_company_id", claims.CompanyID)
		c.Set("jwt_is_owner", claims.IsOwner)
		c.Next()
	}
}

// RequirePanel restricts a route to the given panels. role == "master_admin"
// always passes regardless of panel. Every other token - including riders,
// drivers, and any token with a blank/unrecognized panel claim - is denied
// by default; the caller must be explicitly listed.
func RequirePanel(panels ...string) gin.HandlerFunc {
	allowed := make(map[string]bool, len(panels))
	for _, p := range panels {
		allowed[p] = true
	}
	return func(c *gin.Context) {
		if c.GetString("role") == "master_admin" {
			c.Next()
			return
		}
		panel := c.GetString("panel")
		if panel == "" || !allowed[panel] {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden: insufficient panel access"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// RequireTrackerCompany restricts a route to tracker-company panel tokens
// and puts the JWT-derived company id into gin context as "company_id", plus
// "is_owner" for the owner-only staff-management routes — handlers must
// scope every query off "company_id", never a client-supplied path/query
// param (same defense-in-depth rule as GetHospitalBookings).
//
// It also re-checks the company's live status on every request (one indexed
// PK lookup) rather than trusting the JWT alone — otherwise suspending a
// company wouldn't cut off an already-issued token until it expires. The
// 403 body matches login's shape ({error, status}) so the frontend can
// route both cases to the same blocked-screen handling.
//
// For a staff-issued token (is_owner=false), it additionally re-verifies the
// specific tracker_staff_users row still exists on every request — so
// removing a staff login takes effect on their very next request instead of
// waiting for the token to expire.
func RequireTrackerCompany() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetString("panel") != "tracker_company" {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden: insufficient panel access"})
			c.Abort()
			return
		}

		companyID := c.GetString("jwt_company_id")
		isOwner := c.GetBool("jwt_is_owner")
		if companyID == "" {
			c.JSON(http.StatusForbidden, gin.H{"error": "account not found"})
			c.Abort()
			return
		}

		ctx := context.Background()
		pool := db.GetDB().GetPool()

		var status string
		if err := pool.QueryRow(ctx,
			`SELECT status FROM tracker_companies WHERE id=$1`, companyID,
		).Scan(&status); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "account not found"})
			c.Abort()
			return
		}
		if status != "active" {
			c.JSON(http.StatusForbidden, gin.H{"error": "account " + status, "status": status})
			c.Abort()
			return
		}

		if !isOwner {
			staffID := c.GetString("user_id")
			var exists bool
			if err := pool.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM tracker_staff_users WHERE id=$1 AND company_id=$2)`,
				staffID, companyID,
			).Scan(&exists); err != nil || !exists {
				c.JSON(http.StatusForbidden, gin.H{"error": "staff access revoked"})
				c.Abort()
				return
			}
		}

		c.Set("company_id", companyID)
		c.Set("is_owner", isOwner)
		c.Next()
	}
}

// RequireTrackerOwner restricts a route to the company's owner token (as
// opposed to a staff login) — chain after RequireTrackerCompany(), which is
// what populates "is_owner". Used for staff-management routes only: staff
// have full owner-equivalent access everywhere else, just not over other
// staff logins.
func RequireTrackerOwner() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !c.GetBool("is_owner") {
			c.JSON(http.StatusForbidden, gin.H{"error": "owner access required"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// OptionalAuthMiddleware validates JWT tokens but doesn't fail if missing
func OptionalAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.Next()
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.Next()
			return
		}

		token := parts[1]
		claims, err := auth.ValidateToken(token)
		if err != nil {
			c.Next()
			return
		}

		c.Set("user_id", claims.UserID.String())
		c.Set("user_email", claims.Email)
		c.Set("user_name", claims.Name)
		c.Set("panel", claims.Panel)
		c.Set("role", claims.Role)
		c.Set("jwt_company_id", claims.CompanyID)
		c.Set("jwt_is_owner", claims.IsOwner)
		c.Next()
	}
}
