package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

var gitHubOAuthConfig *oauth2.Config

type GitHubUser struct {
	ID     int64  `json:"id"`
	Login  string `json:"login"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Avatar string `json:"avatar_url"`
}

func InitGitHub(cfg *config.Config) {
	gitHubOAuthConfig = &oauth2.Config{
		ClientID:     cfg.GitHubClientID,
		ClientSecret: cfg.GitHubClientSecret,
		RedirectURL:  cfg.GitHubRedirectURL,
		Scopes: []string{
			"repo",
			"workflow",
			"user:email",
			"admin:repo_hook",
		},
		Endpoint: github.Endpoint,
	}
}

// GetGitHubAuthURL returns the OAuth authorization URL
func GetGitHubAuthURL(state string) string {
	return gitHubOAuthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline)
}

// ExchangeGitHubCode exchanges auth code for access token
func ExchangeGitHubCode(ctx context.Context, code string) (string, error) {
	token, err := gitHubOAuthConfig.Exchange(ctx, code)
	if err != nil {
		return "", fmt.Errorf("failed to exchange code: %w", err)
	}
	return token.AccessToken, nil
}

// GetGitHubUser fetches GitHub user info
func GetGitHubUser(ctx context.Context, accessToken string) (*GitHubUser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var user GitHubUser
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, err
	}

	return &user, nil
}

// GetGitHubUserEmail fetches GitHub user email
func GetGitHubUserEmail(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user/emails", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var emails []struct {
		Email   string `json:"email"`
		Primary bool   `json:"primary"`
	}
	if err := json.Unmarshal(body, &emails); err != nil {
		return "", err
	}

	for _, e := range emails {
		if e.Primary {
			return e.Email, nil
		}
	}

	if len(emails) > 0 {
		return emails[0].Email, nil
	}

	return "", fmt.Errorf("no email found")
}

// HandleGitHubCallback handles OAuth callback and creates/updates user
func HandleGitHubCallback(ctx context.Context, code string, cfg *config.Config) (uuid.UUID, string, error) {
	// Exchange code for token
	accessToken, err := ExchangeGitHubCode(ctx, code)
	if err != nil {
		return uuid.Nil, "", err
	}

	// Get user info
	ghUser, err := GetGitHubUser(ctx, accessToken)
	if err != nil {
		return uuid.Nil, "", err
	}

	// Get email
	email, err := GetGitHubUserEmail(ctx, accessToken)
	if err != nil {
		email = fmt.Sprintf("%s@github.com", ghUser.Login)
	}

	pool := db.GetDB().GetPool()

	// Check if user exists
	var userID uuid.UUID
	var userName string
	err = pool.QueryRow(ctx,
		"SELECT id, name FROM users WHERE github_id = $1",
		ghUser.ID,
	).Scan(&userID, &userName)

	if err == pgx.ErrNoRows {
		// Create new user
		userID = uuid.New()
		_, err = pool.Exec(ctx, `
			INSERT INTO users (id, email, name, github_id, github_login, avatar_url, is_verified)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, userID, email, ghUser.Name, ghUser.ID, ghUser.Login, ghUser.Avatar, true)
		if err != nil {
			return uuid.Nil, "", fmt.Errorf("failed to create user: %w", err)
		}
		userName = ghUser.Name
	} else if err != nil {
		return uuid.Nil, "", err
	}

	// Store OAuth token
	expiresAt := time.Now().Add(time.Hour * 24 * 30)
	_, err = pool.Exec(ctx, `
		INSERT INTO oauth_tokens (id, user_id, provider, access_token, expires_at, scopes)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id, provider) DO UPDATE SET
			access_token = $4,
			expires_at = $5
	`, uuid.New(), userID, "github", accessToken, expiresAt, "{repo,workflow,user:email,admin:repo_hook}")

	// Generate JWT token
	token, err := GenerateToken(userID, email, userName, "", cfg)
	if err != nil {
		return uuid.Nil, "", err
	}

	return userID, token, nil
}

// ListGitHubRepos lists user's GitHub repositories
func ListGitHubRepos(ctx context.Context, userID uuid.UUID) ([]map[string]interface{}, error) {
	pool := db.GetDB().GetPool()

	// Get GitHub token
	var token string
	err := pool.QueryRow(ctx,
		"SELECT access_token FROM oauth_tokens WHERE user_id = $1 AND provider = $2",
		userID, "github",
	).Scan(&token)
	if err != nil {
		return nil, fmt.Errorf("no github token found")
	}

	// Fetch repos
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user/repos?per_page=100&sort=updated", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var repos []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
		return nil, err
	}

	return repos, nil
}

// CreateGitHubWebhook creates a webhook for a repository
func CreateGitHubWebhook(ctx context.Context, userID uuid.UUID, owner, repo, webhookURL string) error {
	pool := db.GetDB().GetPool()

	// Get GitHub token
	var token string
	err := pool.QueryRow(ctx,
		"SELECT access_token FROM oauth_tokens WHERE user_id = $1 AND provider = $2",
		userID, "github",
	).Scan(&token)
	if err != nil {
		return fmt.Errorf("no github token found")
	}

	// Create webhook
	payload := map[string]interface{}{
		"name":   "web",
		"active": true,
		"events": []string{"push", "pull_request"},
		"config": map[string]interface{}{
			"url":          webhookURL,
			"content_type": "json",
			"secret":       "your-secret-here",
		},
	}

	body, _ := json.Marshal(payload)

	req, _ := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("https://api.github.com/repos/%s/%s/hooks", owner, repo),
		bytes.NewBuffer(body),
	)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode > 299 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github error: %s", string(body))
	}

	return nil
}

// WriteGitHubActionsYAML writes the CI/CD workflow file to a GitHub repo
func WriteGitHubActionsYAML(ctx context.Context, userID uuid.UUID, owner, repo, appName string) error {
	pool := db.GetDB().GetPool()

	// Get GitHub token
	var token string
	err := pool.QueryRow(ctx,
		"SELECT access_token FROM oauth_tokens WHERE user_id = $1 AND provider = $2",
		userID, "github",
	).Scan(&token)
	if err != nil {
		return fmt.Errorf("no github token found")
	}

	// GitHub Actions workflow content
	yamlContent := fmt.Sprintf(`name: Deploy %s

on:
  push:
    branches:
      - main
  pull_request:
    branches:
      - main

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: deploykit/cli-action@v1
        with:
          app-name: %s
          token: ${{ secrets.DEPLOYKIT_TOKEN }}
`, appName, appName)

	// Encode content to base64
	encoded := url.QueryEscape(yamlContent)

	// Create file via GitHub API
	filePayload := map[string]interface{}{
		"message": fmt.Sprintf("Add Deploykit CI/CD for %s", appName),
		"content": encoded,
	}

	filebody, _ := json.Marshal(filePayload)

	req, _ := http.NewRequestWithContext(ctx, "PUT",
		fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/.github/workflows/deploykit.yml", owner, repo),
		bytes.NewBuffer(filebody),
	)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode > 299 {
		return fmt.Errorf("failed to write file")
	}

	return nil
}
