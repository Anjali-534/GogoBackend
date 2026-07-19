-- Migration 040 — Bogie Tracker: auto-disable staff seats on plan downgrade.
--
-- A company can end up with more tracker_staff_users rows than its new plan's
-- PanelLoginStaffCap allows (e.g. downgrading 5users -> 2users after the
-- previous plan expired). Rather than deleting the excess rows outright, they
-- are marked disabled_at so the owner can see who was auto-disabled and
-- either remove them or wait to upgrade again (manual reactivation only —
-- see MarkTrackerPlanOrderPaid's disableExcessTrackerStaff).

ALTER TABLE tracker_staff_users ADD COLUMN IF NOT EXISTS disabled_at TIMESTAMPTZ;
