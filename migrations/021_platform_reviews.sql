-- Migration 021 — Platform Reviews (reviews of Bogie the platform, distinct
-- from per-ride driver/rider ratings on the bookings table). One review per
-- user, auto-published on submit (no moderation queue).

CREATE TABLE IF NOT EXISTS platform_reviews (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL UNIQUE,
    user_type    TEXT NOT NULL CHECK (user_type IN ('rider', 'driver')),
    display_name TEXT NOT NULL,
    rating       INT NOT NULL CHECK (rating BETWEEN 1 AND 5),
    review_text  TEXT NOT NULL CHECK (char_length(review_text) <= 500),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_platform_reviews_created_at ON platform_reviews(created_at DESC);
