package handlers

import (
	"context"
	"net/http"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type AppResponse struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Type          string  `json:"type"`
	Status        string  `json:"status"`
	RepoURL       *string `json:"repo_url,omitempty"`
	DockerImage   *string `json:"docker_image,omitempty"`
	Port          int     `json:"port"`
	Replicas      int     `json:"replicas"`
	CPUMillicores int     `json:"cpu_millicores"`
	RamMB         int     `json:"ram_mb"`
	IsPublic      bool    `json:"is_public"`
	CreatedAt     string  `json:"created_at"`
}

type CreateAppRequest struct {
	Name          string  `json:"name" binding:"required"`
	Type          string  `json:"type" binding:"required"`
	RepoURL       *string `json:"repo_url"`
	DockerImage   *string `json:"docker_image"`
	BuildMethod   string  `json:"build_method" binding:"required"`
	Port          int     `json:"port"`
	CPUMillicores int     `json:"cpu_millicores"`
	RamMB         int     `json:"ram_mb"`
	Replicas      int     `json:"replicas"`
}

// ListApps lists all apps in a cluster
func ListApps(c *gin.Context) {
	clusterID := c.Param("id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT id, name, type, status, repo_url, docker_image, port, replicas, 
		       cpu_millicores, ram_mb, is_public, created_at
		FROM apps
		WHERE cluster_id = $1
		ORDER BY created_at DESC
	`, clusterID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch apps"})
		return
	}
	defer rows.Close()

	var apps []AppResponse
	for rows.Next() {
		var app AppResponse
		if err := rows.Scan(&app.ID, &app.Name, &app.Type, &app.Status, &app.RepoURL, &app.DockerImage,
			&app.Port, &app.Replicas, &app.CPUMillicores, &app.RamMB, &app.IsPublic, &app.CreatedAt); err != nil {
			continue
		}
		apps = append(apps, app)
	}

	c.JSON(http.StatusOK, apps)
}

// CreateApp creates a new application
func CreateApp(c *gin.Context) {
	clusterID := c.Param("id")

	var req CreateAppRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Port == 0 {
		req.Port = 3000
	}
	if req.CPUMillicores == 0 {
		req.CPUMillicores = 100
	}
	if req.RamMB == 0 {
		req.RamMB = 256
	}
	if req.Replicas == 0 {
		req.Replicas = 1
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Get project_id from cluster
	var projectID string
	err := pool.QueryRow(ctx, "SELECT project_id FROM clusters WHERE id = $1", clusterID).Scan(&projectID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "cluster not found"})
		return
	}

	appID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO apps 
		(id, project_id, cluster_id, name, type, status, repo_url, docker_image, 
		 build_method, port, cpu_millicores, ram_mb, replicas, is_public)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`, appID, projectID, clusterID, req.Name, req.Type, "deploying",
		req.RepoURL, req.DockerImage, req.BuildMethod, req.Port,
		req.CPUMillicores, req.RamMB, req.Replicas, true)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create app"})
		return
	}

	c.JSON(http.StatusCreated, AppResponse{
		ID:            appID.String(),
		Name:          req.Name,
		Type:          req.Type,
		Status:        "deploying",
		RepoURL:       req.RepoURL,
		DockerImage:   req.DockerImage,
		Port:          req.Port,
		CPUMillicores: req.CPUMillicores,
		RamMB:         req.RamMB,
		Replicas:      req.Replicas,
		IsPublic:      true,
	})
}

// GetApp returns a specific app
func GetApp(c *gin.Context) {
	appID := c.Param("id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var app AppResponse
	err := pool.QueryRow(ctx, `
		SELECT id, name, type, status, repo_url, docker_image, port, replicas, 
		       cpu_millicores, ram_mb, is_public, created_at
		FROM apps WHERE id = $1
	`, appID).Scan(&app.ID, &app.Name, &app.Type, &app.Status, &app.RepoURL, &app.DockerImage,
		&app.Port, &app.Replicas, &app.CPUMillicores, &app.RamMB, &app.IsPublic, &app.CreatedAt)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "app not found"})
		return
	}

	c.JSON(http.StatusOK, app)
}
