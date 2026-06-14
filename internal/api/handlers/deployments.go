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

type DeployRequest struct {
	AppID   string `json:"app_id" binding:"required"`
	BuildID string `json:"build_id" binding:"required"`
}

type DeploymentResp struct {
	ID        string    `json:"id"`
	AppID     string    `json:"app_id"`
	Status    string    `json:"status"`
	Revision  int       `json:"revision"`
	ImageURL  *string   `json:"image_url,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// TriggerDeployment creates a new deployment
func TriggerDeployment(c *gin.Context) {
	var req DeployRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Get build image
	var imageURL string
	err := pool.QueryRow(ctx, "SELECT image_url FROM builds WHERE id = $1", req.BuildID).Scan(&imageURL)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "build not found"})
		return
	}

	// Get next revision number
	var lastRevision int
	pool.QueryRow(ctx, "SELECT COALESCE(MAX(revision), 0) FROM deployments WHERE app_id = $1", req.AppID).Scan(&lastRevision)
	newRevision := lastRevision + 1

	// Create deployment
	deployID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO deployments (id, app_id, build_id, revision, status, image_url)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, deployID, req.AppID, req.BuildID, newRevision, "deploying", imageURL)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create deployment"})
		return
	}

	// Queue deployment job (would send to deployment worker)
	// TODO: Send to k8s deployment queue

	c.JSON(http.StatusCreated, DeploymentResp{
		ID:        deployID.String(),
		AppID:     req.AppID,
		Status:    "deploying",
		Revision:  newRevision,
		ImageURL:  &imageURL,
		CreatedAt: time.Now(),
	})
}

// GetDeploymentStatus returns deployment status
func GetDeploymentStatus(c *gin.Context) {
	deployID := c.Param("id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var d DeploymentResp
	err := pool.QueryRow(ctx, `
		SELECT id, app_id, status, revision, image_url, created_at
		FROM deployments WHERE id = $1
	`, deployID).Scan(&d.ID, &d.AppID, &d.Status, &d.Revision, &d.ImageURL, &d.CreatedAt)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "deployment not found"})
		return
	}

	c.JSON(http.StatusOK, d)
}

// ListDeployments lists deployment history
func ListDeployments(c *gin.Context) {
	appID := c.Param("id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT id, app_id, status, revision, image_url, created_at
		FROM deployments
		WHERE app_id = $1
		ORDER BY created_at DESC
		LIMIT 50
	`, appID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch deployments"})
		return
	}
	defer rows.Close()

	var deployments []DeploymentResp
	for rows.Next() {
		var d DeploymentResp
		if err := rows.Scan(&d.ID, &d.AppID, &d.Status, &d.Revision, &d.ImageURL, &d.CreatedAt); err != nil {
			continue
		}
		deployments = append(deployments, d)
	}

	c.JSON(http.StatusOK, deployments)
}

// RollbackDeployment rolls back to a previous deployment
func RollbackDeployment(c *gin.Context) {
	appID := c.Param("id")
	previousRevision := c.Param("revision")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Get previous deployment
	var previousID, imageURL string
	err := pool.QueryRow(ctx, `
		SELECT id, image_url FROM deployments
		WHERE app_id = $1 AND revision = $2
	`, appID, previousRevision).Scan(&previousID, &imageURL)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "deployment not found"})
		return
	}

	// Get next revision
	var lastRevision int
	pool.QueryRow(ctx, "SELECT COALESCE(MAX(revision), 0) FROM deployments WHERE app_id = $1", appID).Scan(&lastRevision)
	newRevision := lastRevision + 1

	// Create rollback deployment
	deployID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO deployments (id, app_id, revision, status, image_url, rollback_of)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, deployID, appID, newRevision, "deploying", imageURL, previousID)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to rollback"})
		return
	}

	c.JSON(http.StatusCreated, DeploymentResp{
		ID:        deployID.String(),
		AppID:     appID,
		Status:    "deploying",
		Revision:  newRevision,
		ImageURL:  &imageURL,
		CreatedAt: time.Now(),
	})
}

// DeployToK8s would be called by a deployment worker
// This shows the conceptual flow
func DeployToK8s(deployID, appID, imageURL, clusterID string) error {
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Get app config
	var appName, port string
	var replicas int
	var cpuMillicores, ramMB int
	err := pool.QueryRow(ctx, `
		SELECT name, port, replicas, cpu_millicores, ram_mb
		FROM apps WHERE id = $1
	`, appID).Scan(&appName, &port, &replicas, &cpuMillicores, &ramMB)
	if err != nil {
		return err
	}

	// Create K8s manifest
	_ = fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: default
spec:
  replicas: %d
  selector:
    matchLabels:
      app: %s
  strategy:
    type: RollingUpdate
  template:
    metadata:
      labels:
        app: %s
    spec:
      containers:
      - name: %s
        image: %s
        ports:
        - containerPort: %s
        resources:
          requests:
            cpu: %dm
            memory: %dMi
          limits:
            cpu: %dm
            memory: %dMi
        livenessProbe:
          httpGet:
            path: /health
            port: %s
          initialDelaySeconds: 30
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /health
            port: %s
          initialDelaySeconds: 10
          periodSeconds: 5
---
apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: default
spec:
  type: ClusterIP
  selector:
    app: %s
  ports:
  - port: 80
    targetPort: %s
    protocol: TCP
`, appName, replicas, appName, appName, appName, imageURL, port,
		cpuMillicores, ramMB, cpuMillicores*2, ramMB*2, port, port, appName, appName, port)

	// Apply to cluster (would use k8s client-go)
	// For now, just update deployment status
	_, err = pool.Exec(ctx, `
		UPDATE deployments SET status = $1, updated_at = NOW()
		WHERE id = $2
	`, "successful", deployID)

	// Log event
	_, _ = pool.Exec(ctx, `
		INSERT INTO deployment_events (id, app_id, deployment_id, type, message)
		VALUES ($1, $2, $3, $4, $5)
	`, uuid.New(), appID, deployID, "deploy.success", "Deployment successful")

	return err
}
