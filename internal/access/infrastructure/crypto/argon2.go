// Package crypto implements password hashing with Argon2id (spec 8.2).
package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"

	"app-maker/internal/access/domain"
)

// Argon2id hashes passwords; parameters are stored with each hash.
type Argon2id struct {
	Time    uint32
	Memory  uint32 // KiB
	Threads uint8
	KeyLen  uint32
}

// DefaultArgon2id follows the RFC 9106 second recommended option.
var DefaultArgon2id = Argon2id{Time: 3, Memory: 64 * 1024, Threads: 4, KeyLen: 32}

var _ domain.PasswordHasher = Argon2id{}

// Hash returns "$argon2id$v=19$m=..,t=..,p=..$salt$hash".
func (a Argon2id) Hash(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, a.Time, a.Memory, a.Threads, a.KeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, a.Memory, a.Time, a.Threads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify checks a password against an encoded hash in constant time.
func (Argon2id) Verify(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, fmt.Errorf("unsupported hash format")
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, fmt.Errorf("bad hash parameters: %w", err)
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
