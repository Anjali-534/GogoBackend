-- Migration 049 — Bogie Tracker: driver+consignee-driven delivery
-- completion, replacing the company's manual "Confirm Delivered" action.
--
-- delivery_condition / delivery_condition_reason: set together with
-- received_confirmed_at by ConfirmTrackerReceipt (either the "good
-- condition" or "bad condition" button on the public receipt page).
-- received_confirmed_at now means "consignee responded" for EITHER
-- button, not just good — reusing the existing column rather than
-- adding a new one, since nothing downstream assumed it meant "good".
--
-- needs_staff_attention: set by the daily reminder job (see
-- trackerdelivery package, mirroring trackersub's reminder job) once 7
-- reminders have been sent with no consignee response. Purely
-- informational for the panel — never blocks anything.
--
-- tracker_delivery_reminders_sent: dedup table for the 24h-cycle
-- reminder emails, same idiom as tracker_subscription_reminders_sent
-- (migration 036) — keyed on (order_id, reminder_number) so each of
-- the up-to-7 daily reminders sends at most once.

ALTER TABLE tracker_orders
  ADD COLUMN IF NOT EXISTS delivery_condition        TEXT
    CHECK (delivery_condition IN ('good', 'bad')),
  ADD COLUMN IF NOT EXISTS delivery_condition_reason TEXT,
  ADD COLUMN IF NOT EXISTS needs_staff_attention     BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS tracker_delivery_reminders_sent (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id        UUID NOT NULL REFERENCES tracker_orders(id) ON DELETE CASCADE,
  reminder_number INT  NOT NULL CHECK (reminder_number BETWEEN 1 AND 7),
  sent_at         TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE (order_id, reminder_number)
);

CREATE INDEX IF NOT EXISTS idx_tracker_delivery_reminders_sent_order_id
  ON tracker_delivery_reminders_sent(order_id);
