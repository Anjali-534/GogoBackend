package handlers

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// nextSerialNumber allocates the next gap-free serial in an invoice series:
// prefix + 5-digit zero-padded count, e.g. "INV-2026-00001" or
// "BGR/2627/00001". Shared by every invoice series (tracker plan invoices,
// booking invoices) so there is exactly one numbering mechanism.
//
// Must run inside tx: pg_advisory_xact_lock(lockKey) is held until tx ends,
// serializing concurrent allocators for the same series, and the number is
// derived from a COUNT of already-issued numbers rather than a sequence — so
// a rolled-back tx never burns a number (no gaps). countSQL must count rows
// whose number matches $1 (passed as prefix+"%"), and the caller must write
// the returned number in the same tx before committing.
func nextSerialNumber(ctx context.Context, tx pgx.Tx, lockKey, countSQL, prefix string) (string, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
		return "", err
	}

	var count int
	if err := tx.QueryRow(ctx, countSQL, prefix+"%").Scan(&count); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%05d", prefix, count+1), nil
}
