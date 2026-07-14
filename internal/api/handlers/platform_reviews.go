package handlers

import (
    "context"
    "net/http"
    "strings"
    "time"

    "github.com/deploykit/backend/internal/db"
    "github.com/gin-gonic/gin"
    "github.com/google/uuid"
)

// firstNameOnly returns just the first word of a full name, for public
// display where we never want to expose a full name/phone/email.
func firstNameOnly(name string) string {
    name = strings.TrimSpace(name)
    if name == "" {
        return "Bogie user"
    }
    return strings.Fields(name)[0]
}

// SubmitPlatformReview creates or (if the caller already has one) updates
// their review of the Bogie platform itself — distinct from per-ride
// driver/rider ratings on bookings. One review per user; resubmitting edits
// it in place. Works for both rider and driver tokens.
// POST /gogoo/reviews/platform
func SubmitPlatformReview(c *gin.Context) {
    userID := c.GetString("user_id")
    ctx := context.Background()
    pool := db.GetDB().GetPool()

    userType, _, _, ok := resolveReferralActor(ctx, userID)
    if !ok {
        c.JSON(http.StatusForbidden, gin.H{"error": "no rider or driver account found for this user"})
        return
    }

    var req struct {
        Rating     int    `json:"rating"`
        ReviewText string `json:"review_text"`
    }
    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
        return
    }
    if req.Rating < 1 || req.Rating > 5 {
        c.JSON(http.StatusBadRequest, gin.H{"error": "rating must be between 1 and 5"})
        return
    }
    reviewText := strings.TrimSpace(req.ReviewText)
    if reviewText == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "review_text is required"})
        return
    }
    if len(reviewText) > 500 {
        c.JSON(http.StatusBadRequest, gin.H{"error": "review_text must be 500 characters or fewer"})
        return
    }

    var name string
    pool.QueryRow(ctx, "SELECT name FROM users WHERE id=$1", userID).Scan(&name)
    displayName := firstNameOnly(name)

    _, err := pool.Exec(ctx, `
        INSERT INTO platform_reviews (id, user_id, user_type, display_name, rating, review_text)
        VALUES ($1, $2, $3, $4, $5, $6)
        ON CONFLICT (user_id) DO UPDATE SET
            display_name = EXCLUDED.display_name,
            rating        = EXCLUDED.rating,
            review_text   = EXCLUDED.review_text,
            updated_at    = NOW()
    `, uuid.New(), userID, userType, displayName, req.Rating, reviewText)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save review"})
        return
    }

    c.JSON(http.StatusOK, gin.H{"message": "Review saved"})
}

// GetPlatformReviewsPublic returns the most recent published platform
// reviews for the public marketing site. PUBLIC (no auth): display name is
// first-name-only, never full name/phone/email. No moderation filter beyond
// requiring non-empty review text.
// GET /gogoo/reviews/platform/public
func GetPlatformReviewsPublic(c *gin.Context) {
    ctx := context.Background()
    pool := db.GetDB().GetPool()

    rows, err := pool.Query(ctx, `
        SELECT display_name, user_type, rating, review_text, created_at
        FROM platform_reviews
        WHERE trim(review_text) <> ''
        ORDER BY created_at DESC
        LIMIT 30
    `)
    if err != nil {
        c.JSON(http.StatusOK, gin.H{"reviews": []gin.H{}})
        return
    }
    defer rows.Close()

    reviews := []gin.H{}
    for rows.Next() {
        var displayName, userType, reviewText string
        var rating int
        var createdAt time.Time
        if err := rows.Scan(&displayName, &userType, &rating, &reviewText, &createdAt); err != nil {
            continue
        }
        reviews = append(reviews, gin.H{
            "display_name": displayName,
            "user_type":    userType,
            "rating":       rating,
            "review_text":  reviewText,
            "created_at":   createdAt,
        })
    }

    c.JSON(http.StatusOK, gin.H{"reviews": reviews})
}
