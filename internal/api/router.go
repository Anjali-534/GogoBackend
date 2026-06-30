package api

import (
	"github.com/deploykit/backend/internal/api/handlers"
	"github.com/deploykit/backend/internal/api/middleware"
	"github.com/deploykit/backend/internal/config"
	"github.com/gin-gonic/gin"
)

func SetupRouter(cfg *config.Config) *gin.Engine {
	router := gin.Default()

	// Add middlewares
	router.Use(middleware.CORSMiddleware())
	router.Use(func(c *gin.Context) {
		c.Set("config", cfg)
		c.Next()
	})

	// ============================================================
	// PUBLIC ROUTES
	// ============================================================
	public := router.Group("")
	{
		// Health checks
		public.GET("/health", handlers.Health)
		public.GET("/ready", handlers.Ready)

		// Auth
		public.POST("/auth/signup", handlers.Signup)
		public.POST("/auth/login", handlers.Login)
		public.POST("/auth/refresh", handlers.Refresh)

		// GitHub OAuth
		public.GET("/auth/github", handlers.GitHubAuthURL)
		public.GET("/auth/github/callback", handlers.GitHubCallback)
	}

	// ============================================================
	// AUTHENTICATED ROUTES
	// ============================================================
	protected := router.Group("")
	protected.Use(middleware.AuthMiddleware())
	{
		// User
		protected.GET("/auth/me", handlers.Me)
		protected.POST("/auth/logout", handlers.Logout)

		// Projects
		protected.GET("/projects", handlers.ListProjects)
		protected.POST("/projects", handlers.CreateProject)
		protected.GET("/projects/:id", handlers.GetProject)

		// Clusters
		protected.GET("/projects/:id/clusters", handlers.ListClusters)
		protected.POST("/projects/:id/clusters", handlers.CreateCluster)
		protected.GET("/clusters/:id", handlers.GetCluster)

		// Apps
		protected.GET("/clusters/:id/apps", handlers.ListApps)
		protected.POST("/clusters/:id/apps", handlers.CreateApp)
		protected.GET("/apps/:id", handlers.GetApp)

		// Builds
		protected.POST("/builds", handlers.TriggerBuild)
		protected.GET("/builds/:id/logs", handlers.GetBuildLogs)
		protected.GET("/apps/:id/builds", handlers.ListBuilds)

		// Deployments
		protected.POST("/deployments", handlers.TriggerDeployment)
		protected.GET("/deployments/:id", handlers.GetDeploymentStatus)
		protected.GET("/apps/:id/deployments", handlers.ListDeployments)
		protected.POST("/apps/:id/rollback/:revision", handlers.RollbackDeployment)

		// AWS Provisioning
		protected.POST("/projects/:id/aws/link", handlers.LinkAWSAccount)
		protected.POST("/projects/:id/provision/aws", handlers.ProvisionAWSCluster)

	}

	// Serve uploaded files
	router.Static("/uploads", "./uploads")

	// ============================================================
	// GOGOO â€" PUBLIC ROUTES
	// ============================================================
	gogooPublic := router.Group("/gogoo")
	{
		gogooPublic.POST("/rider/signup", handlers.RiderSignup)
		gogooPublic.POST("/driver/signup", handlers.DriverSignup)
		gogooPublic.GET("/services", handlers.ListServiceTypes)
		gogooPublic.POST("/panel-login",    handlers.PanelLogin)
		gogooPublic.POST("/hospital-login", handlers.HospitalLogin)
		gogooPublic.GET("/ambulance/hospitals/nearby", handlers.GetNearbyHospitals)
	}

	// ============================================================
	// GOGOO â€" PROTECTED ROUTES
	// ============================================================
	gogoo := router.Group("/gogoo")
	gogoo.Use(middleware.AuthMiddleware())
	{
		// Bookings
		gogoo.POST("/bookings", handlers.CreateBooking)
		gogoo.GET("/bookings", handlers.ListBookings)
		gogoo.GET("/bookings-pending", handlers.ListPendingBookings)
		gogoo.GET("/bookings/:id", handlers.GetBooking)
        gogoo.POST("/bookings/:id/rate", handlers.RateBooking)
        gogoo.POST("/bookings/:id/accept", handlers.AcceptBooking)
        gogoo.POST("/bookings/:id/verify-otp", handlers.VerifyRideOTP)
		gogoo.PATCH("/bookings/:id/status", handlers.UpdateBookingStatus)

		// Drivers
		gogoo.GET("/drivers", handlers.ListDrivers)
		gogoo.GET("/drivers/:id", handlers.GetDriverByID)
		gogoo.PATCH("/drivers/:id/verify", handlers.VerifyDriver)
		gogoo.PATCH("/drivers/:id/online", handlers.ToggleDriverOnline)
		gogoo.POST("/drivers/:id/location", handlers.UpdateDriverLocation)

		// Riders (dashboard)
		gogoo.GET("/riders", handlers.ListRiders)

		// Driver profile & history
		gogoo.GET("/driver/profile", handlers.GetDriverProfile)
		gogoo.GET("/driver/active-booking", handlers.GetDriverActiveBooking)
		gogoo.GET("/driver/bookings", handlers.ListDriverBookings)
        gogoo.GET("/driver/reviews", handlers.GetDriverReviews)
        gogoo.GET("/rider/profile", handlers.GetRiderProfile)
		gogoo.GET("/rider/bookings", handlers.ListRiderBookings)
		gogoo.GET("/rider/saved-places", handlers.GetSavedPlaces)
		gogoo.POST("/rider/saved-places", handlers.SavePlace)
		gogoo.DELETE("/rider/saved-places/:label", handlers.DeleteSavedPlace)
		// Driver ride history + block management (admin)
		gogoo.GET("/drivers/:id/bookings",  handlers.ListDriverBookingsByID)
		gogoo.PATCH("/drivers/:id/block",   handlers.ManageDriverBlock)
		// Rider ride history (admin)
		gogoo.GET("/riders/:id/bookings",   handlers.ListRiderBookingsByID)

		// Documents
		gogoo.GET("/drivers/:id/documents", handlers.GetDriverDocuments)
		gogoo.POST("/drivers/:id/documents", handlers.UploadDriverDocument)
		gogoo.PATCH("/drivers/:id/documents/:doc_type/review", handlers.ReviewDriverDocument)
		gogoo.DELETE("/drivers/:id/documents/:doc_type", handlers.DeleteDriverDocument)

		// Payments
		gogoo.GET("/payments", handlers.ListPayments)

		// Driver wallet / ledger / earnings
		gogoo.GET("/driver/wallet",           handlers.GetDriverWallet)
		gogoo.GET("/driver/ledger",           handlers.GetDriverLedger)
		gogoo.GET("/driver/earnings/summary", handlers.GetEarningsSummary)

		// Admin driver payments
		gogoo.GET("/admin/driver-payments", handlers.AdminDriverPayments)

		// Analytics
		gogoo.GET("/analytics",                  handlers.GetAnalytics)
		gogoo.POST("/analytics/event",           handlers.RecordAnalyticsEvent)
		gogoo.GET("/analytics/screen-times",     handlers.GetScreenTimes)
		gogoo.GET("/analytics/geo-distribution", handlers.GetGeoDistribution)
		gogoo.GET("/analytics/device-breakdown", handlers.GetDeviceBreakdown)
		gogoo.GET("/analytics/retention",        handlers.GetRetentionStats)
		gogoo.GET("/analytics/sessions",         handlers.GetSessionStats)
		gogoo.GET("/analytics/usage-heatmap",    handlers.GetUsageHeatmap)
		gogoo.GET("/analytics/funnel",           handlers.GetFunnelData)

		// Notifications (riders)
		gogoo.GET("/notifications",                       handlers.ListNotifications)
		gogoo.GET("/notifications/unread-count",          handlers.GetNotificationUnreadCount)
		gogoo.POST("/notifications/:id/read",             handlers.MarkNotificationRead)

		// Notifications (drivers)
		gogoo.GET("/driver/notifications",                handlers.ListDriverNotifications)
		gogoo.GET("/driver/notifications/unread-count",   handlers.GetDriverNotificationUnreadCount)
		gogoo.POST("/driver/notifications/:id/read",      handlers.MarkNotificationRead)

		// Push token registration (riders + drivers share same endpoint)
		gogoo.POST("/push-token",                         handlers.RegisterPushToken)

		// Broadcasts (admin)
		gogoo.POST("/admin/notifications",                handlers.CreateNotification)
		gogoo.GET("/admin/notifications",                 handlers.AdminListNotifications)
		gogoo.DELETE("/admin/notifications/:id",          handlers.DeleteNotification)

		// Panel access management (admin)
		gogoo.GET("/admin/panel-access",                  handlers.GetPanelAccess)
		gogoo.PATCH("/admin/panel-access/:id/password",   handlers.UpdatePanelPassword)

		// Ambulance — NGO management
		gogoo.GET("/ambulance/ngos",                      handlers.GetNGOs)
		gogoo.POST("/ambulance/ngos",                     handlers.CreateNGO)
		gogoo.PATCH("/ambulance/ngos/:id",                handlers.UpdateNGO)
		gogoo.DELETE("/ambulance/ngos/:id",               handlers.DeleteNGO)

		// Ambulance — Hospital management
		gogoo.GET("/ambulance/hospitals",                 handlers.GetHospitals)
		gogoo.GET("/ambulance/hospitals/:id",             handlers.GetHospitalByID)
		gogoo.POST("/ambulance/hospitals",                handlers.CreateHospital)
		gogoo.PATCH("/ambulance/hospitals/:id",           handlers.UpdateHospital)
		gogoo.DELETE("/ambulance/hospitals/:id",          handlers.DeleteHospital)
		gogoo.PATCH("/ambulance/hospitals/:id/password",  handlers.ResetHospitalPassword)

		// Ambulance — Bookings
		gogoo.GET("/ambulance/bookings/hospital",         handlers.GetHospitalBookings)
		gogoo.POST("/ambulance/bookings/hospital",        handlers.CreateHospitalBooking)
		gogoo.PATCH("/ambulance/bookings/hospital/:id/status", handlers.UpdateHospitalBookingStatus)
		gogoo.GET("/ambulance/all-bookings",              handlers.GetAmbulanceAllBookings)

		// Support panel
		gogoo.GET("/support/tickets",                     handlers.GetSupportTickets)
		gogoo.POST("/support/tickets",                    handlers.CreateSupportTicket)
		gogoo.PATCH("/support/tickets/:id",               handlers.UpdateSupportTicket)
		gogoo.POST("/support/tickets/:id/refund",         handlers.ProcessRefund)
		gogoo.GET("/support/tickets/:id/messages",        handlers.GetTicketMessages)
		gogoo.POST("/support/tickets/:id/messages",       handlers.SendTicketMessage)
		gogoo.GET("/support/stats",                       handlers.GetSupportStats)
		gogoo.POST("/support/cancel-booking/:id",         handlers.SupportCancelBooking)
		gogoo.POST("/support/block-rider/:id",            handlers.SupportBlockRider)

		// In-app chat (rider + driver apps)
		gogoo.POST("/support/chat/start",                 handlers.StartSupportChat)
		gogoo.GET("/support/chat/my-tickets",             handlers.GetMyTickets)
		gogoo.GET("/support/chat/:ticket_id/messages",    handlers.GetChatMessages)
		gogoo.POST("/support/chat/:ticket_id/messages",   handlers.SendChatMessage)
		gogoo.GET("/support/unread-count",                handlers.GetUnreadCount)
	}

	// ============================================================
	// GOGOO â€" EXCEL EXPORTS (token via header or ?token= query)
	// ============================================================
	gogooExport := router.Group("/gogoo/export")
	gogooExport.Use(middleware.DownloadAuthMiddleware())
	{
		gogooExport.GET("/drivers.xlsx", handlers.ExportDriversXLSX)
		gogooExport.GET("/users.xlsx", handlers.ExportUsersXLSX)
	}

	return router
}



