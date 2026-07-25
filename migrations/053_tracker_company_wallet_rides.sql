-- Migration 053 — Bogie Tracker: let a tracker company pay for its own
-- Book-a-Ride bookings (cab/truck/ambulance) out of its own wallet
-- (tracker_companies.wallet_balance, migration 052), instead of the
-- rider-table 'wallet' value which only ever reaches the synthetic rider
-- row's own (always-empty) balance.
--
-- bookings.payment_method gains 'company_wallet' alongside the existing
-- 'cash'/'wallet' — set by createBookingCore when the booking's rider_id
-- resolves to a tracker company's synthetic_rider_id, debited at
-- completion by debitCompanyWalletForRide (mirrors debitWalletForRide,
-- but against tracker_companies.wallet_balance + tracker_wallet_ledger).
--
-- tracker_wallet_ledger.type gains 'ride_payment' alongside the existing
-- 'topup'/'subscription_charge'/'adjustment' — the ledger row type written
-- by that same debit.

ALTER TABLE bookings
    DROP CONSTRAINT IF EXISTS bookings_payment_method_check;

ALTER TABLE bookings
    ADD CONSTRAINT bookings_payment_method_check
    CHECK (payment_method IN ('cash', 'wallet', 'company_wallet'));

ALTER TABLE tracker_wallet_ledger
    DROP CONSTRAINT IF EXISTS tracker_wallet_ledger_type_check;

ALTER TABLE tracker_wallet_ledger
    ADD CONSTRAINT tracker_wallet_ledger_type_check
    CHECK (type IN ('topup', 'subscription_charge', 'adjustment', 'ride_payment'));
