package domain

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"time"
)

// BlobInfo describes stored bytes.
type BlobInfo struct {
	Size        int64
	ContentType string
}

// ErrBlobNotFound is returned for missing blobs.
var ErrBlobNotFound = errors.New("blob not found")

// BlobStore holds blob bytes (spec 14.4); adapters: local filesystem
// (development, single replica) and S3-compatible storage.
type BlobStore interface {
	// Put stores exactly size bytes from r; a different length is an error.
	Put(ctx context.Context, id string, r io.Reader, size int64, contentType string) error
	Get(ctx context.Context, id string) (io.ReadCloser, BlobInfo, error)
	Delete(ctx context.Context, id string) error
}

// BlobSigner signs short-lived blob URLs: op is "put" or "get".
type BlobSigner struct{ Key []byte }

// Sign returns the signature of op on id until exp.
func (s BlobSigner) Sign(op, id string, exp time.Time) string {
	m := hmac.New(sha256.New, s.Key)
	m.Write([]byte(op + "\x00" + id + "\x00" + strconv.FormatInt(exp.Unix(), 10)))
	return hex.EncodeToString(m.Sum(nil))
}

// Valid checks a signature and expiry.
func (s BlobSigner) Valid(op, id, exp, sig string, now time.Time) bool {
	n, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || now.Unix() > n {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(s.Sign(op, id, time.Unix(n, 0))))
}
