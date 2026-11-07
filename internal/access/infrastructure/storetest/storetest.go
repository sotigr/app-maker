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

	// ADR 0027: the admin console lists and revokes keys and reads which
	// sign-in methods a principal has.
	t.Run("admin lookups", func(t *testing.T) {
		c, d := kernel.MustParseID("01a19463-0000-7000-8000-000000000003"), kernel.MustParseID("01a19463-0000-7000-8000-000000000004")
		for i, p := range []string{"k2", "k1"} {
			if err := s.PutAPIKey(ctx, domain.APIKey{Prefix: p, Hash: "h-" + p, PrincipalID: c, Name: "n" + p, CreatedAt: now.Add(time.Duration(i) * time.Minute)}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.PutAPIKey(ctx, domain.APIKey{Prefix: "k9", Hash: "h-k9", PrincipalID: d, Name: "other", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		keys, err := s.APIKeysOf(ctx, c)
		if err != nil || len(keys) != 2 || keys[0].Prefix != "k2" || keys[1].Name != "nk1" {
			t.Fatalf("keys: %+v %v", keys, err)
		}
		if err := s.DeleteAPIKey(ctx, c, "k9"); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("another principal's key: %v", err)
		}
		if err := s.DeleteAPIKey(ctx, c, "k2"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.APIKey(ctx, "h-k2"); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal("revoked key still found")
		}
		if keys, _ := s.APIKeysOf(ctx, c); len(keys) != 1 {
			t.Fatalf("after revoke: %+v", keys)
		}
		if _, err := s.PasswordOf(ctx, c); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("no password: %v", err)
		}
		if err := s.PutPassword(ctx, domain.PasswordCredential{PrincipalID: c, Login: "c@example.com", Hash: "x"}); err != nil {
			t.Fatal(err)
		}
		if pw, err := s.PasswordOf(ctx, c); err != nil || pw.Login != "c@example.com" {
			t.Fatalf("password of: %+v %v", pw, err)
		}
		if err := s.PutIdentity(ctx, domain.Identity{Provider: "gitlab", Subject: "c1", PrincipalID: c, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		ids, err := s.IdentitiesOf(ctx, c)
		if err != nil || len(ids) != 1 || ids[0].Provider != "gitlab" {
			t.Fatalf("identities: %+v %v", ids, err)
		}
		if ids, _ := s.IdentitiesOf(ctx, d); len(ids) != 0 {
			t.Fatalf("identities of another principal: %+v", ids)
		}
	})
}
