-- Migration 044 — Bogie Tracker: multi-document restructure (Phase 2).
--
-- Replaces the single fixed eway_bill_file_url column with a variable-length
-- document set per order: COA, Invoice, LR, E-way Bill, Other (with a
-- custom_label when doc_type='other'). Per the confirmed product decision,
-- ALL documents stay optional forever — no declared-value threshold or any
-- other mandatory-document enforcement, client or server side.
--
-- Existing eway_bill_file_url data is backfilled below as a doc_type=
-- 'eway_bill' row so nothing is lost. eway_bill_file_url itself is left in
-- place (deprecated, not dropped) — the column still has admin-dashboard
-- readers (tracker_admin.go) that aren't part of this phase's scope, and a
-- read-only historical column costs nothing to keep around. The upload
-- endpoint that writes it (POST /tracker/orders/:id/eway-bill) stops being
-- called from the frontend as of this phase; nothing else writes to it
-- going forward. eway_bill_number is a separate plain-text field (the
-- e-way bill's number, not its file) and is NOT part of this restructure —
-- it stays a live, editable column.

CREATE TABLE IF NOT EXISTS tracker_order_documents (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id      UUID NOT NULL REFERENCES tracker_orders(id) ON DELETE CASCADE,
  doc_type      TEXT NOT NULL CHECK (doc_type IN ('coa','invoice','lr','eway_bill','other')),
  -- Only meaningful (and only ever set) when doc_type='other' — the
  -- company's own label for what the document is.
  custom_label  TEXT,
  file_url      TEXT NOT NULL,
  -- e.g. e-way bill validity date. Optional for every doc_type, not just
  -- eway_bill — a company may want to track an invoice or LR's own expiry.
  expiry_date   DATE,
  created_at    TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tracker_order_documents_order_id
  ON tracker_order_documents(order_id);

-- Backfill: every existing order with an uploaded e-way bill file gets a
-- matching row in the new table, preserving the original upload timestamp.
INSERT INTO tracker_order_documents (order_id, doc_type, file_url, created_at)
SELECT id, 'eway_bill', eway_bill_file_url, created_at
FROM tracker_orders
WHERE eway_bill_file_url IS NOT NULL AND eway_bill_file_url <> '';
