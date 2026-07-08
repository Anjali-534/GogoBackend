package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/deploykit/backend/internal/api"
	"github.com/deploykit/backend/internal/api/handlers"
	"github.com/deploykit/backend/internal/auth"
	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/db"
	"github.com/deploykit/backend/internal/services/ledger"
	"github.com/joho/godotenv"
)

func main() {
	// Load .env file
	_ = godotenv.Load()

	// Load config
	cfg := config.Load()

	// Initialize JWT
	auth.Init(cfg)

	// Initialize GitHub OAuth
	auth.InitGitHub(cfg)

	// Initialize database
	if err := db.Init(cfg); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.GetDB().Close()

	log.Println("✓ Database connected")

	// Run migrations
	if err := handlers.MigrateNotifications(); err != nil {
		log.Printf("⚠ Notifications migration warning: %v", err)
	} else {
		log.Println("✓ Notifications tables ready")
	}
	if err := handlers.MigrateReferrals(); err != nil {
		log.Printf("⚠ Referrals migration warning: %v", err)
	} else {
		log.Println("✓ Referral tables ready")
	}
	if err := handlers.MigrateSOS(); err != nil {
		log.Printf("⚠ SOS migration warning: %v", err)
	} else {
		log.Println("✓ SOS ticket type ready")
	}
	if err := ledger.MigrateSentStatements(); err != nil {
		log.Printf("⚠ Sent-statements migration warning: %v", err)
	} else {
		log.Println("✓ Sent-statements table ready")
	}
	if err := handlers.MigrateRideMessages(); err != nil {
		log.Printf("⚠ Ride-messages migration warning: %v", err)
	} else {
		log.Println("✓ Ride-chat table ready")
	}

	// Scheduled-ride dispatcher — ticks scheduled bookings into the normal
	// searching/matching flow ~15 minutes before pickup.
	go handlers.StartScheduledDispatcher()
	log.Println("✓ Scheduled ride dispatcher running")

	// Monthly driver earnings statement emailer — ticks daily, sends on the 1st.
	go ledger.StartMonthlyStatementMailer(cfg)
	log.Println("✓ Monthly statement mailer running")

	// Setup API router
	router := api.SetupRouter(cfg)

	log.Println("✓ API routes configured")

	// Start server
	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("🚀 Server starting on %s\n", addr)

	// Handle graceful shutdown
	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt, syscall.SIGTERM)
		<-sigint

		log.Println("\n🛑 Shutting down...")
		os.Exit(0)
	}()

	if err := router.Run(addr); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
