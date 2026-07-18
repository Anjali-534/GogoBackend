package handlers

// Bogie Tracker — transactional lifecycle emails (signup received, approved,
// rejected). Sent fire-and-forget via internal/mail (Resend), mirroring the
// sendPushNotifications pattern in notifications.go: each call spawns its own
// goroutine and never returns an error to the caller, because a company must
// never fail to sign up (or an admin action fail) just because Resend
// hiccuped. Suspension deliberately has no email — scope-out, admin may
// suspend for internal reasons.

import (
	"fmt"
	"log"

	"github.com/deploykit/backend/internal/config"
	"github.com/deploykit/backend/internal/mail"
)

func sendTrackerSignupEmail(cfg *config.Config, companyName, toEmail string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("tracker signup email: recovered from panic: %v", r)
			}
		}()
		if !mail.IsConfigured(cfg) {
			return
		}

		body := fmt.Sprintf(
			"Hi %s,\n\n"+
				"Thank you for signing up for Bogie Tracker!\n\n"+
				"We've received your registration and our team is now verifying your company details. This usually takes 1–2 business days.\n\n"+
				"You'll receive another email from us as soon as your account is approved and ready to use. Until then, no action is needed from your side.\n\n"+
				"If you have any questions in the meantime, just reply to this email or contact us at support@bogie.in.\n\n"+
				"Warm regards,\nTeam Bogie\nbogie.in",
			companyName,
		)

		if err := mail.Send(cfg, mail.Message{
			To:      toEmail,
			Subject: "Your Bogie Tracker account is under verification",
			Body:    body,
		}); err != nil {
			log.Printf("tracker signup email: send failed for %s: %v", toEmail, err)
		}
	}()
}

func sendTrackerOTPEmail(cfg *config.Config, companyName, toEmail, code string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("tracker OTP email: recovered from panic: %v", r)
			}
		}()
		if !mail.IsConfigured(cfg) {
			return
		}

		body := fmt.Sprintf(
			"Hi %s,\n\n"+
				"Your Bogie Tracker verification code is: %s\n\n"+
				"This code expires in 10 minutes. Enter it to verify your email and continue setting up your account.\n\n"+
				"If you didn't request this, you can safely ignore this email.\n\n"+
				"Warm regards,\nTeam Bogie\nbogie.in",
			companyName, code,
		)

		if err := mail.Send(cfg, mail.Message{
			To:      toEmail,
			Subject: "Your Bogie Tracker verification code",
			Body:    body,
		}); err != nil {
			log.Printf("tracker OTP email: send failed for %s: %v", toEmail, err)
		}
	}()
}

func sendTrackerApprovedEmail(cfg *config.Config, companyName, toEmail string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("tracker approved email: recovered from panic: %v", r)
			}
		}()
		if !mail.IsConfigured(cfg) {
			return
		}

		body := fmt.Sprintf(
			"Hi %s,\n\n"+
				"Great news — your Bogie Tracker account has been verified and activated!\n\n"+
				"You can now log in and start using Bogie Tracker:\n%s\n\n"+
				"Here's what you can do right away:\n"+
				"- Add your drivers and transporters\n"+
				"- Create dispatch orders for your shipments\n"+
				"- Track every order end-to-end\n"+
				"- Share live tracking links with your receiving parties — no login needed on their side\n\n"+
				"If you need any help getting started, reply to this email and we'll be happy to assist.\n\n"+
				"Welcome aboard!\nTeam Bogie\nbogie.in",
			companyName, cfg.TrackerPanelURL,
		)

		if err := mail.Send(cfg, mail.Message{
			To:      toEmail,
			Subject: "Your Bogie Tracker account is verified — you're ready to go!",
			Body:    body,
		}); err != nil {
			log.Printf("tracker approved email: send failed for %s: %v", toEmail, err)
		}
	}()
}

func sendTrackerRejectedEmail(cfg *config.Config, companyName, toEmail string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("tracker rejected email: recovered from panic: %v", r)
			}
		}()
		if !mail.IsConfigured(cfg) {
			return
		}

		body := fmt.Sprintf(
			"Hi %s,\n\n"+
				"Thank you for your interest in Bogie Tracker.\n\n"+
				"After reviewing your registration, we're unable to activate your account at this time.\n\n"+
				"If you believe this is an error, or if you'd like to provide additional details about your company, please reach out to us at support@bogie.in — we're happy to take another look.\n\n"+
				"Warm regards,\nTeam Bogie\nbogie.in",
			companyName,
		)

		if err := mail.Send(cfg, mail.Message{
			To:      toEmail,
			Subject: "Update on your Bogie Tracker registration",
			Body:    body,
		}); err != nil {
			log.Printf("tracker rejected email: send failed for %s: %v", toEmail, err)
		}
	}()
}
