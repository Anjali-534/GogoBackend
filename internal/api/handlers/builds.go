package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type BuildRequest struct {
	AppID     string `json:"app_id" binding:"required"`
	CommitSHA string `json:"commit_sha"`
	CommitMsg string `json:"commit_msg"`
	Branch    string `json:"branch"`
}

type BuildResponse struct {
	ID        string    `json:"id"`
	AppID     string    `json:"app_id"`
	Status    string    `json:"status"`
	CommitSHA *string   `json:"commit_sha,omitempty"`
	Branch    *string   `json:"branch,omitempty"`
	ImageURL  *string   `json:"image_url,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// TriggerBuild creates a new build
func TriggerBuild(c *gin.Context) {
	var req BuildRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Verify app exists
	var appClusterID string
	err := pool.QueryRow(ctx, "SELECT cluster_id FROM apps WHERE id = $1", req.AppID).Scan(&appClusterID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "app not found"})
		return
	}

	// Create build record
	buildID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO builds (id, app_id, status, commit_sha, commit_msg, branch)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, buildID, req.AppID, "queued", req.CommitSHA, req.CommitMsg, req.Branch)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create build"})
		return
	}

	// Queue build job (would be sent to Redis queue)
	// TODO: Send to build worker queue

	c.JSON(http.StatusCreated, BuildResponse{
		ID:        buildID.String(),
		AppID:     req.AppID,
		Status:    "queued",
		CommitSHA: &req.CommitSHA,
		Branch:    &req.Branch,
		CreatedAt: time.Now(),
	})
}

// GetBuildLogs returns build logs
func GetBuildLogs(c *gin.Context) {
	buildID := c.Param("id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var logs *string
	err := pool.QueryRow(ctx, "SELECT logs FROM builds WHERE id = $1", buildID).Scan(&logs)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "build not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"logs": logs})
}

// ListBuilds lists all builds for an app
func ListBuilds(c *gin.Context) {
	appID := c.Param("id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT id, app_id, status, commit_sha, branch, image_url, created_at
		FROM builds
		WHERE app_id = $1
		ORDER BY created_at DESC
		LIMIT 50
	`, appID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch builds"})
		return
	}
	defer rows.Close()

	var builds []BuildResponse
	for rows.Next() {
		var b BuildResponse
		if err := rows.Scan(&b.ID, &b.AppID, &b.Status, &b.CommitSHA, &b.Branch, &b.ImageURL, &b.CreatedAt); err != nil {
			continue
		}
		builds = append(builds, b)
	}

	c.JSON(http.StatusOK, builds)
}

// BuildWorker would run in a separate service
// This is a conceptual function showing how builds would be processed
func BuildApp(appID, commitSHA string) error {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Get app details
	var repoURL, buildMethod string
	var clusterID string
	err := pool.QueryRow(ctx, `
		SELECT repo_url, build_method, cluster_id
		FROM apps WHERE id = $1
	`, appID).Scan(&repoURL, &buildMethod, &clusterID)
	if err != nil {
		return err
	}

	// Get cluster's registry
	var registryURL string
	err = pool.QueryRow(ctx, `
		SELECT CASE 
			WHEN provider = 'aws' THEN concat(aws_account_id, '.dkr.ecr.', aws_region, '.amazonaws.com')
			ELSE 'registry.example.com'
		END
		FROM clusters WHERE id = $1
	`, clusterID).Scan(&registryURL)
	if err != nil {
		return err
	}

	// Build steps:
	// 1. Clone repo
	// 2. Run buildpack or build Dockerfile
	// 3. Tag image: {registry}/{appID}:{commitSHA}
	// 4. Push to registry
	// 5. Update build record with image URL
	// 6. Trigger deployment

	imageURL := fmt.Sprintf("%s/%s:%s", registryURL, appID, commitSHA[:7])

	_, err = pool.Exec(ctx, `
		UPDATE builds SET status = $1, image_url = $2, finished_at = NOW()
		WHERE app_id = $3 AND commit_sha = $4
	`, "success", imageURL, appID, commitSHA)

	return err
}
