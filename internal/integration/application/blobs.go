package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	accessapi "organon/internal/access/api"
	changeapi "organon/internal/change/api"
	contentapi "organon/internal/content/api"
	flowapi "organon/internal/flow/api"
	"organon/internal/integration/domain"
	"organon/internal/kernel"
	queryapi "organon/internal/query/api"
	schemaapi "organon/internal/schema/api"
)

// Job kinds of blob bookkeeping (spec 14.4).
const (
	JobBlobState    = "blob_state"    // a committed or released blob
	JobBlobsOfPurge = "blob_purge"    // release the blobs of a purged feature
	JobCleanupBlobs = "cleanup_blobs" // periodic: delete unreferenced bytes
)

// Blob lifetimes (spec 14.4).
const (
	UploadURLTTL       = 15 * time.Minute
	DownloadURLTTL     = 15 * time.Minute
	UnreferencedMaxAge = 24 * time.Hour
	// DefaultMaxBlobBytes bounds Blob fields without x-maxBytes.
	DefaultMaxBlobBytes = 25 << 20
)

// BlobDeps wires the blob service.
type BlobDeps struct {
	Store  domain.BlobStore
	Signer domain.BlobSigner
	Types  schemaapi.TypeRegistry
	// Authorize checks that p may write field of a feature (update policy)
	// or of a new feature under parent (create policy) and returns the type.
	Authorize func(ctx context.Context, p accessapi.Principal, typeName, feature, parent, field string) (*schemaapi.CompiledType, error)
	Reader    contentapi.SystemReader
	Writer    contentapi.SystemWriter
	Clock     kernel.Clock
	Log       *slog.Logger
	// Prefix is the URL path of blob downloads and uploads ("/v1/blobs/").
	Prefix string
}

// Blobs implements uploads, downloads, and blob bookkeeping (spec 14.4).
type Blobs struct{ d BlobDeps }

var _ contentapi.Blobs = (*Blobs)(nil)

// NewBlobs returns the service.
func NewBlobs(d BlobDeps) *Blobs {
	if d.Prefix == "" {
		d.Prefix = "/v1/blobs/"
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Blobs{d: d}
}

// UploadRequest is POST /v1/uploads.
type UploadRequest struct {
	Type        string `json:"type"`
	Field       string `json:"field"`
	Feature     string `json:"feature,omitempty"` // existing feature (update policy)
	Parent      string `json:"parent,omitempty"`  // new feature's parent (create policy)
	Size        int64  `json:"size"`
	ContentType string `json:"contentType"`
}

// UploadTicket answers an upload request.
type UploadTicket struct {
	Blob      kernel.Blob       `json:"blob"`
	URL       string            `json:"uploadUrl"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expiresAt"`
}

// CreateUpload checks policy and schema limits and records an upload
// intent; the client PUTs the bytes to the signed URL, then sets the
// returned blob on the field in a normal write.
func (b *Blobs) CreateUpload(ctx context.Context, p accessapi.Principal, req UploadRequest) (UploadTicket, error) {
	field := "/" + strings.TrimPrefix(req.Field, "/")
	ct, err := b.d.Authorize(ctx, p, req.Type, req.Feature, req.Parent, field)
	if err != nil {
		return UploadTicket{}, err
	}
	fd, ok := ct.Field(field)
	if !ok || fd.Blob == nil || !fd.Top {
		return UploadTicket{}, kernel.NewValidationError(kernel.FieldError{Path: "/field", Code: "blob", Message: req.Field + " is not a Blob field of " + req.Type})
	}
	limit := fd.Blob.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxBlobBytes
	}
	var errs []kernel.FieldError
	if req.Size <= 0 || req.Size > limit {
		errs = append(errs, kernel.FieldError{Path: "/size", Code: "maximum", Message: fmt.Sprintf("size must be between 1 and %d bytes", limit)})
	}
	ctype := strings.ToLower(strings.TrimSpace(req.ContentType))
	if ctype == "" || !contentTypeAllowed(ctype, fd.Blob.ContentTypes) {
		errs = append(errs, kernel.FieldError{Path: "/contentType", Code: "enum", Message: "content type " + req.ContentType + " is not allowed"})
	}
	if len(errs) > 0 {
		return UploadTicket{}, kernel.NewValidationError(errs...)
	}
	now := b.d.Clock.Now()
	exp := now.Add(UploadURLTTL)
	data := map[string]any{"state": "pending", "target": string(ct.Base()), "field": field, "size": req.Size,
		"contentType": ctype, "expiresAt": exp.UTC().Format(time.RFC3339Nano)}
	if !p.ID.IsZero() {
		data["principal"] = p.ID.String()
	}
	id, err := b.d.Writer.Create(ctx, "blob", nil, data, nil)
	if err != nil {
		return UploadTicket{}, err
	}
	sid := id.String()
	return UploadTicket{Blob: kernel.Blob{ID: sid, Size: req.Size, ContentType: ctype},
		URL: b.signed("put", sid, exp), Method: "PUT", Headers: map[string]string{"Content-Type": ctype}, ExpiresAt: exp}, nil
}

func contentTypeAllowed(ct string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		a = strings.ToLower(a)
		if a == ct || (strings.HasSuffix(a, "/*") && strings.HasPrefix(ct, strings.TrimSuffix(a, "*"))) {
			return true
		}
		if ok, _ := path.Match(a, ct); ok {
			return true
		}
	}
	return false
}

func (b *Blobs) signed(op, id string, exp time.Time) string {
	q := url.Values{"exp": {strconv.FormatInt(exp.Unix(), 10)}, "sig": {b.d.Signer.Sign(op, id, exp)}}
	return b.d.Prefix + id + "?" + q.Encode()
}

// intent reads an upload intent.
func (b *Blobs) intent(ctx context.Context, id string) (contentapi.StoredFeature, error) {
	fid, err := kernel.ParseID(id)
	if err != nil {
		return contentapi.StoredFeature{}, kernel.ErrNotFound
	}
	f, err := b.d.Reader.Get(ctx, fid)
	if err != nil {
		return contentapi.StoredFeature{}, err
	}
	if f.Type.Base() != schemaapi.TypeBlob {
		return contentapi.StoredFeature{}, kernel.ErrNotFound
	}
	return f, nil
}

func str(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func num(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case int:
		return int64(v)
	}
	return -1
}

// Upload stores the bytes of a pending intent (PUT to the signed URL).
func (b *Blobs) Upload(ctx context.Context, id, exp, sig, contentType string, body io.Reader) error {
	if !b.d.Signer.Valid("put", id, exp, sig, b.d.Clock.Now()) {
		return kernel.NewError(kernel.CodeForbidden, "the upload URL is invalid or expired")
	}
	f, err := b.intent(ctx, id)
	if err != nil {
		return err
	}
	if str(f.Data, "state") != "pending" {
		return kernel.NewError(kernel.CodeConflict, "the blob was already uploaded")
	}
	want := str(f.Data, "contentType")
	if got, _, _ := strings.Cut(strings.ToLower(contentType), ";"); strings.TrimSpace(got) != want {
		return kernel.NewError(kernel.CodeValidationFailed, "Content-Type must be %s", want)
	}
	if err := b.d.Store.Put(ctx, id, body, num(f.Data, "size"), want); err != nil {
		return kernel.NewError(kernel.CodeValidationFailed, "upload failed: %v", err)
	}
	return b.d.Writer.Update(ctx, "blob", f.ID, f.Version, map[string]any{"state": "uploaded",
		"uploadedAt": b.d.Clock.Now().UTC().Format(time.RFC3339Nano)})
}

// Verify implements contentapi.Blobs.
func (b *Blobs) Verify(ctx context.Context, p accessapi.Principal, t schemaapi.TypeID, path string, v kernel.Blob) error {
	f, err := b.intent(ctx, v.ID)
	if errors.Is(err, kernel.ErrNotFound) {
		return errors.New("unknown blob; request an upload first")
	}
	if err != nil {
		return err
	}
	switch str(f.Data, "state") {
	case "uploaded":
	case "pending":
		return errors.New("the blob has not been uploaded")
	default:
		return errors.New("the blob is already in use or deleted")
	}
	if str(f.Data, "target") != string(t.Base()) || str(f.Data, "field") != path {
		return errors.New("the blob was uploaded for another field")
	}
	if owner := str(f.Data, "principal"); owner != "" && owner != p.ID.String() {
		return errors.New("the blob was uploaded by another principal")
	}
	if num(f.Data, "size") != v.Size || str(f.Data, "contentType") != v.ContentType {
		return errors.New("size or content type differ from the upload")
	}
	return nil
}

// Link implements contentapi.Blobs. Expiry is rounded to five minutes so
// URLs stay stable (and cacheable) for a while.
func (b *Blobs) Link(id string, thumbnails []int) (string, map[string]string) {
	exp := b.d.Clock.Now().Truncate(5 * time.Minute).Add(DownloadURLTTL)
	var thumbs map[string]string
	if len(thumbnails) > 0 {
		thumbs = map[string]string{}
		for _, s := range thumbnails {
			tid := thumbID(id, s)
			thumbs[strconv.Itoa(s)] = b.signed("get", tid, exp)
		}
	}
	return b.signed("get", id, exp), thumbs
}

func thumbID(id string, size int) string { return id + "_t" + strconv.Itoa(size) }

// Download opens a blob for a signed URL.
func (b *Blobs) Download(ctx context.Context, id, exp, sig string) (io.ReadCloser, domain.BlobInfo, error) {
	if !b.d.Signer.Valid("get", id, exp, sig, b.d.Clock.Now()) {
		return nil, domain.BlobInfo{}, kernel.NewError(kernel.CodeForbidden, "the download URL is invalid or expired")
	}
	r, info, err := b.d.Store.Get(ctx, id)
	if errors.Is(err, domain.ErrBlobNotFound) {
		return nil, domain.BlobInfo{}, kernel.ErrNotFound
	}
	return r, info, err
}

type blobStateArgs struct {
	Blob       string    `json:"blob"`
	State      string    `json:"state"`
	Feature    string    `json:"feature,omitempty"`
	Thumbnails []int     `json:"thumbnails,omitempty"`
	At         time.Time `json:"at"`
}

// Jobs implements the content commit hook: blobs set on a field become
// committed, replaced or removed ones released, and a purge releases all
// blobs of the feature.
func (b *Blobs) Jobs(_ context.Context, events []changeapi.Event, _ flowapi.CausationChain) ([]kernel.JobRequest, error) {
	var jobs []kernel.JobRequest
	add := func(kind string, a any) {
		raw, _ := json.Marshal(a)
		jobs = append(jobs, kernel.JobRequest{Kind: kind, Queue: kernel.QueueMaintenance, Args: raw, MaxAttempts: 10})
	}
	for _, ev := range events {
		ct, ok := b.d.Types.Get(ev.TypeID)
		if !ok {
			ct, ok = b.d.Types.Get(ev.TypeID.Base())
		}
		if !ok || ct.Base() == schemaapi.TypeBlob {
			continue
		}
		if ev.Kind == changeapi.EventPurged {
			add(JobBlobsOfPurge, blobStateArgs{Feature: ev.FeatureID.String(), At: ev.At})
			continue
		}
		if ev.Kind != changeapi.EventCreated && ev.Kind != changeapi.EventUpdated {
			continue
		}
		for _, fd := range ct.TopFields() {
			if fd.Blob == nil {
				continue
			}
			key := fd.Path[1:]
			newID, oldID := wireBlobID(ev.After[key]), wireBlobID(ev.Before[key])
			if ev.Kind == changeapi.EventUpdated && !contains(ev.Changed, fd.Path) {
				continue
			}
			if newID == oldID {
				continue
			}
			if newID != "" {
				add(JobBlobState, blobStateArgs{Blob: newID, State: "committed", Feature: ev.FeatureID.String(), Thumbnails: fd.Blob.Thumbnails, At: ev.At})
			}
			if oldID != "" {
				add(JobBlobState, blobStateArgs{Blob: oldID, State: "released", At: ev.At})
			}
		}
	}
	return jobs, nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func wireBlobID(v any) string {
	switch x := v.(type) {
	case map[string]any:
		s, _ := x["id"].(string)
		return s
	case kernel.Blob:
		return x.ID
	}
	return ""
}

// HandleBlobState records a commit or release; committing an image field
// with x-thumbnails also renders its thumbnails.
func (b *Blobs) HandleBlobState(ctx context.Context, raw json.RawMessage) error {
	var a blobStateArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return err
	}
	f, err := b.intent(ctx, a.Blob)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	patch := map[string]any{"state": a.State}
	switch a.State {
	case "committed":
		patch["feature"] = a.Feature
		patch["releasedAt"] = nil
	case "released":
		if str(f.Data, "state") == "deleted" {
			return nil
		}
		patch["releasedAt"] = a.At.UTC().Format(time.RFC3339Nano)
	}
	if err := b.d.Writer.Update(ctx, "blob", f.ID, f.Version, patch); err != nil {
		return err
	}
	if a.State == "committed" && len(a.Thumbnails) > 0 {
		return b.thumbnails(ctx, a.Blob, a.Thumbnails)
	}
	return nil
}

// HandleBlobsOfPurge releases every blob committed to a purged feature.
func (b *Blobs) HandleBlobsOfPurge(ctx context.Context, raw json.RawMessage) error {
	var a blobStateArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return err
	}
	fs, err := b.d.Reader.Find(ctx, schemaapi.TypeBlob, queryapi.And{Terms: []queryapi.Predicate{
		queryapi.Compare{Path: "/feature", Op: queryapi.OpEq, Value: kernel.String(a.Feature)},
		queryapi.Compare{Path: "/state", Op: queryapi.OpEq, Value: kernel.Enum("committed")},
	}}, 1000)
	if err != nil {
		return err
	}
	for _, f := range fs {
		if err := b.d.Writer.Update(ctx, "blob", f.ID, f.Version, map[string]any{"state": "released",
			"releasedAt": a.At.UTC().Format(time.RFC3339Nano)}); err != nil && !errors.Is(err, kernel.ErrVersionConflict) {
			return err
		}
	}
	return nil
}

// CleanupBlobs deletes bytes never committed, or released, more than 24
// hours ago (spec 14.4).
func (b *Blobs) CleanupBlobs(ctx context.Context, _ json.RawMessage) error {
	cutoff := kernel.Time{T: b.d.Clock.Now().Add(-UnreferencedMaxAge).UTC()}
	where := queryapi.Or{Terms: []queryapi.Predicate{
		queryapi.And{Terms: []queryapi.Predicate{
			queryapi.Compare{Path: "/state", Op: queryapi.OpIn, Value: kernel.List{Elem: kernel.KindEnum, Items: []kernel.Value{kernel.Enum("pending"), kernel.Enum("uploaded")}}},
			queryapi.Compare{Path: "/createdAt", Op: queryapi.OpLt, Value: cutoff},
		}},
		queryapi.And{Terms: []queryapi.Predicate{
			queryapi.Compare{Path: "/state", Op: queryapi.OpEq, Value: kernel.Enum("released")},
			queryapi.Compare{Path: "/releasedAt", Op: queryapi.OpLt, Value: cutoff},
		}},
	}}
	fs, err := b.d.Reader.Find(ctx, schemaapi.TypeBlob, where, 500)
	if err != nil {
		return err
	}
	for _, f := range fs {
		id := f.ID.String()
		if err := b.d.Store.Delete(ctx, id); err != nil {
			return err
		}
		if l, ok := f.Data["thumbnails"].([]any); ok {
			for _, s := range l {
				if n, ok := s.(int64); ok {
					_ = b.d.Store.Delete(ctx, thumbID(id, int(n)))
				}
			}
		}
		if err := b.d.Writer.Update(ctx, "blob", f.ID, f.Version, map[string]any{"state": "deleted"}); err != nil && !errors.Is(err, kernel.ErrVersionConflict) {
			return err
		}
	}
	if len(fs) > 0 {
		b.d.Log.Info("cleanup_blobs deleted unreferenced blobs", "count", len(fs))
	}
	return nil
}

// thumbnails renders image thumbnails of a committed blob (spec 14.4);
// other content types are skipped.
func (b *Blobs) thumbnails(ctx context.Context, id string, sizes []int) error {
	r, info, err := b.d.Store.Get(ctx, id)
	if errors.Is(err, domain.ErrBlobNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	src, err := io.ReadAll(io.LimitReader(r, info.Size+1))
	_ = r.Close()
	if err != nil {
		return err
	}
	var done []any
	for _, s := range sizes {
		out, ct, err := domain.Thumbnail(src, info.ContentType, s)
		if errors.Is(err, domain.ErrNotImage) {
			return nil
		}
		if err != nil {
			b.d.Log.Warn("thumbnail failed", "blob", id, "size", s, "err", err)
			return nil // a broken image is not worth retrying
		}
		if err := b.d.Store.Put(ctx, thumbID(id, s), bytes.NewReader(out), int64(len(out)), ct); err != nil {
			return err
		}
		done = append(done, s)
	}
	f, err := b.intent(ctx, id)
	if err != nil {
		return err
	}
	return b.d.Writer.Update(ctx, "blob", f.ID, f.Version, map[string]any{"thumbnails": done})
}
