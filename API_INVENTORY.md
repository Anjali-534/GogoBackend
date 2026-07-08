# bogie Backend — API Inventory

Authoritative list of every route registered in [`internal/api/router.go`](internal/api/router.go).
`cmd/server/main.go` only calls `api.SetupRouter(cfg)` — no routes are registered anywhere else.

Legend:
- **PUBLIC** — no auth middleware, reachable by anyone.
- **AUTH-PROTECTED** — requires `middleware.AuthMiddleware()` (JWT bearer, riders/drivers/admin/panel users).
- **DOWNLOAD-AUTH-PROTECTED** — requires `middleware.DownloadAuthMiddleware()` (token via header or `?token=` query, used for browser-downloadable exports).

---

## Auth

| Method | Path | Handler | Access |
|---|---|---|---|
| POST | /auth/signup | handlers.Signup | PUBLIC |
| POST | /auth/login | handlers.Login | PUBLIC |
| POST | /auth/refresh | handlers.Refresh | PUBLIC |
| GET | /auth/github | handlers.GitHubAuthURL | PUBLIC |
| GET | /auth/github/callback | handlers.GitHubCallback | PUBLIC |
| GET | /auth/me | handlers.Me | AUTH-PROTECTED |
| POST | /auth/logout | handlers.Logout | AUTH-PROTECTED |

## Public Landing Pages

| Method | Path | Handler | Access |
|---|---|---|---|
| GET | /r/:code | handlers.ReferralLandingUser | PUBLIC |
| GET | /dr/:code | handlers.ReferralLandingDriver | PUBLIC |
| GET | /driver-app | handlers.DriverAppLanding | PUBLIC |

## Static Files (not gin route handlers — `router.Static`)

| Method | Path | Handler | Access |
|---|---|---|---|
| GET | /uploads/* | (static file server → ./uploads) | PUBLIC |
| GET | /policies/* | (static file server → ./static/policies) | PUBLIC |

## Services

| Method | Path | Handler | Access |
|---|---|---|---|
| GET | /gogoo/services | handlers.ListServiceTypes | PUBLIC |

## Bookings

| Method | Path | Handler | Access |
|---|---|---|---|
| POST | /gogoo/bookings | handlers.CreateBooking | AUTH-PROTECTED |
| GET | /gogoo/bookings | handlers.ListBookings | AUTH-PROTECTED |
| GET | /gogoo/bookings-pending | handlers.ListPendingBookings | AUTH-PROTECTED |
| GET | /gogoo/bookings/:id | handlers.GetBooking | AUTH-PROTECTED |
| POST | /gogoo/bookings/:id/rate | handlers.RateBooking | AUTH-PROTECTED |
| POST | /gogoo/bookings/:id/accept | handlers.AcceptBooking | AUTH-PROTECTED |
| POST | /gogoo/bookings/:id/verify-otp | handlers.VerifyRideOTP | AUTH-PROTECTED |
| PATCH | /gogoo/bookings/:id/status | handlers.UpdateBookingStatus | AUTH-PROTECTED |

## Drivers

| Method | Path | Handler | Access |
|---|---|---|---|
| POST | /gogoo/driver/signup | handlers.DriverSignup | PUBLIC |
| GET | /gogoo/drivers | handlers.ListDrivers | AUTH-PROTECTED |
| GET | /gogoo/drivers/:id | handlers.GetDriverByID | AUTH-PROTECTED |
| PATCH | /gogoo/drivers/:id/verify | handlers.VerifyDriver | AUTH-PROTECTED |
| PATCH | /gogoo/drivers/:id/online | handlers.ToggleDriverOnline | AUTH-PROTECTED |
| POST | /gogoo/drivers/:id/location | handlers.UpdateDriverLocation | AUTH-PROTECTED |
| GET | /gogoo/driver/profile | handlers.GetDriverProfile | AUTH-PROTECTED |
| GET | /gogoo/driver/active-booking | handlers.GetDriverActiveBooking | AUTH-PROTECTED |
| GET | /gogoo/driver/bookings | handlers.ListDriverBookings | AUTH-PROTECTED |
| GET | /gogoo/driver/reviews | handlers.GetDriverReviews | AUTH-PROTECTED |
| GET | /gogoo/drivers/:id/bookings | handlers.ListDriverBookingsByID | AUTH-PROTECTED |
| PATCH | /gogoo/drivers/:id/block | handlers.ManageDriverBlock | AUTH-PROTECTED |
| GET | /gogoo/drivers/:id/documents | handlers.GetDriverDocuments | AUTH-PROTECTED |
| POST | /gogoo/drivers/:id/documents | handlers.UploadDriverDocument | AUTH-PROTECTED |
| PATCH | /gogoo/drivers/:id/documents/:doc_type/review | handlers.ReviewDriverDocument | AUTH-PROTECTED |
| DELETE | /gogoo/drivers/:id/documents/:doc_type | handlers.DeleteDriverDocument | AUTH-PROTECTED |
| GET | /gogoo/driver/wallet | handlers.GetDriverWallet | AUTH-PROTECTED |
| GET | /gogoo/driver/ledger | handlers.GetDriverLedger | AUTH-PROTECTED |
| GET | /gogoo/driver/earnings/summary | handlers.GetEarningsSummary | AUTH-PROTECTED |
| GET | /gogoo/admin/driver-payments | handlers.AdminDriverPayments | AUTH-PROTECTED |
| GET | /gogoo/driver/notifications | handlers.ListDriverNotifications | AUTH-PROTECTED |
| GET | /gogoo/driver/notifications/unread-count | handlers.GetDriverNotificationUnreadCount | AUTH-PROTECTED |
| POST | /gogoo/driver/notifications/:id/read | handlers.MarkNotificationRead | AUTH-PROTECTED |
| GET | /gogoo/export/drivers.xlsx | handlers.ExportDriversXLSX | DOWNLOAD-AUTH-PROTECTED |

## Riders

| Method | Path | Handler | Access |
|---|---|---|---|
| POST | /gogoo/rider/signup | handlers.RiderSignup | PUBLIC |
| GET | /gogoo/riders | handlers.ListRiders | AUTH-PROTECTED |
| GET | /gogoo/rider/profile | handlers.GetRiderProfile | AUTH-PROTECTED |
| GET | /gogoo/rider/bookings | handlers.ListRiderBookings | AUTH-PROTECTED |
| GET | /gogoo/rider/saved-places | handlers.GetSavedPlaces | AUTH-PROTECTED |
| POST | /gogoo/rider/saved-places | handlers.SavePlace | AUTH-PROTECTED |
| DELETE | /gogoo/rider/saved-places/:label | handlers.DeleteSavedPlace | AUTH-PROTECTED |
| GET | /gogoo/riders/:id/bookings | handlers.ListRiderBookingsByID | AUTH-PROTECTED |
| GET | /gogoo/notifications | handlers.ListNotifications | AUTH-PROTECTED |
| GET | /gogoo/notifications/unread-count | handlers.GetNotificationUnreadCount | AUTH-PROTECTED |
| POST | /gogoo/notifications/:id/read | handlers.MarkNotificationRead | AUTH-PROTECTED |
| GET | /gogoo/export/users.xlsx | handlers.ExportUsersXLSX | DOWNLOAD-AUTH-PROTECTED |

## Ambulance

| Method | Path | Handler | Access |
|---|---|---|---|
| POST | /gogoo/hospital-login | handlers.HospitalLogin | PUBLIC |
| GET | /gogoo/ambulance/hospitals/nearby | handlers.GetNearbyHospitals | PUBLIC |
| GET | /gogoo/ambulance/ngos | handlers.GetNGOs | AUTH-PROTECTED |
| POST | /gogoo/ambulance/ngos | handlers.CreateNGO | AUTH-PROTECTED |
| PATCH | /gogoo/ambulance/ngos/:id | handlers.UpdateNGO | AUTH-PROTECTED |
| DELETE | /gogoo/ambulance/ngos/:id | handlers.DeleteNGO | AUTH-PROTECTED |
| GET | /gogoo/ambulance/hospitals | handlers.GetHospitals | AUTH-PROTECTED |
| GET | /gogoo/ambulance/hospitals/:id | handlers.GetHospitalByID | AUTH-PROTECTED |
| POST | /gogoo/ambulance/hospitals | handlers.CreateHospital | AUTH-PROTECTED |
| PATCH | /gogoo/ambulance/hospitals/:id | handlers.UpdateHospital | AUTH-PROTECTED |
| DELETE | /gogoo/ambulance/hospitals/:id | handlers.DeleteHospital | AUTH-PROTECTED |
| PATCH | /gogoo/ambulance/hospitals/:id/password | handlers.ResetHospitalPassword | AUTH-PROTECTED |
| GET | /gogoo/ambulance/bookings/hospital | handlers.GetHospitalBookings | AUTH-PROTECTED |
| POST | /gogoo/ambulance/bookings/hospital | handlers.CreateHospitalBooking | AUTH-PROTECTED |
| PATCH | /gogoo/ambulance/bookings/hospital/:id/status | handlers.UpdateHospitalBookingStatus | AUTH-PROTECTED |
| GET | /gogoo/ambulance/all-bookings | handlers.GetAmbulanceAllBookings | AUTH-PROTECTED |

## Support

| Method | Path | Handler | Access |
|---|---|---|---|
| GET | /gogoo/support/tickets | handlers.GetSupportTickets | AUTH-PROTECTED |
| POST | /gogoo/support/tickets | handlers.CreateSupportTicket | AUTH-PROTECTED |
| PATCH | /gogoo/support/tickets/:id | handlers.UpdateSupportTicket | AUTH-PROTECTED |
| POST | /gogoo/support/tickets/:id/refund | handlers.ProcessRefund | AUTH-PROTECTED |
| GET | /gogoo/support/tickets/:id/messages | handlers.GetTicketMessages | AUTH-PROTECTED |
| POST | /gogoo/support/tickets/:id/messages | handlers.SendTicketMessage | AUTH-PROTECTED |
| GET | /gogoo/support/stats | handlers.GetSupportStats | AUTH-PROTECTED |
| POST | /gogoo/support/cancel-booking/:id | handlers.SupportCancelBooking | AUTH-PROTECTED |
| POST | /gogoo/support/block-rider/:id | handlers.SupportBlockRider | AUTH-PROTECTED |

## Support Chat

| Method | Path | Handler | Access |
|---|---|---|---|
| POST | /gogoo/support/chat/start | handlers.StartSupportChat | AUTH-PROTECTED |
| GET | /gogoo/support/chat/my-tickets | handlers.GetMyTickets | AUTH-PROTECTED |
| GET | /gogoo/support/chat/:ticket_id/messages | handlers.GetChatMessages | AUTH-PROTECTED |
| POST | /gogoo/support/chat/:ticket_id/messages | handlers.SendChatMessage | AUTH-PROTECTED |
| GET | /gogoo/support/unread-count | handlers.GetUnreadCount | AUTH-PROTECTED |

## SOS

| Method | Path | Handler | Access |
|---|---|---|---|
| POST | /gogoo/sos | handlers.TriggerSOS | AUTH-PROTECTED |

## Referrals

| Method | Path | Handler | Access |
|---|---|---|---|
| POST | /gogoo/referral/validate | handlers.ValidateReferralCode | PUBLIC |
| GET | /gogoo/referral/my-code | handlers.GetMyReferralCode | AUTH-PROTECTED |
| GET | /gogoo/referral/my-referrals | handlers.GetMyReferrals | AUTH-PROTECTED |
| GET | /gogoo/referral/all | handlers.AdminListReferrals | AUTH-PROTECTED |
| GET | /gogoo/export/referrals.xlsx | handlers.ExportReferralsXLSX | DOWNLOAD-AUTH-PROTECTED |

## Live Ops

| Method | Path | Handler | Access |
|---|---|---|---|
| GET | /gogoo/live/drivers | handlers.ListLiveDrivers | AUTH-PROTECTED |
| GET | /gogoo/live/bookings | handlers.ListLiveBookings | AUTH-PROTECTED |
| GET | /gogoo/route | handlers.ProxyOlaRoute | AUTH-PROTECTED |
| GET | /gogoo/geocode/reverse | handlers.ReverseGeocodeProxy | AUTH-PROTECTED |

## Analytics

| Method | Path | Handler | Access |
|---|---|---|---|
| GET | /gogoo/analytics | handlers.GetAnalytics | AUTH-PROTECTED |
| POST | /gogoo/analytics/event | handlers.RecordAnalyticsEvent | AUTH-PROTECTED |
| GET | /gogoo/analytics/screen-times | handlers.GetScreenTimes | AUTH-PROTECTED |
| GET | /gogoo/analytics/geo-distribution | handlers.GetGeoDistribution | AUTH-PROTECTED |
| GET | /gogoo/analytics/device-breakdown | handlers.GetDeviceBreakdown | AUTH-PROTECTED |
| GET | /gogoo/analytics/retention | handlers.GetRetentionStats | AUTH-PROTECTED |
| GET | /gogoo/analytics/sessions | handlers.GetSessionStats | AUTH-PROTECTED |
| GET | /gogoo/analytics/usage-heatmap | handlers.GetUsageHeatmap | AUTH-PROTECTED |
| GET | /gogoo/analytics/funnel | handlers.GetFunnelData | AUTH-PROTECTED |

## Misc

| Method | Path | Handler | Access |
|---|---|---|---|
| GET | /health | handlers.Health | PUBLIC |
| GET | /ready | handlers.Ready | PUBLIC |
| POST | /gogoo/panel-login | handlers.PanelLogin | PUBLIC |
| POST | /gogoo/push-token | handlers.RegisterPushToken | AUTH-PROTECTED |
| POST | /gogoo/admin/notifications | handlers.CreateNotification | AUTH-PROTECTED |
| GET | /gogoo/admin/notifications | handlers.AdminListNotifications | AUTH-PROTECTED |
| DELETE | /gogoo/admin/notifications/:id | handlers.DeleteNotification | AUTH-PROTECTED |
| GET | /gogoo/admin/panel-access | handlers.GetPanelAccess | AUTH-PROTECTED |
| PATCH | /gogoo/admin/panel-access/:id/password | handlers.UpdatePanelPassword | AUTH-PROTECTED |
| GET | /gogoo/payments | handlers.ListPayments | AUTH-PROTECTED |
