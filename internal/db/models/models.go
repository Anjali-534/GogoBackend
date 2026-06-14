package models

import (
	"database/sql/driver"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ============================================================
// USER MODELS
// ============================================================

type User struct {
	ID          uuid.UUID `db:"id" json:"id"`
	Email       string    `db:"email" json:"email"`
	Name        string    `db:"name" json:"name"`
	AvatarURL   *string   `db:"avatar_url" json:"avatar_url,omitempty"`
	GitHubID    *int64    `db:"github_id" json:"github_id,omitempty"`
	GitHubLogin *string   `db:"github_login" json:"github_login,omitempty"`
	GitLabID    *int64    `db:"gitlab_id" json:"gitlab_id,omitempty"`
	GitLabLogin *string   `db:"gitlab_login" json:"gitlab_login,omitempty"`
	PasswordHash *string  `db:"password_hash" json:"-"`
	IsVerified  bool      `db:"is_verified" json:"is_verified"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time `db:"updated_at" json:"updated_at"`
}

type OAuthToken struct {
	ID           uuid.UUID `db:"id" json:"id"`
	UserID       uuid.UUID `db:"user_id" json:"user_id"`
	Provider     string    `db:"provider" json:"provider"` // github, gitlab
	AccessToken  string    `db:"access_token" json:"access_token"`
	RefreshToken *string   `db:"refresh_token" json:"refresh_token,omitempty"`
	ExpiresAt    *time.Time `db:"expires_at" json:"expires_at,omitempty"`
	Scopes       []string  `db:"scopes" json:"scopes"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time `db:"updated_at" json:"updated_at"`
}

// ============================================================
// PROJECT MODELS
// ============================================================

type Project struct {
	ID        uuid.UUID `db:"id" json:"id"`
	Name      string    `db:"name" json:"name"`
	Slug      string    `db:"slug" json:"slug"`
	OwnerID   uuid.UUID `db:"owner_id" json:"owner_id"`
	Plan      string    `db:"plan" json:"plan"` // starter, standard, enterprise
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

type ProjectMember struct {
	ID        uuid.UUID `db:"id" json:"id"`
	ProjectID uuid.UUID `db:"project_id" json:"project_id"`
	UserID    uuid.UUID `db:"user_id" json:"user_id"`
	Role      string    `db:"role" json:"role"` // owner, admin, developer, viewer
	InvitedBy *uuid.UUID `db:"invited_by" json:"invited_by,omitempty"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// ============================================================
// CLOUD MODELS
// ============================================================

type CloudCredential struct {
	ID                  uuid.UUID `db:"id" json:"id"`
	ProjectID           uuid.UUID `db:"project_id" json:"project_id"`
	Provider            string    `db:"provider" json:"provider"` // aws, gcp, azure
	Name                string    `db:"name" json:"name"`
	AWSAccountID        *string   `db:"aws_account_id" json:"aws_account_id,omitempty"`
	AWSRoleARN          *string   `db:"aws_role_arn" json:"aws_role_arn,omitempty"`
	AWSRegion           *string   `db:"aws_region" json:"aws_region,omitempty"`
	GCPProjectID        *string   `db:"gcp_project_id" json:"gcp_project_id,omitempty"`
	AzureSubscriptionID *string   `db:"azure_subscription_id" json:"azure_subscription_id,omitempty"`
	CreatedAt           time.Time `db:"created_at" json:"created_at"`
	UpdatedAt           time.Time `db:"updated_at" json:"updated_at"`
}

type Cluster struct {
	ID                uuid.UUID          `db:"id" json:"id"`
	ProjectID         uuid.UUID          `db:"project_id" json:"project_id"`
	CloudCredentialID *uuid.UUID         `db:"cloud_credential_id" json:"cloud_credential_id,omitempty"`
	Name              string             `db:"name" json:"name"`
	Provider          string             `db:"provider" json:"provider"` // aws, gcp, azure, local
	Region            string             `db:"region" json:"region"`
	K8sVersion        *string            `db:"k8s_version" json:"k8s_version,omitempty"`
	Status            string             `db:"status" json:"status"` // provisioning, active, error, deleting
	VanityURL         *string            `db:"vanity_url" json:"vanity_url,omitempty"`
	IngressIP         *string            `db:"ingress_ip" json:"ingress_ip,omitempty"`
	AgentConnected    bool               `db:"agent_connected" json:"agent_connected"`
	AgentVersion      *string            `db:"agent_version" json:"agent_version,omitempty"`
	LastHeartbeat     *time.Time         `db:"last_heartbeat" json:"last_heartbeat,omitempty"`
	NodeCount         int                `db:"node_count" json:"node_count"`
	NodeInstanceType  string             `db:"node_instance_type" json:"node_instance_type"`
	CreatedAt         time.Time          `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time          `db:"updated_at" json:"updated_at"`
}

// ============================================================
// APP MODELS
// ============================================================

type App struct {
	ID                    uuid.UUID `db:"id" json:"id"`
	ProjectID             uuid.UUID `db:"project_id" json:"project_id"`
	ClusterID             uuid.UUID `db:"cluster_id" json:"cluster_id"`
	Name                  string    `db:"name" json:"name"`
	Type                  string    `db:"type" json:"type"` // web, worker, cron, job
	Status                string    `db:"status" json:"status"` // deploying, running, errored, sleeping
	RepoURL               *string   `db:"repo_url" json:"repo_url,omitempty"`
	RepoBranch            string    `db:"repo_branch" json:"repo_branch"`
	RepoProvider          *string   `db:"repo_provider" json:"repo_provider,omitempty"` // github, gitlab
	DockerImage           *string   `db:"docker_image" json:"docker_image,omitempty"`
	BuildMethod           string    `db:"build_method" json:"build_method"` // buildpack, dockerfile, image
	DockerfilePath        string    `db:"dockerfile_path" json:"dockerfile_path"`
	BuildContext          string    `db:"build_context" json:"build_context"`
	StartCommand          *string   `db:"start_command" json:"start_command,omitempty"`
	Port                  int       `db:"port" json:"port"`
	CPUMillicores         int       `db:"cpu_millicores" json:"cpu_millicores"`
	RamMB                 int       `db:"ram_mb" json:"ram_mb"`
	Replicas              int       `db:"replicas" json:"replicas"`
	AutoscalingEnabled    bool      `db:"autoscaling_enabled" json:"autoscaling_enabled"`
	MinReplicas           int       `db:"min_replicas" json:"min_replicas"`
	MaxReplicas           int       `db:"max_replicas" json:"max_replicas"`
	ScaleOnCPU            int       `db:"scale_on_cpu" json:"scale_on_cpu"`
	CronSchedule          *string   `db:"cron_schedule" json:"cron_schedule,omitempty"`
	IsPublic              bool      `db:"is_public" json:"is_public"`
	CustomDomain          *string   `db:"custom_domain" json:"custom_domain,omitempty"`
	Subdomain             *string   `db:"subdomain" json:"subdomain,omitempty"`
	HealthCheckPath       string    `db:"health_check_path" json:"health_check_path"`
	LastDeployedAt        *time.Time `db:"last_deployed_at" json:"last_deployed_at,omitempty"`
	CreatedAt             time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt             time.Time  `db:"updated_at" json:"updated_at"`
}

type AppEnvVar struct {
	ID        uuid.UUID `db:"id" json:"id"`
	AppID     uuid.UUID `db:"app_id" json:"app_id"`
	Key       string    `db:"key" json:"key"`
	Value     string    `db:"value" json:"value"`
	IsSecret  bool      `db:"is_secret" json:"is_secret"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

type EnvGroup struct {
	ID        uuid.UUID `db:"id" json:"id"`
	ProjectID uuid.UUID `db:"project_id" json:"project_id"`
	ClusterID uuid.UUID `db:"cluster_id" json:"cluster_id"`
	Name      string    `db:"name" json:"name"`
	Version   int       `db:"version" json:"version"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// ============================================================
// BUILD & DEPLOYMENT MODELS
// ============================================================

type Build struct {
	ID          uuid.UUID `db:"id" json:"id"`
	AppID       uuid.UUID `db:"app_id" json:"app_id"`
	Status      string    `db:"status" json:"status"` // queued, building, success, failed
	CommitSHA   *string   `db:"commit_sha" json:"commit_sha,omitempty"`
	CommitMsg   *string   `db:"commit_msg" json:"commit_msg,omitempty"`
	CommitAuthor *string  `db:"commit_author" json:"commit_author,omitempty"`
	Branch      *string   `db:"branch" json:"branch,omitempty"`
	ImageURL    *string   `db:"image_url" json:"image_url,omitempty"`
	Logs        *string   `db:"logs" json:"logs,omitempty"`
	ErrorMsg    *string   `db:"error_msg" json:"error_msg,omitempty"`
	StartedAt   *time.Time `db:"started_at" json:"started_at,omitempty"`
	FinishedAt  *time.Time `db:"finished_at" json:"finished_at,omitempty"`
	CreatedAt   time.Time  `db:"created_at" json:"created_at"`
}

type Deployment struct {
	ID              uuid.UUID      `db:"id" json:"id"`
	AppID           uuid.UUID      `db:"app_id" json:"app_id"`
	BuildID         *uuid.UUID     `db:"build_id" json:"build_id,omitempty"`
	Revision        int            `db:"revision" json:"revision"`
	Status          string         `db:"status" json:"status"` // deploying, successful, failed, rolled_back
	ImageURL        *string        `db:"image_url" json:"image_url,omitempty"`
	ConfigSnapshot  JSON           `db:"config_snapshot" json:"config_snapshot"`
	DeployedBy      *uuid.UUID     `db:"deployed_by" json:"deployed_by,omitempty"`
	RollbackOf      *uuid.UUID     `db:"rollback_of" json:"rollback_of,omitempty"`
	CreatedAt       time.Time      `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time      `db:"updated_at" json:"updated_at"`
}

type DeploymentEvent struct {
	ID           uuid.UUID      `db:"id" json:"id"`
	AppID        uuid.UUID      `db:"app_id" json:"app_id"`
	DeploymentID *uuid.UUID     `db:"deployment_id" json:"deployment_id,omitempty"`
	Type         string         `db:"type" json:"type"`
	Message      string         `db:"message" json:"message"`
	Metadata     JSON           `db:"metadata" json:"metadata"`
	CreatedAt    time.Time      `db:"created_at" json:"created_at"`
}

// ============================================================
// CUSTOM TYPES
// ============================================================

type JSON map[string]interface{}

func (j JSON) Value() (driver.Value, error) {
	return json.Marshal(j)
}

func (j *JSON) Scan(value interface{}) error {
	if value == nil {
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return nil
	}
	return json.Unmarshal(bytes, &j)
}
