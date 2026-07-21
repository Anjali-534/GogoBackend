package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	// Server
	Port              int
	Environment       string
	LogLevel          string
	
	// Database
	DBHost            string
	DBPort            int
	DBUser            string
	DBPassword        string
	DBName            string
	DBMaxConnections  int
	
	// JWT
	JWTSecret         string
	JWTExpiration     time.Duration
	
	// GitHub OAuth
	GitHubClientID    string
	GitHubClientSecret string
	GitHubRedirectURL string
	
	// GitLab OAuth
	GitLabClientID    string
	GitLabClientSecret string
	GitLabRedirectURL string
	
	// Frontend URLs
	DashboardURL      string
	APIBaseURL        string
	
	// Redis
	RedisHost         string
	RedisPort         int
	
	// Stripe
	StripeKey         string
	StripeWebhookSecret string
	
	// AWS
	AWSRegion         string
	
	// Kubernetes
	KubeConfig        string

	// SMTP_HOST/PORT/USER/PASSWORD are unused for sending — Railway blocks
	// outbound SMTP (confirmed: port 587 times out). Email actually goes out
	// via Resend's HTTP API (RESEND_API_KEY); SMTP_FROM_EMAIL/SMTP_FROM_NAME
	// are kept since they still describe the sender identity either way.
	SMTPHost      string
	SMTPPort      int
	SMTPUser      string
	SMTPPassword  string
	SMTPFromEmail string
	SMTPFromName  string

	// Resend (transactional email — e.g. monthly driver earnings statements).
	// bogie.in is verified on Resend, so sending works for real recipients,
	// not just the account owner's own address (sandbox mode's restriction).
	ResendAPIKey    string
	ResendFromEmail string

	// TrackerPanelURL is the login link sent in the tracker company approval
	// email. The bogie-tracker-panel frontend isn't deployed yet — this
	// placeholder MUST be overridden with the real panel URL via
	// TRACKER_PANEL_URL once it is.
	TrackerPanelURL string
}

func Load() *Config {
	cfg := &Config{
		Port:              getInt("PORT", 8080),
		Environment:       getString("ENVIRONMENT", "development"),
		LogLevel:          getString("LOG_LEVEL", "info"),
		
		DBHost:            getString("DB_HOST", "localhost"),
		DBPort:            getInt("DB_PORT", 5432),
		DBUser:            getString("DB_USER", "deploykit"),
		DBPassword:        getString("DB_PASSWORD", "deploykit"),
		DBName:            getString("DB_NAME", "deploykit"),
		DBMaxConnections:  getInt("DB_MAX_CONNECTIONS", 25),
		
		JWTSecret:        getString("JWT_SECRET", ""),
		JWTExpiration:    time.Hour * 24 * 30,
		
		GitHubClientID:   getString("GITHUB_CLIENT_ID", ""),
		GitHubClientSecret: getString("GITHUB_CLIENT_SECRET", ""),
		GitHubRedirectURL: getString("GITHUB_REDIRECT_URL", "http://localhost:3000/auth/github/callback"),
		
		GitLabClientID:   getString("GITLAB_CLIENT_ID", ""),
		GitLabClientSecret: getString("GITLAB_CLIENT_SECRET", ""),
		GitLabRedirectURL: getString("GITLAB_REDIRECT_URL", "http://localhost:3000/auth/gitlab/callback"),
		
		DashboardURL:     getString("DASHBOARD_URL", "http://localhost:3000"),
		APIBaseURL:       getString("API_BASE_URL", "http://localhost:8080"),
		
		RedisHost:        getString("REDIS_HOST", "localhost"),
		RedisPort:        getInt("REDIS_PORT", 6379),
		
		StripeKey:        getString("STRIPE_KEY", ""),
		StripeWebhookSecret: getString("STRIPE_WEBHOOK_SECRET", ""),
		
		AWSRegion:        getString("AWS_REGION", "us-east-1"),
		
		KubeConfig:       getString("KUBECONFIG", ""),

		SMTPHost:      getString("SMTP_HOST", ""),
		SMTPPort:      getInt("SMTP_PORT", 587),
		SMTPUser:      getString("SMTP_USER", ""),
		SMTPPassword:  getString("SMTP_PASSWORD", ""),
		SMTPFromEmail: getString("SMTP_FROM_EMAIL", ""),
		SMTPFromName:  getString("SMTP_FROM_NAME", "Bogie"),

		ResendAPIKey:    getString("RESEND_API_KEY", ""),
		ResendFromEmail: getString("RESEND_FROM_EMAIL", "statements@bogie.in"),

		TrackerPanelURL: getString("TRACKER_PANEL_URL", "https://bogie-tracker.bogie.in"),
	}

	return cfg
}

func getString(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultVal
}
