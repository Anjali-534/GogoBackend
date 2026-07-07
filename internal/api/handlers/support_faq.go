package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// FAQItem is one predefined support question with a fixed, instant answer —
// no AI call, no network round-trip, zero marginal cost per use. Add new
// items freely; nothing else needs to change to support a new entry.
type FAQItem struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
	Category string `json:"category"` // "ride" | "payment" | "driver" | "general"
}

var FAQItems = []FAQItem{
	{
		ID:       "extra_charges",
		Question: "I was charged extra than shown",
		Answer:   "Fare can vary from the estimate due to actual distance/time taken, tolls, or waiting charges. Check your ride receipt in History for the full breakdown. If you still believe this is wrong, tap 'Still need help' below.",
		Category: "payment",
	},
	{
		ID:       "driver_not_reached",
		Question: "My driver hasn't reached yet",
		Answer:   "You can track your driver live on the tracking screen and call them directly using the call button. If they've been delayed more than 10 minutes beyond the estimate, you can cancel free of charge.",
		Category: "ride",
	},
	{
		ID:       "taking_too_long",
		Question: "My ride is taking too long",
		Answer:   "Traffic and route conditions can affect trip duration. You can view live progress on the tracking screen. If something feels wrong (driver going off-route), use the SOS button immediately.",
		Category: "ride",
	},
	{
		ID:       "cancel_ride",
		Question: "I want to cancel my ride",
		Answer:   "Open the ride from Home or History and tap Cancel. Cancellation may include a small fee if the driver has already accepted and some time has passed — this will be shown before you confirm.",
		Category: "ride",
	},
	{
		ID:       "refund_status",
		Question: "Where is my refund?",
		Answer:   "Refunds are typically processed within 3-5 business days to your original payment method. If it's been longer, tap 'Still need help' and our team will check your specific case.",
		Category: "payment",
	},
	{
		ID:       "driver_behavior",
		Question: "My driver was rude or unsafe",
		Answer:   "We take this seriously. Please share more details by tapping 'Still need help' below and our team will investigate and take appropriate action.",
		Category: "driver",
	},
	{
		ID:       "lost_item",
		Question: "I lost an item during my ride",
		Answer:   "Go to Help & Support > Lost Items to report what you lost and we'll try to connect you with your driver.",
		Category: "ride",
	},
	{
		ID:       "wrong_fare_shown",
		Question: "The fare estimate seems wrong",
		Answer:   "Fare estimates are based on distance, vehicle type, and current demand. Actual fare may vary slightly. For ambulance bookings, remember gogoo charges zero commission.",
		Category: "payment",
	},
	{
		ID:       "app_issue",
		Question: "App is not working properly",
		Answer:   "Try force-closing and reopening the app. Make sure you're on the latest version. If the issue persists, tap 'Still need help' and describe what's happening.",
		Category: "general",
	},
	{
		ID:       "account_issue",
		Question: "I can't log in / account issue",
		Answer:   "Make sure you're using the correct phone number and OTP. If you're still stuck, tap 'Still need help' below.",
		Category: "general",
	},
}

var faqByID = func() map[string]FAQItem {
	m := make(map[string]FAQItem, len(FAQItems))
	for _, item := range FAQItems {
		m[item.ID] = item
	}
	return m
}()

// GET /gogoo/support/faq
func GetFAQ(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"items": FAQItems})
}
