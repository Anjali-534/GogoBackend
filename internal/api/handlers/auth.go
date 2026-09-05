package handlers

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/deploykit/backend/internal/auth"
	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/api/idtoken"
)

type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=6"`
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required,min=8"`
}

type SignupRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Name     string `json:"name" binding:"required"`
	Password string `json:"password" binding:"required,min=8"`
}

type AuthResponse struct {
	User         UserResponse `json:"user"`
	AccessToken  string       `json:"access_token"`
	RefreshToken string       `json:"refresh_token,omitempty"`
	ExpiresIn    int          `json:"expires_in"`
}

type UserResponse struct {
	ID        string  `json:"id"`
	Email     string  `json:"email"`
	Name      string  `json:"name"`
	AvatarURL *string `json:"avatar_url,omitempty"`
}

type MeResponse struct {
	User     UserResponse      `json:"user"`
	Projects []ProjectResponse `json:"projects"`
}

type ProjectResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	Plan string `json:"plan"`
}

// Signup creates a new user account
func Signup(c *gin.Context) {
	var req SignupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Check if user exists
	var count int
	err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM users WHERE email = $1", req.Email).Scan(&count)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "email already registered"})
		return
	}

	// Hash password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}

	// Create user
	userID := uuid.New()
	_, err = pool.Exec(ctx,
		"INSERT INTO users (id, email, name, password_hash, is_verified) VALUES ($1, $2, $3, $4, $5)",
		userID, req.Email, req.Name, string(hashedPassword), false,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create user"})
		return
	}

	// Generate token
	cfg := c.MustGet("config").(*config.Config)
	token, err := auth.GenerateToken(userID, req.Email, req.Name, "", cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	c.JSON(http.StatusCreated, AuthResponse{
		User: UserResponse{
			ID:    userID.String(),
			Email: req.Email,
			Name:  req.Name,
		},
		AccessToken: token,
		ExpiresIn:   int(cfg.JWTExpiration.Seconds()),
	})
}

// Login authenticates a user
func Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Get user
	var userID uuid.UUID
	var name string
	var passwordHash string
	err := pool.QueryRow(ctx,
		"SELECT id, name, password_hash FROM users WHERE email = $1",
		req.Email,
	).Scan(&userID, &name, &passwordHash)

	if err == pgx.ErrNoRows {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	// Verify password
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	// Generate token — the platform admin account gets role=master_admin so
	// deny-by-default panel middleware recognizes it; everyone else (riders,
	// drivers) gets a blank role and is denied on gated admin endpoints.
	cfg := c.MustGet("config").(*config.Config)
	role := ""
	adminEmail := os.Getenv("ADMIN_EMAIL")
	if adminEmail != "" && strings.EqualFold(req.Email, adminEmail) {
		role = "master_admin"
	}
	token, err := auth.GenerateToken(userID, req.Email, name, role, cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	c.JSON(http.StatusOK, AuthResponse{
		User: UserResponse{
			ID:    userID.String(),
			Email: req.Email,
			Name:  name,
		},
		AccessToken: token,
		ExpiresIn:   int(cfg.JWTExpiration.Seconds()),
	})
}

// ChangePassword updates the calling user's own password — user_id comes
// from the JWT (set by AuthMiddleware), never a request param, so a caller
// can only ever change their own password. Shared by riders and drivers,
// since both authenticate against the same users table via /auth/login.
func ChangePassword(c *gin.Context) {
	var req ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	userID := c.GetString("user_id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var passwordHash string
	if err := pool.QueryRow(ctx,
		"SELECT password_hash FROM users WHERE id = $1", userID,
	).Scan(&passwordHash); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.CurrentPassword)); err != nil {
		// 403, not 401: the driver-app's shared axios interceptor treats any
		// 401 as an expired session and force-logs-out to the login screen
		// (see services/api.ts) — a wrong current-password guess must not
		// trigger that, it needs to surface inline on this form instead.
		c.JSON(http.StatusForbidden, gin.H{"error": "current password is incorrect"})
		return
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}

	if _, err := pool.Exec(ctx,
		"UPDATE users SET password_hash = $2, updated_at = NOW() WHERE id = $1",
		userID, string(newHash),
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update password"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "password changed"})
}

// Me returns the current authenticated user
func Me(c *gin.Context) {
	userID := c.GetString("user_id")
	userEmail := c.GetString("user_email")
	userName := c.GetString("user_name")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Get projects for this user
	rows, err := pool.Query(ctx, `
		SELECT p.id, p.name, p.slug, p.plan
		FROM projects p
		WHERE p.owner_id = $1
		OR EXISTS (
			SELECT 1 FROM project_members pm
			WHERE pm.project_id = p.id AND pm.user_id = $1
		)
		ORDER BY p.created_at DESC
	`, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch projects"})
		return
	}
	defer rows.Close()

	var projects []ProjectResponse
	for rows.Next() {
		var id, name, slug, plan string
		if err := rows.Scan(&id, &name, &slug, &plan); err != nil {
			continue
		}
		projects = append(projects, ProjectResponse{
			ID:   id,
			Name: name,
			Slug: slug,
			Plan: plan,
		})
	}

	c.JSON(http.StatusOK, MeResponse{
		User: UserResponse{
			ID:    userID,
			Email: userEmail,
			Name:  userName,
		},
		Projects: projects,
	})
}

// Refresh generates a new token
func Refresh(c *gin.Context) {
	token := c.GetHeader("Authorization")
	if token == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
		return
	}

	// Remove "Bearer " prefix
	if len(token) > 7 {
		token = token[7:]
	}

	cfg := c.MustGet("config").(*config.Config)
	newToken, err := auth.RefreshToken(token, cfg)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"access_token": newToken,
		"expires_in":   int(cfg.JWTExpiration.Seconds()),
	})
}

// Logout is a placeholder (JWT doesn't require server-side logout)
func Logout(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "logged out"})
}

// GoogleLoginRequest carries the ID token Google Identity Services hands
// the frontend after a successful "Sign in with Google" — the frontend
// never sees or handles credentials itself, just forwards this token here
// for signature/audience verification.
type GoogleLoginRequest struct {
	IDToken string `json:"id_token" binding:"required"`
}

// GoogleLogin verifies a Google ID token and logs the rider in, creating a
// new account on first sign-in. Runs alongside plain email+password login
// (POST /auth/login) — this doesn't replace it.
//
// Matching/linking is by verified email, same as GitHub's github_id lookup
// but keyed on email instead: users.email is UNIQUE, so an email can only
// ever belong to one row, and linking here (rather than creating a second
// account) is the only option that doesn't collide with that constraint.
// A first-time Google sign-in for an email that already has a
// password_hash account links google_id onto that existing row instead of
// creating a duplicate; either sign-in method works for it from then on.
func GoogleLogin(c *gin.Context) {
	var req GoogleLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	cfg := c.MustGet("config").(*config.Config)
	if cfg.GoogleClientID == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "google sign-in not configured"})
		return
	}

	ctx := context.Background()

	// Validate checks the token's signature against Google's published keys
	// and that its audience matches our client ID — never trust an ID token
	// without both checks.
	payload, err := idtoken.Validate(ctx, req.IDToken, cfg.GoogleClientID)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid google token"})
		return
	}

	email, _ := payload.Claims["email"].(string)
	if email == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "google account has no email"})
		return
	}
	if verified, _ := payload.Claims["email_verified"].(bool); !verified {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "google email not verified"})
		return
	}
	name, _ := payload.Claims["name"].(string)
	if name == "" {
		name = strings.SplitN(email, "@", 2)[0]
	}
	avatarURL, _ := payload.Claims["picture"].(string)
	googleID := payload.Subject

	pool := db.GetDB().GetPool()

	var userID uuid.UUID
	var existingName string
	var existingAvatar *string
	var existingGoogleID *string
	err = pool.QueryRow(ctx,
		"SELECT id, name, avatar_url, google_id FROM users WHERE email = $1",
		email,
	).Scan(&userID, &existingName, &existingAvatar, &existingGoogleID)

	if err == pgx.ErrNoRows {
		// New account: no password, flagged as Google-authenticated via
		// google_id. Also create the riders row (same dual-insert as
		// RiderSignup) so GetRiderProfile doesn't 404 on this user's next
		// page load — phone is left NULL (nullable since migration 059) and
		// can be added later; there's no phone on a Google identity to seed it with.
		userID = uuid.New()
		riderID := uuid.New()
		tx, txErr := pool.Begin(ctx)
		if txErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}
		defer tx.Rollback(ctx)

		if _, err := tx.Exec(ctx,
			"INSERT INTO users (id, email, name, google_id, avatar_url, is_verified) VALUES ($1, $2, $3, $4, $5, true)",
			userID, email, name, googleID, nullIfEmpty(avatarURL),
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create user"})
			return
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO riders (id, user_id, phone) VALUES ($1, $2, NULL)",
			riderID, userID,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create rider profile"})
			return
		}
		if err := tx.Commit(ctx); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}
		existingName = name
		if avatarURL != "" {
			existingAvatar = &avatarURL
		}
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	} else if existingGoogleID == nil {
		// Existing password-account signing in with Google for the first
		// time — link, don't duplicate.
		if _, err := pool.Exec(ctx,
			"UPDATE users SET google_id = $2, updated_at = NOW() WHERE id = $1",
			userID, googleID,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to link google account"})
			return
		}
	}

	// Same admin-role rule as password login (see Login above).
	cfgRole := ""
	adminEmail := os.Getenv("ADMIN_EMAIL")
	if adminEmail != "" && strings.EqualFold(email, adminEmail) {
		cfgRole = "master_admin"
	}

	token, err := auth.GenerateToken(userID, email, existingName, cfgRole, cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	c.JSON(http.StatusOK, AuthResponse{
		User: UserResponse{
			ID:        userID.String(),
			Email:     email,
			Name:      existingName,
			AvatarURL: existingAvatar,
		},
		AccessToken: token,
		ExpiresIn:   int(cfg.JWTExpiration.Seconds()),
	})
}
