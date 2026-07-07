package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/deploykit/backend/internal/config"
)

type Claims struct {
	UserID    uuid.UUID  `json:"user_id"`
	Email     string     `json:"email"`
	Name      string     `json:"name"`
	Panel     string     `json:"panel,omitempty"`
	Role      string     `json:"role,omitempty"`
	ProjectID *uuid.UUID `json:"project_id,omitempty"`
	jwt.RegisteredClaims
}

type TokenResponse struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresIn    int       `json:"expires_in"`
	TokenType    string    `json:"token_type"`
}

var jwtSecret string

// Init initializes the JWT secret
func Init(cfg *config.Config) {
	jwtSecret = cfg.JWTSecret
}

// GenerateToken generates a JWT token. role is "" for ordinary riders/drivers
// and "master_admin" for the platform admin account — deny-by-default
// middleware treats a blank role/panel as no panel access.
func GenerateToken(userID uuid.UUID, email, name, role string, cfg *config.Config) (string, error) {
	expirationTime := time.Now().Add(cfg.JWTExpiration)

	claims := &Claims{
		UserID: userID,
		Email:  email,
		Name:   name,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expirationTime),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
			Issuer:    "deploykit",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(jwtSecret))
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return tokenString, nil
}

// ValidateToken validates a JWT token and returns the claims
func ValidateToken(tokenString string) (*Claims, error) {
	claims := &Claims{}

	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(jwtSecret), nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	return claims, nil
}

// RefreshToken generates a new token from an existing valid token
func RefreshToken(tokenString string, cfg *config.Config) (string, error) {
	claims, err := ValidateToken(tokenString)
	if err != nil {
		return "", err
	}

	return GenerateToken(claims.UserID, claims.Email, claims.Name, claims.Role, cfg)
}
