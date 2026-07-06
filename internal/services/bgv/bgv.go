// Package bgv defines the seam for automated driver background-verification
// providers (IDfy, Signzy, AuthBridge, etc.) required under MVAG 2020.
//
// Nothing in this package calls an external API yet — VerificationService
// only wires against StubProvider, which returns ErrNotImplemented for every
// check. Reviewers must keep using the manual workflow (the
// background_check_status column on drivers, driven by the
// PATCH /gogoo/drivers/:id/background-check panel endpoint) until a real
// Provider is implemented and swapped in.
//
// To integrate a real provider later: implement Provider against a vendor
// SDK/HTTP client, then construct VerificationService with it instead of
// StubProvider — no other call site needs to change.
package bgv

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned by every StubProvider method. Callers should
// treat it as "no automated result available — fall back to manual review",
// not as a hard failure.
var ErrNotImplemented = errors.New("bgv: automated check not implemented — use manual review")

// CheckResult is the normalized shape every provider (IDfy, Signzy,
// AuthBridge, ...) is expected to map its response onto, so swapping
// providers never touches call sites.
type CheckResult struct {
	Status   string // "clear" | "flagged" | "in_review" — mirrors drivers.background_check_status
	Notes    string
	Provider string // e.g. "idfy", "signzy" — empty for the stub
	RawRef   string // provider-side reference/request ID, for audit trails
}

// Provider is the interface every background-verification vendor integration
// must satisfy. Each method corresponds to one MVAG-relevant check.
type Provider interface {
	// VerifyDL checks a driving license number against the issuing RTO
	// records (e.g. via parivahan.gov.in / Sarathi in India).
	VerifyDL(ctx context.Context, licenseNumber string) (CheckResult, error)

	// VerifyRC checks a vehicle registration certificate number against the
	// Vahan database.
	VerifyRC(ctx context.Context, rcNumber string) (CheckResult, error)

	// CourtRecordCheck screens a driver (by name + DOB/ID proof) against
	// court/criminal records — the automated equivalent of manually
	// confirming a Police Clearance Certificate.
	CourtRecordCheck(ctx context.Context, fullName, idNumber string) (CheckResult, error)
}

// StubProvider is the default, always-on Provider. It performs no network
// calls and always returns ErrNotImplemented — a deliberate no-op so nothing
// silently "passes" a background check before a real provider is wired in.
type StubProvider struct{}

func (StubProvider) VerifyDL(ctx context.Context, licenseNumber string) (CheckResult, error) {
	return CheckResult{}, ErrNotImplemented
}

func (StubProvider) VerifyRC(ctx context.Context, rcNumber string) (CheckResult, error) {
	return CheckResult{}, ErrNotImplemented
}

func (StubProvider) CourtRecordCheck(ctx context.Context, fullName, idNumber string) (CheckResult, error) {
	return CheckResult{}, ErrNotImplemented
}

// VerificationService is the entry point handlers would call once automated
// checks are wired in. It currently only holds a StubProvider, so every call
// resolves to ErrNotImplemented — callers must keep the manual review path
// (PATCH /gogoo/drivers/:id/background-check) as the source of truth.
type VerificationService struct {
	provider Provider
}

// NewVerificationService constructs the seam. Swap StubProvider for a real
// Provider implementation here once one exists — no other code needs to
// change.
func NewVerificationService() *VerificationService {
	return &VerificationService{provider: StubProvider{}}
}

func (s *VerificationService) VerifyDL(ctx context.Context, licenseNumber string) (CheckResult, error) {
	return s.provider.VerifyDL(ctx, licenseNumber)
}

func (s *VerificationService) VerifyRC(ctx context.Context, rcNumber string) (CheckResult, error) {
	return s.provider.VerifyRC(ctx, rcNumber)
}

func (s *VerificationService) CourtRecordCheck(ctx context.Context, fullName, idNumber string) (CheckResult, error) {
	return s.provider.CourtRecordCheck(ctx, fullName, idNumber)
}
