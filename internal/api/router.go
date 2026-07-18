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

		// Referral smart-redirect landing pages (shared via WhatsApp etc.) —
		// bogie.in isn't wired up to host these yet, so the backend itself serves these.
		public.GET("/r/:code", handlers.ReferralLandingUser)
		public.GET("/dr/:code", handlers.ReferralLandingDriver)
		public.GET("/driver-app", handlers.DriverAppLanding)
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

	// Serve legal/policy PDFs (Terms & Conditions, Privacy Policy, TDS Declaration)
	router.Static("/policies", "./static/policies")

	// ============================================================
	// GOGOO â€" PUBLIC ROUTES
	// ============================================================
	gogooPublic := router.Group("/gogoo")
	{
		gogooPublic.POST("/rider/signup", handlers.RiderSignup)
		gogooPublic.POST("/driver/signup", handlers.DriverSignup)
		gogooPublic.GET("/services", handlers.ListServiceTypes)
		gogooPublic.POST("/panel-login", handlers.PanelLogin)
		gogooPublic.POST("/hospital-login", handlers.HospitalLogin)
		gogooPublic.GET("/ambulance/hospitals/nearby", handlers.GetNearbyHospitals)
		gogooPublic.POST("/referral/validate", handlers.ValidateReferralCode)
		gogooPublic.GET("/stats/public", handlers.GetPublicStats)
		gogooPublic.GET("/reviews/platform/public", handlers.GetPlatformReviewsPublic)

		// Bogie Tracker — company self-signup/login and the unauthenticated
		// public tracking page (protected only by the unguessable token).
		gogooPublic.POST("/tracker/signup", handlers.TrackerCompanySignup)
		gogooPublic.POST("/tracker/verify-email", handlers.VerifyTrackerCompanyEmail)
		gogooPublic.POST("/tracker/resend-otp", handlers.ResendTrackerCompanyOTP)
		gogooPublic.POST("/tracker/login", handlers.TrackerCompanyLogin)
		gogooPublic.GET("/public/tracker/orders/:token", handlers.GetPublicTrackerOrder)

		// Bogie Tracker — driver share-link (live location), protected only
		// by the unguessable driver_tracking_token, same model as above.
		gogooPublic.GET("/public/tracker/driver/:driver_token", handlers.GetTrackerDriverOrder)
		gogooPublic.POST("/public/tracker/driver/:driver_token/location", handlers.PostTrackerDriverLocation)
		// Driver quick-status events, proof-of-delivery signature, and the
		// company->driver message feed — same unguessable-token-only model.
		gogooPublic.POST("/public/tracker/driver/:driver_token/event", handlers.PostTrackerDriverEvent)
		gogooPublic.POST("/public/tracker/driver/:driver_token/signature", handlers.UploadTrackerDriverSignature)
		gogooPublic.GET("/public/tracker/driver/:driver_token/messages", handlers.GetTrackerDriverMessages)

		// Bogie Tracker — consignee goods-received receipt confirmation,
		// protected only by the unguessable received_confirmation_token
		// (deliberately separate from the public tracking token — see
		// tracker_receipt.go).
		gogooPublic.GET("/public/tracker/receipt/:token", handlers.GetTrackerReceiptOrder)
		gogooPublic.POST("/public/tracker/receipt/:token/confirm", handlers.ConfirmTrackerReceipt)
	}

	// ============================================================
	// GOGOO â€" PROTECTED ROUTES
	// ============================================================
	gogoo := router.Group("/gogoo")
	gogoo.Use(middleware.AuthMiddleware())
	{
		// Bookings
		gogoo.POST("/bookings", handlers.CreateBooking)
		gogoo.GET("/bookings", middleware.RequirePanel("cab", "truck", "ambulance", "support"), handlers.ListBookings)
		gogoo.GET("/bookings-pending", handlers.ListPendingBookings)
		gogoo.GET("/bookings/:id", handlers.GetBooking)
		gogoo.GET("/bookings/:id/cancel-preview", handlers.GetCancelPreview)
		gogoo.GET("/bookings/:id/messages", handlers.GetRideMessages)
		gogoo.POST("/bookings/:id/messages", handlers.SendRideMessage)
		gogoo.POST("/bookings/:id/rate", handlers.RateBooking)
		gogoo.POST("/bookings/:id/accept", handlers.AcceptBooking)
		gogoo.POST("/bookings/:id/verify-otp", handlers.VerifyRideOTP)
		gogoo.PATCH("/bookings/:id/status", handlers.UpdateBookingStatus)
		gogoo.POST("/bookings/:id/waive-ambulance-fare", middleware.RequirePanel("ambulance", "support"), handlers.WaiveAmbulanceFare)

		// Drivers
		gogoo.GET("/drivers", middleware.RequirePanel("cab", "truck", "ambulance", "support"), handlers.ListDrivers)
		gogoo.GET("/drivers/:id", middleware.RequirePanel("cab", "truck", "ambulance", "support"), handlers.GetDriverByID)
		// verify/background-check are staff-only actions (deny-by-default);
		// online/location are the driver acting on their own record, checked
		// for ownership inside the handler since panels never call them.
		gogoo.PATCH("/drivers/:id/verify", middleware.RequirePanel("cab", "truck", "ambulance", "support"), handlers.VerifyDriver)
		gogoo.PATCH("/drivers/:id/online", handlers.ToggleDriverOnline)
		gogoo.PATCH("/drivers/:id/background-check", middleware.RequirePanel("cab", "truck", "ambulance", "support"), handlers.UpdateDriverBackgroundCheck)
		gogoo.POST("/drivers/:id/location", handlers.UpdateDriverLocation)
		gogoo.GET("/drivers/nearby-count", handlers.GetNearbyDriverCount)

		// Live map (admin panels)
		gogoo.GET("/live/drivers", handlers.ListLiveDrivers)
		gogoo.GET("/live/bookings", handlers.ListLiveBookings)
		gogoo.GET("/route", handlers.ProxyOlaRoute)
		gogoo.GET("/geocode/reverse", handlers.ReverseGeocodeProxy)

		// Riders (dashboard)
		gogoo.GET("/riders", handlers.ListRiders)

		// Driver profile & history
		gogoo.GET("/driver/profile", handlers.GetDriverProfile)
		gogoo.GET("/driver/active-booking", handlers.GetDriverActiveBooking)
		gogoo.GET("/driver/bookings", handlers.ListDriverBookings)
		gogoo.GET("/driver/reviews", handlers.GetDriverReviews)
		gogoo.POST("/reviews/platform", handlers.SubmitPlatformReview)
		gogoo.GET("/rider/profile", handlers.GetRiderProfile)
		gogoo.GET("/rider/bookings", handlers.ListRiderBookings)
		gogoo.GET("/rider/saved-places", handlers.GetSavedPlaces)
		gogoo.POST("/rider/saved-places", handlers.SavePlace)
		gogoo.DELETE("/rider/saved-places/:label", handlers.DeleteSavedPlace)
		// Driver ride history + block management (admin)
		gogoo.GET("/drivers/:id/bookings", handlers.ListDriverBookingsByID)
		gogoo.PATCH("/drivers/:id/block", middleware.RequirePanel("cab", "truck", "ambulance", "support"), handlers.ManageDriverBlock)
		// Rider ride history (admin)
		gogoo.GET("/riders/:id/bookings", handlers.ListRiderBookingsByID)

		// Documents — GET/POST/DELETE are the driver acting on their own
		// documents (checked for ownership inside the handler) but panels also
		// GET them for review, so that one stays open at the router level;
		// review itself is a staff-only verdict.
		gogoo.GET("/drivers/:id/documents", handlers.GetDriverDocuments)
		gogoo.POST("/drivers/:id/documents", handlers.UploadDriverDocument)
		gogoo.PATCH("/drivers/:id/documents/:doc_type/review", middleware.RequirePanel("cab", "truck", "ambulance", "support"), handlers.ReviewDriverDocument)
		gogoo.DELETE("/drivers/:id/documents/:doc_type", handlers.DeleteDriverDocument)

		// Payments
		gogoo.GET("/payments", handlers.ListPayments)

		// Driver wallet / ledger / earnings
		gogoo.GET("/driver/wallet", handlers.GetDriverWallet)
		gogoo.GET("/driver/ledger", handlers.GetDriverLedger)
		gogoo.GET("/driver/ledger/pdf", handlers.GetDriverLedgerPDF)
		gogoo.GET("/driver/earnings/summary", handlers.GetEarningsSummary)

		// Admin driver payments
		gogoo.GET("/admin/driver-payments", middleware.RequirePanel("support"), handlers.AdminDriverPayments)

		// Analytics
		gogoo.GET("/analytics", handlers.GetAnalytics)
		gogoo.POST("/analytics/event", handlers.RecordAnalyticsEvent)
		gogoo.GET("/analytics/screen-times", handlers.GetScreenTimes)
		gogoo.GET("/analytics/geo-distribution", handlers.GetGeoDistribution)
		gogoo.GET("/analytics/device-breakdown", handlers.GetDeviceBreakdown)
		gogoo.GET("/analytics/retention", handlers.GetRetentionStats)
		gogoo.GET("/analytics/sessions", handlers.GetSessionStats)
		gogoo.GET("/analytics/usage-heatmap", handlers.GetUsageHeatmap)
		gogoo.GET("/analytics/funnel", handlers.GetFunnelData)

		// Notifications (riders)
		gogoo.GET("/notifications", handlers.ListNotifications)
		gogoo.GET("/notifications/unread-count", handlers.GetNotificationUnreadCount)
		gogoo.POST("/notifications/:id/read", handlers.MarkNotificationRead)

		// Notifications (drivers)
		gogoo.GET("/driver/notifications", handlers.ListDriverNotifications)
		gogoo.GET("/driver/notifications/unread-count", handlers.GetDriverNotificationUnreadCount)
		gogoo.POST("/driver/notifications/:id/read", handlers.MarkNotificationRead)

		// Push token registration (riders + drivers share same endpoint)
		gogoo.POST("/push-token", handlers.RegisterPushToken)

		// Referrals (riders + drivers share same endpoints)
		gogoo.GET("/referral/my-code", handlers.GetMyReferralCode)
		gogoo.GET("/referral/my-referrals", handlers.GetMyReferrals)
		gogoo.GET("/referral/all", handlers.AdminListReferrals)

		// Broadcasts (admin) — every operating panel can send within its own
		// category; scoping to that category is enforced inside the handlers.
		gogoo.POST("/admin/notifications", middleware.RequirePanel("cab", "truck", "ambulance", "support"), handlers.CreateNotification)
		gogoo.GET("/admin/notifications", middleware.RequirePanel("cab", "truck", "ambulance", "support"), handlers.AdminListNotifications)
		gogoo.DELETE("/admin/notifications/:id", middleware.RequirePanel("cab", "truck", "ambulance", "support"), handlers.DeleteNotification)

		// Notifications (hospitals) — in-portal inbox, no push mechanism
		gogoo.GET("/ambulance/hospital/notifications", middleware.RequirePanel("hospital"), handlers.ListHospitalNotifications)
		gogoo.GET("/ambulance/hospital/notifications/unread-count", middleware.RequirePanel("hospital"), handlers.GetHospitalNotificationUnreadCount)
		gogoo.POST("/ambulance/hospital/notifications/:id/read", middleware.RequirePanel("hospital"), handlers.MarkNotificationRead)

		// Panel access management (admin)
		gogoo.GET("/admin/panel-access", middleware.RequirePanel(), handlers.GetPanelAccess)
		gogoo.PATCH("/admin/panel-access/:id/password", middleware.RequirePanel(), handlers.UpdatePanelPassword)

		// Ambulance — NGO management
		gogoo.GET("/ambulance/ngos", middleware.RequirePanel("ambulance"), handlers.GetNGOs)
		gogoo.POST("/ambulance/ngos", middleware.RequirePanel("ambulance"), handlers.CreateNGO)
		gogoo.PATCH("/ambulance/ngos/:id", middleware.RequirePanel("ambulance"), handlers.UpdateNGO)
		gogoo.DELETE("/ambulance/ngos/:id", middleware.RequirePanel("ambulance"), handlers.DeleteNGO)

		// Ambulance — Hospital management
		gogoo.GET("/ambulance/hospitals", middleware.RequirePanel("ambulance"), handlers.GetHospitals)
		gogoo.GET("/ambulance/hospitals/:id", middleware.RequirePanel("ambulance"), handlers.GetHospitalByID)
		gogoo.POST("/ambulance/hospitals", middleware.RequirePanel("ambulance"), handlers.CreateHospital)
		gogoo.PATCH("/ambulance/hospitals/:id", middleware.RequirePanel("ambulance"), handlers.UpdateHospital)
		gogoo.DELETE("/ambulance/hospitals/:id", middleware.RequirePanel("ambulance"), handlers.DeleteHospital)
		gogoo.PATCH("/ambulance/hospitals/:id/password", middleware.RequirePanel("ambulance"), handlers.ResetHospitalPassword)

		// Bogie Tracker — dashboard (master admin) oversight of subscribed
		// companies. Staff-only, unscoped across all companies by design —
		// see the comment at the top of tracker_admin.go before reusing any
		// of this against a company-facing route.
		gogoo.GET("/dashboard/tracker/companies", middleware.RequirePanel(), handlers.ListTrackerCompanies)
		gogoo.GET("/dashboard/tracker/companies/:id", middleware.RequirePanel(), handlers.GetTrackerCompany)
		gogoo.POST("/dashboard/tracker/companies/:id/approve", middleware.RequirePanel(), handlers.ApproveTrackerCompany)
		gogoo.POST("/dashboard/tracker/companies/:id/reject", middleware.RequirePanel(), handlers.RejectTrackerCompany)
		gogoo.POST("/dashboard/tracker/companies/:id/suspend", middleware.RequirePanel(), handlers.SuspendTrackerCompany)
		gogoo.GET("/dashboard/tracker/companies/:id/drivers", middleware.RequirePanel(), handlers.GetTrackerCompanyDrivers)
		gogoo.GET("/dashboard/tracker/companies/:id/orders", middleware.RequirePanel(), handlers.GetTrackerCompanyOrders)
		gogoo.GET("/dashboard/tracker/companies/:id/orders/:orderId", middleware.RequirePanel(), handlers.GetTrackerCompanyOrderDetail)
		gogoo.GET("/dashboard/tracker/companies/:id/plan-orders", middleware.RequirePanel(), handlers.GetTrackerCompanyPlanOrders)
		gogoo.POST("/dashboard/tracker/plan-orders/:id/mark-paid", middleware.RequirePanel(), handlers.MarkTrackerPlanOrderPaid)

		// Bogie Tracker — company-facing panel. Every route is scoped to the
		// caller's own company_id (see comment at the top of tracker.go); this
		// is the opposite scoping rule from the /dashboard/tracker/* admin
		// routes above, which deliberately see across all companies.
		gogoo.GET("/tracker/company/profile", middleware.RequireTrackerCompany(), handlers.GetTrackerCompanyProfile)
		gogoo.PATCH("/tracker/company/profile", middleware.RequireTrackerCompany(), handlers.UpdateTrackerCompanyProfile)
		gogoo.POST("/tracker/company/password", middleware.RequireTrackerCompany(), handlers.UpdateTrackerCompanyPassword)
		gogoo.GET("/tracker/drivers", middleware.RequireTrackerCompany(), handlers.ListTrackerCompanyOwnDrivers)
		gogoo.POST("/tracker/drivers", middleware.RequireTrackerCompany(), handlers.CreateTrackerCompanyDriver)
		gogoo.PATCH("/tracker/drivers/:id", middleware.RequireTrackerCompany(), handlers.UpdateTrackerCompanyDriver)
		gogoo.DELETE("/tracker/drivers/:id", middleware.RequireTrackerCompany(), handlers.DeactivateTrackerCompanyDriver)
		gogoo.GET("/tracker/orders", middleware.RequireTrackerCompany(), handlers.ListTrackerCompanyOwnOrders)
		gogoo.POST("/tracker/orders", middleware.RequireTrackerCompany(), handlers.CreateTrackerCompanyOrder)
		gogoo.GET("/tracker/orders/:id", middleware.RequireTrackerCompany(), handlers.GetTrackerCompanyOwnOrder)
		gogoo.PATCH("/tracker/orders/:id", middleware.RequireTrackerCompany(), handlers.UpdateTrackerCompanyOrderStatus)
		gogoo.PATCH("/tracker/orders/:id/details", middleware.RequireTrackerCompany(), handlers.UpdateTrackerCompanyOrderDetails)
		gogoo.POST("/tracker/orders/:id/events", middleware.RequireTrackerCompany(), handlers.AddTrackerCompanyOrderEvent)
		gogoo.POST("/tracker/orders/:id/eway-bill", middleware.RequireTrackerCompany(), handlers.UploadTrackerOrderEwayBill)
		gogoo.POST("/tracker/orders/:id/messages", middleware.RequireTrackerCompany(), handlers.SendTrackerOrderMessage)
		gogoo.POST("/tracker/orders/:id/notify", middleware.RequireTrackerCompany(), handlers.NotifyTrackerOrderStakeholders)
		gogoo.POST("/tracker/plan-orders", middleware.RequireTrackerCompany(), handlers.CreateTrackerPlanOrder)
		gogoo.GET("/tracker/plan-orders", middleware.RequireTrackerCompany(), handlers.ListTrackerPlanOrders)
		gogoo.GET("/tracker/plan-orders/:id/invoice", middleware.RequireTrackerCompany(), handlers.GetTrackerPlanOrderInvoice)

		// Ambulance — Bookings
		gogoo.GET("/ambulance/bookings/hospital", middleware.RequirePanel("hospital", "ambulance"), handlers.GetHospitalBookings)
		gogoo.POST("/ambulance/bookings/hospital", middleware.RequirePanel("hospital"), handlers.CreateHospitalBooking)
		gogoo.PATCH("/ambulance/bookings/hospital/:id/status", middleware.RequirePanel("hospital", "ambulance"), handlers.UpdateHospitalBookingStatus)
		gogoo.GET("/ambulance/all-bookings", middleware.RequirePanel("ambulance"), handlers.GetAmbulanceAllBookings)

		// Support panel — listing all tickets and reading arbitrary tickets'
		// messages is support-staff-only (tickets carry names, phones, and SOS
		// live locations). Riders/drivers have their own ownership-checked
		// path: /support/chat/my-tickets + /support/chat/:ticket_id/messages.
		// PATCH stays ungated for now: the rider app calls it to mark its own
		// ticket resolved (user-app support/chat.tsx).
		gogoo.GET("/support/tickets", middleware.RequirePanel("support"), handlers.GetSupportTickets)
		gogoo.POST("/support/tickets", handlers.CreateSupportTicket)
		gogoo.PATCH("/support/tickets/:id", handlers.UpdateSupportTicket)
		// Financial/account-altering staff actions — arbitrary-amount refunds,
		// cancelling any booking, blocking any rider — were reachable by any
		// authenticated rider/driver token with no panel check at all.
		gogoo.POST("/support/tickets/:id/refund", middleware.RequirePanel("support"), handlers.ProcessRefund)
		gogoo.GET("/support/tickets/:id/messages", middleware.RequirePanel("support"), handlers.GetTicketMessages)
		gogoo.POST("/support/tickets/:id/messages", handlers.SendTicketMessage)
		gogoo.GET("/support/stats", handlers.GetSupportStats)
		gogoo.POST("/support/cancel-booking/:id", middleware.RequirePanel("support"), handlers.SupportCancelBooking)
		gogoo.POST("/support/block-rider/:id", middleware.RequirePanel("support"), handlers.SupportBlockRider)

		// In-app chat (rider + driver apps)
		gogoo.GET("/support/faq", handlers.GetFAQ)
		gogoo.POST("/support/chat/start", handlers.StartSupportChat)
		gogoo.GET("/support/chat/my-tickets", handlers.GetMyTickets)
		gogoo.GET("/support/chat/:ticket_id/messages", handlers.GetChatMessages)
		gogoo.POST("/support/chat/:ticket_id/messages", handlers.SendChatMessage)
		gogoo.POST("/support/chat/:ticket_id/escalate", handlers.EscalateSupportChat)
		gogoo.GET("/support/unread-count", handlers.GetUnreadCount)

		// Lost item reporting (rider app) — reuses the ticket/chat system
		gogoo.POST("/support/lost-item/photo", handlers.UploadLostItemPhoto)
		gogoo.POST("/support/lost-item", handlers.ReportLostItem)

		// SOS emergency alert (riders + drivers share the same endpoint)
		gogoo.POST("/sos", handlers.TriggerSOS)
	}

	// ============================================================
	// GOGOO â€" EXCEL EXPORTS (token via header or ?token= query)
	// ============================================================
	// DownloadAuthMiddleware sets the same panel/role context keys as
	// AuthMiddleware, so RequirePanel composes on top. master_admin always
	// passes RequirePanel, so the dashboard's export buttons keep working.
	gogooExport := router.Group("/gogoo/export")
	gogooExport.Use(middleware.DownloadAuthMiddleware())
	{
		// Category panels manage their own drivers (cab panel has an export
		// button today); note the handler currently exports ALL categories.
		gogooExport.GET("/drivers.xlsx", middleware.RequirePanel("cab", "truck", "ambulance"), handlers.ExportDriversXLSX)
		// Full rider list with emails+phones: master admin + support staff.
		gogooExport.GET("/users.xlsx", middleware.RequirePanel("support"), handlers.ExportUsersXLSX)
		// No frontend calls this today; master admin only (empty allow-list
		// denies every panel, master_admin bypasses).
		gogooExport.GET("/referrals.xlsx", middleware.RequirePanel(), handlers.ExportReferralsXLSX)
	}

	return router
}
