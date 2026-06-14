package handlers

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/deploykit/backend/internal/db"
)

type CreateProjectRequest struct {
	Name string `json:"name" binding:"required"`
}

type ProjectDetail struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Plan  string `json:"plan"`
	Apps  int    `json:"apps_count"`
	Clusters int `json:"clusters_count"`
}

// ListProjects lists all projects for the authenticated user
func ListProjects(c *gin.Context) {
	userID := c.GetString("user_id")
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	rows, err := pool.Query(ctx, `
		SELECT id, name, slug, plan
		FROM projects
		WHERE owner_id = $1
		OR EXISTS (
			SELECT 1 FROM project_members pm
			WHERE pm.project_id = projects.id AND pm.user_id = $1
		)
		ORDER BY created_at DESC
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

	c.JSON(http.StatusOK, projects)
}

// CreateProject creates a new project
func CreateProject(c *gin.Context) {
	userID := c.GetString("user_id")

	var req CreateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	projectID := uuid.New()
	slug := req.Name // simplified - should be slugified

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	_, err := pool.Exec(ctx,
		"INSERT INTO projects (id, name, slug, owner_id, plan) VALUES ($1, $2, $3, $4, $5)",
		projectID, req.Name, slug, userID, "starter",
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create project"})
		return
	}

	c.JSON(http.StatusCreated, ProjectResponse{
		ID:   projectID.String(),
		Name: req.Name,
		Slug: slug,
		Plan: "starter",
	})
}

// GetProject returns a specific project
func GetProject(c *gin.Context) {
	projectID := c.Param("id")
	userID := c.GetString("user_id")

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Check access
	var count int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM projects
		WHERE id = $1
		AND (owner_id = $2 OR EXISTS (
			SELECT 1 FROM project_members pm
			WHERE pm.project_id = projects.id AND pm.user_id = $2
		))
	`, projectID, userID).Scan(&count)

	if err != nil || count == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}

	// Get project details
	var id, name, slug, plan string
	err = pool.QueryRow(ctx,
		"SELECT id, name, slug, plan FROM projects WHERE id = $1",
		projectID,
	).Scan(&id, &name, &slug, &plan)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch project"})
		return
	}

	// Count apps and clusters
	var appsCount, clustersCount int
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM apps WHERE project_id = $1", projectID).Scan(&appsCount)
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM clusters WHERE project_id = $1", projectID).Scan(&clustersCount)

	c.JSON(http.StatusOK, ProjectDetail{
		ID:       id,
		Name:     name,
		Slug:     slug,
		Plan:     plan,
		Apps:     appsCount,
		Clusters: clustersCount,
	})
}
