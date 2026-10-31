package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strconv"
	"time"

	contentapp "app-maker/internal/content/application"
	integrationapp "app-maker/internal/integration/application"
	integrationdomain "app-maker/internal/integration/domain"
	integrationinfra "app-maker/internal/integration/infrastructure"
	"app-maker/platform/config"
)

// blobService wires blob storage (spec 14.4): the store, the signing key
// derived from the instance key, content verification and links, the
// commit hook, and the bookkeeping jobs.
func (rt *Runtime) blobService(cfg config.Config, instanceKey []byte, opt Options) (*integrationapp.Blobs, error) {
	dir := cfg.Blobs.Dir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "fw-blobs-"+strconv.Itoa(os.Getuid()))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, instanceKey)
	m.Write([]byte("blob-urls"))
	sys := rt.Content.SystemPorts()
	blobs := integrationapp.NewBlobs(integrationapp.BlobDeps{
		Store: integrationinfra.BlobFS{Dir: dir}, Signer: integrationdomain.BlobSigner{Key: m.Sum(nil)},
		Types: rt.App.Types, Authorize: rt.Content.AuthorizeBlobField, Reader: sys, Writer: sys,
		Clock: opt.Clock, Log: opt.Log,
	})
	rt.Content.SetBlobs(blobs)
	rt.Content.AddHook(blobs)
	rt.JobKinds.Register(integrationapp.JobBlobState, blobs.HandleBlobState, time.Minute)
	rt.JobKinds.Register(integrationapp.JobBlobsOfPurge, blobs.HandleBlobsOfPurge, time.Minute)
	rt.JobKinds.Register(integrationapp.JobCleanupBlobs, blobs.CleanupBlobs, 5*time.Minute)
	return blobs, nil
}

var _ contentapp.CommitHook = (*integrationapp.Blobs)(nil)
