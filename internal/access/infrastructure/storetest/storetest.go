// Package storetest is the CredentialStore conformance suite (spec 15.7)
// for identities, TOTP secrets, and one-time tickets.
package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"organon/internal/access/domain"
	"organon/internal/kernel"
)

// Run checks a CredentialStore.
func Run(t *testing.T, s domain.CredentialStore) {
	ctx := context.Background()
	a, b := kernel.MustParseID("01a19463-0000-7000-8000-000000000001"), kernel.MustParseID("01a19463-0000-7000-8000-000000000002")
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	t.Run("identities", func(t *testing.T) {
		if _, err := s.Identity(ctx, "google", "s1"); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("missing identity: %v", err)
		}
		if err := s.PutIdentity(ctx, domain.Identity{Provider: "google", Subject: "s1", PrincipalID: a, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := s.PutIdentity(ctx, domain.Identity{Provider: "google", Subject: "s1", PrincipalID: a, CreatedAt: now}); err != nil {
			t.Fatalf("relinking the same principal: %v", err)
		}
		var ce *kernel.ConstraintError
		if err := s.PutIdentity(ctx, domain.Identity{Provider: "google", Subject: "s1", PrincipalID: b, CreatedAt: now}); !errors.As(err, &ce) {
			t.Fatalf("identity stolen by another principal: %v", err)
		}
		i, err := s.Identity(ctx, "google", "s1")
		if err != nil || i.PrincipalID != a {
			t.Fatalf("identity %+v %v", i, err)
		}
		if _, err := s.Identity(ctx, "github", "s1"); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal("providers are separate")
		}
	})
	t.Run("totp", func(t *testing.T) {
		if err := s.PutTOTP(ctx, domain.TOTP{PrincipalID: a, Secret: []byte{1, 2, 3}}); err != nil {
			t.Fatal(err)
		}
		if err := s.PutTOTP(ctx, domain.TOTP{PrincipalID: a, Secret: []byte{4}, Confirmed: true, LastStep: 7}); err != nil {
			t.Fatal(err)
		}
		got, err := s.TOTP(ctx, a)
		if err != nil || !got.Confirmed || got.LastStep != 7 || len(got.Secret) != 1 || got.Secret[0] != 4 {
			t.Fatalf("totp %+v %v", got, err)
		}
		if err := s.DeleteTOTP(ctx, a); err != nil {
			t.Fatal(err)
		}
		if _, err := s.TOTP(ctx, a); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("deleted totp: %v", err)
		}
	})
	t.Run("tickets", func(t *testing.T) {
		tk := domain.Ticket{IDHash: "h1", PrincipalID: b, Purpose: domain.TicketMFAVerify, ExpiresAt: now}
		if err := s.CreateTicket(ctx, tk); err != nil {
			t.Fatal(err)
		}
		got, err := s.TakeTicket(ctx, "h1")
		if err != nil || got.PrincipalID != b || got.Purpose != domain.TicketMFAVerify || !got.ExpiresAt.Equal(now) {
			t.Fatalf("ticket %+v %v", got, err)
		}
		if _, err := s.TakeTicket(ctx, "h1"); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("ticket used twice: %v", err)
		}
	})
}
