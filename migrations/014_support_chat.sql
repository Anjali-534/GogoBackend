-- Migration 014 — In-App Support Chat + Claude AI auto-reply

-- Ensure support_tickets table exists with in_app_chat type
CREATE TABLE IF NOT EXISTS support_tickets (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_number       TEXT UNIQUE DEFAULT 'TKT-' || upper(substr(gen_random_uuid()::text, 1, 8)),
    type                TEXT NOT NULL DEFAULT 'other',
    status              TEXT NOT NULL DEFAULT 'open'
                            CHECK (status IN ('open', 'in_progress', 'resolved', 'closed')),
    priority            TEXT NOT NULL DEFAULT 'medium'
                            CHECK (priority IN ('urgent', 'high', 'medium', 'low')),
    subject             TEXT NOT NULL,
    description         TEXT,
    raised_by           TEXT NOT NULL CHECK (raised_by IN ('rider', 'driver')),
    rider_id            UUID REFERENCES riders(id),
    driver_id           UUID REFERENCES drivers(id),
    booking_id          UUID,
    assigned_to         TEXT,
    refund_requested    BOOLEAN DEFAULT FALSE,
    refund_amount       DECIMAL(10, 2),
    refund_status       TEXT,
    refund_processed_at TIMESTAMPTZ,
    refund_processed_by TEXT,
    resolution          TEXT,
    resolved_at         TIMESTAMPTZ,
    resolved_by         TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Ensure support_messages table exists with bot sender_type support
CREATE TABLE IF NOT EXISTS support_messages (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_id   UUID NOT NULL REFERENCES support_tickets(id) ON DELETE CASCADE,
    sender_type TEXT NOT NULL,
    sender_id   TEXT,
    sender_name TEXT,
    message     TEXT NOT NULL,
    is_read     BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Add in_app_chat to type constraint if constraint exists (safe update)
DO $$
BEGIN
    -- Drop old type constraint if it exists and is too restrictive
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'support_tickets_type_check'
        AND conrelid = 'support_tickets'::regclass
    ) THEN
        ALTER TABLE support_tickets DROP CONSTRAINT support_tickets_type_check;
    END IF;
EXCEPTION WHEN OTHERS THEN
    NULL;
END $$;

ALTER TABLE support_tickets
    ADD CONSTRAINT support_tickets_type_check
    CHECK (type IN (
        'payment_issue', 'booking_issue', 'rider_complaint',
        'driver_complaint', 'refund_request', 'other', 'in_app_chat'
    ));

-- Indexes for chat queries
CREATE INDEX IF NOT EXISTS idx_support_tickets_rider_id  ON support_tickets(rider_id);
CREATE INDEX IF NOT EXISTS idx_support_tickets_driver_id ON support_tickets(driver_id);
CREATE INDEX IF NOT EXISTS idx_support_tickets_type      ON support_tickets(type);
CREATE INDEX IF NOT EXISTS idx_support_messages_ticket   ON support_messages(ticket_id);
CREATE INDEX IF NOT EXISTS idx_support_messages_unread   ON support_messages(ticket_id, is_read, sender_type);
