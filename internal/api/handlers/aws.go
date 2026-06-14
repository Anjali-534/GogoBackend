package handlers

import (
	"context"
	"fmt"
	"net/http"

	"github.com/deploykit/backend/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type AWSProvisionRequest struct {
	ClusterName string `json:"cluster_name" binding:"required"`
	Region      string `json:"region" binding:"required"`
	NodeCount   int    `json:"node_count"`
	NodeType    string `json:"node_type"`
	VPCName     string `json:"vpc_name"`
}

type ProvisionResponse struct {
	ClusterID string `json:"cluster_id"`
	Status    string `json:"status"`
	Message   string `json:"message"`
}

// ProvisionAWSCluster starts provisioning an EKS cluster
func ProvisionAWSCluster(c *gin.Context) {
	projectID := c.Param("id")
	userID := c.GetString("user_id")

	var req AWSProvisionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.NodeCount == 0 {
		req.NodeCount = 2
	}
	if req.NodeType == "" {
		req.NodeType = "t3.medium"
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Verify project ownership
	var count int
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM projects WHERE id = $1 AND owner_id = $2", projectID, userID).Scan(&count)
	if count == 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}

	clusterID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO clusters (id, project_id, name, provider, region, status, node_count, node_instance_type)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, clusterID, projectID, req.ClusterName, "aws", req.Region, "provisioning", req.NodeCount, req.NodeType)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create cluster record"})
		return
	}

	// Queue provisioning job (would be sent to provisioning worker)
	// provisioning.QueueAWSCluster(clusterID, req)

	c.JSON(http.StatusAccepted, ProvisionResponse{
		ClusterID: clusterID.String(),
		Status:    "provisioning",
		Message:   fmt.Sprintf("Starting EKS cluster provisioning in %s", req.Region),
	})
}

// AWSOAuth links AWS account credentials
func LinkAWSAccount(c *gin.Context) {
	projectID := c.Param("id")
	userID := c.GetString("user_id")

	var req struct {
		AccountID string `json:"account_id" binding:"required"`
		RoleARN   string `json:"role_arn" binding:"required"`
		Region    string `json:"region" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := context.Background()
	pool := db.GetDB().GetPool()

	// Verify project ownership
	var count int
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM projects WHERE id = $1 AND owner_id = $2", projectID, userID).Scan(&count)
	if count == 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}

	credID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO cloud_credentials (id, project_id, provider, name, aws_account_id, aws_role_arn, aws_region)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, credID, projectID, "aws", fmt.Sprintf("AWS-%s", req.AccountID), req.AccountID, req.RoleARN, req.Region)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to link account"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"credential_id": credID.String(),
		"provider":      "aws",
		"account_id":    req.AccountID,
	})
}

// ConceptualAWSProvisioning shows the flow
// This would run in a separate provisioning worker
func ProvisionAWSEKS(clusterID, clusterName, region string, nodeCount int, nodeType string) error {
	// 1. Create VPC
	// vpcID, err := createVPC(region)

	// 2. Create subnets
	// publicSubnets, privateSubnets := createSubnets(vpcID, region)

	// 3. Create EKS cluster
	// eksCluster, err := createEKSCluster(clusterName, region, vpcID, publicSubnets)

	// 4. Create node group
	// nodeGroupID, err := createNodeGroup(eksCluster, nodeCount, nodeType)

	// 5. Install Porter agent
	// installPorterAgent(eksCluster)

	// 6. Create load balancer
	// lbIP, err := setupLoadBalancer(eksCluster, region)

	// 7. Setup DNS
	// setupDNS(clusterName, lbIP)

	// 8. Update cluster record
	ctx := context.Background()
	pool := db.GetDB().GetPool()

	_, err := pool.Exec(ctx, `
		UPDATE clusters SET status = $1, ingress_ip = $2, k8s_version = $3
		WHERE id = $4
	`, "active", "0.0.0.0", "1.29", clusterID)

	return err
}
