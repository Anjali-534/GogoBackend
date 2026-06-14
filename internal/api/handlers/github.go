package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/deploykit/backend/internal/auth"
	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
)

// GitHubAuthURL returns GitHub OAuth URL
func GitHubAuthURL(c *gin.Context) {
	// Generate random state
	b := make([]byte, 32)
	rand.Read(b)
	state := base64.StdEncoding.EncodeToString(b)

	// Store state in session (in production, use Redis or encrypted cookie)
	c.SetCookie("oauth_state", state, 600, "/", "", false, true)

	authURL := auth.GetGitHubAuthURL(state)
	c.JSON(http.StatusOK, gin.H{
		"auth_url": authURL,
	})
}

// GitHubCallback handles GitHub OAuth callback
func GitHubCallback(c *gin.Context) {
	code := c.Query("code")
	state := c.Query("state")

	if code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing code"})
		return
	}

	// Verify state
	storedState, err := c.Cookie("oauth_state")
	if err != nil || storedState != state {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid state"})
		return
	}

	ctx := context.Background()
	cfg := c.MustGet("config").(*config.Config)

	// Handle callback and create/update user
	userID, token, err := auth.HandleGitHubCallback(ctx, code, cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "authentication failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user_id":      userID,
		"access_token": token,
		"expires_in":   int(cfg.JWTExpiration.Seconds()),
	})
}

// ListGitHubRepos lists user's GitHub repositories
func ListGitHubRepos(c *gin.Context) {
	userID := c.GetString("user_id")

	ctx := context.Background()
	repos, err := auth.ListGitHubRepos(ctx, uuid.MustParse(userID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, repos)
}

// ConnectGitHubRepo connects a GitHub repo to an app
func ConnectGitHubRepo(c *gin.Context) {
	userID := c.GetString("user_id")

	var req struct {
		AppID      string `json:"app_id" binding:"required"`
		Owner      string `json:"owner" binding:"required"`
		Repository string `json:"repository" binding:"required"`
		Branch     string `json:"branch"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Branch == "" {
		req.Branch = "main"
	}

	ctx := context.Background()
	cfg := c.MustGet("config").(*config.Config)
	pool := db.GetDB().GetPool()

	// Update app with repo URL
	repoURL := "https://github.com/" + req.Owner + "/" + req.Repository
	_, err := pool.Exec(ctx, `
		UPDATE apps SET repo_url = $1, repo_provider = $2, repo_branch = $3
		WHERE id = $4
	`, repoURL, "github", req.Branch, req.AppID)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to connect repository"})
		return
	}

	// Create webhook (would include your API's webhook endpoint)
	webhookURL := cfg.APIBaseURL + "/webhooks/github"
	err = auth.CreateGitHubWebhook(ctx, uuid.MustParse(userID), req.Owner, req.Repository, webhookURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create webhook"})
		return
	}

	// Write GitHub Actions YAML
	err = auth.WriteGitHubActionsYAML(ctx, uuid.MustParse(userID), req.Owner, req.Repository, req.AppID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to write actions file"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  "Repository connected",
		"repo_url": repoURL,
		"webhook":  "created",
		"ci_file":  "created",
	})
}
