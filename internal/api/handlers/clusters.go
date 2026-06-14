package handlers

import (
	"context"
	"net/http"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type ClusterResponse struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Provider       string `json:"provider"`
	Region         string `json:"region"`
	Status         string `json:"status"`
	AgentConnected bool   `json:"agent_connected"`
	NodeCount      int    `json:"node_count"`
	CreatedAt      string `json:"created_at"`
}

type CreateClusterRequest struct {
	Name      string `json:"name" binding:"required"`
	Provider  string `json:"provider" binding:"required"`
	Region    string `json:"region" binding:"required"`
	NodeCount int    `json:"node_count" binding:"min=1,max=100"`
}

// ListClusters lists all clusters in a project
func ListClusters(c *gin.Context) {
	projectID := c.Param("id")
	userID := c.GetString("user_id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Check project access
	var count int
	pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM projects
		WHERE id = $1 AND (owner_id = $2 OR EXISTS (
			SELECT 1 FROM project_members WHERE project_id = $1 AND user_id = $2
		))
	`, projectID, userID).Scan(&count)

	if count == 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}

	rows, err := pool.Query(ctx, `
		SELECT id, name, provider, region, status, agent_connected, node_count, created_at
		FROM clusters
		WHERE project_id = $1
		ORDER BY created_at DESC
	`, projectID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch clusters"})
		return
	}
	defer rows.Close()

	var clusters []ClusterResponse
	for rows.Next() {
		var c ClusterResponse
		if err := rows.Scan(&c.ID, &c.Name, &c.Provider, &c.Region, &c.Status, &c.AgentConnected, &c.NodeCount, &c.CreatedAt); err != nil {
			continue
		}
		clusters = append(clusters, c)
	}

	c.JSON(http.StatusOK, clusters)
}

// CreateCluster creates a new Kubernetes cluster
func CreateCluster(c *gin.Context) {
	projectID := c.Param("id")
	userID := c.GetString("user_id")

	var req CreateClusterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Check project access
	var count int
	pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM projects
		WHERE id = $1 AND owner_id = $2
	`, projectID, userID).Scan(&count)

	if count == 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "only project owner can create clusters"})
		return
	}

	clusterID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO clusters (id, project_id, name, provider, region, status, node_count, node_instance_type)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, clusterID, projectID, req.Name, req.Provider, req.Region, "provisioning", req.NodeCount, "t3.medium")

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create cluster"})
		return
	}

	c.JSON(http.StatusCreated, ClusterResponse{
		ID:        clusterID.String(),
		Name:      req.Name,
		Provider:  req.Provider,
		Region:    req.Region,
		Status:    "provisioning",
		NodeCount: req.NodeCount,
	})
}

// GetCluster returns a specific cluster
func GetCluster(c *gin.Context) {
	clusterID := c.Param("id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	var cluster ClusterResponse
	err := pool.QueryRow(ctx, `
		SELECT id, name, provider, region, status, agent_connected, node_count, created_at
		FROM clusters WHERE id = $1
	`, clusterID).Scan(&cluster.ID, &cluster.Name, &cluster.Provider, &cluster.Region, &cluster.Status, &cluster.AgentConnected, &cluster.NodeCount, &cluster.CreatedAt)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "cluster not found"})
		return
	}

	c.JSON(http.StatusOK, cluster)
}
