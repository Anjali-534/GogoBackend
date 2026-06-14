package middleware

import (
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// CORSMiddleware configures CORS for the API
func CORSMiddleware() gin.HandlerFunc {
	config := cors.DefaultConfig()
	config.AllowOrigins = []string{"*"}

	config.AllowMethods = []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"}
	config.AllowHeaders = []string{"Authorization", "Content-Type", "X-Requested-With"}
	config.ExposeHeaders = []string{"X-Total-Count"}
	config.AllowCredentials = false
	config.MaxAge = 86400

	return cors.New(config)
}
