package infrastructure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"organon/internal/integration/domain"
)

// BlobFS stores blobs as files under Dir (spec 14.4, development and
// single-replica deployments): <id> holds the bytes, <id>.json the info.
type BlobFS struct{ Dir string }

var _ domain.BlobStore = BlobFS{}

var blobID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func (b BlobFS) path(id string) (string, error) {
	if !blobID.MatchString(id) {
		return "", fmt.Errorf("invalid blob id %q", id)
	}
	return filepath.Join(b.Dir, id[:2], id), nil
}

// Put implements domain.BlobStore; bytes are written to a temporary file
// and renamed into place only when complete.
func (b BlobFS) Put(_ context.Context, id string, r io.Reader, size int64, contentType string) error {
	p, err := b.path(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".upload-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	n, err := io.Copy(tmp, io.LimitReader(r, size+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("received %d bytes, expected %d", n, size)
	}
	info, _ := json.Marshal(domain.BlobInfo{Size: size, ContentType: contentType})
	if err := os.WriteFile(p+".json", info, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// Get implements domain.BlobStore.
func (b BlobFS) Get(_ context.Context, id string) (io.ReadCloser, domain.BlobInfo, error) {
	p, err := b.path(id)
	if err != nil {
		return nil, domain.BlobInfo{}, domain.ErrBlobNotFound
	}
	raw, err := os.ReadFile(p + ".json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, domain.BlobInfo{}, domain.ErrBlobNotFound
	}
	if err != nil {
		return nil, domain.BlobInfo{}, err
	}
	var info domain.BlobInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, domain.BlobInfo{}, err
	}
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, domain.BlobInfo{}, domain.ErrBlobNotFound
	}
	return f, info, err
}

// Delete implements domain.BlobStore; deleting a missing blob succeeds.
func (b BlobFS) Delete(_ context.Context, id string) error {
	p, err := b.path(id)
	if err != nil {
		return err
	}
	for _, f := range []string{p, p + ".json"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}
