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
)

type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=6"`
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

// POST /auth/google — Google OAuth login
