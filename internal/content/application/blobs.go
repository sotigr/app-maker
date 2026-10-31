package application

import (
	"context"

	accessapi "app-maker/internal/access/api"
	contentapi "app-maker/internal/content/api"
	"app-maker/internal/kernel"
	schemaapi "app-maker/internal/schema/api"
)

// blobIDs returns the blob IDs of the top-level Blob fields by path.
func blobIDs(ct *schemaapi.CompiledType, fields map[string]kernel.Value) map[string]string {
	out := map[string]string{}
	for _, f := range ct.TopFields() {
		if b, ok := fields[f.Path].(kernel.Blob); ok {
			out[f.Path] = b.ID
		}
	}
	return out
}

// verifyBlobs checks blob values that a write sets or replaces (spec
// 14.4): each must be an upload for this type and field. Unchanged values
// are not checked again; system and fixture writes are trusted.
func (s *Service) verifyBlobs(ctx context.Context, wc WriteContext, w *work, old map[string]string) error {
	if wc.Principal.IsSystem() || wc.Fixture {
		return nil
	}
	fields := w.feature.Fields()
	var errs []kernel.FieldError
	for _, f := range w.ct.TopFields() {
		b, ok := fields[f.Path].(kernel.Blob)
		if !ok || old[f.Path] == b.ID {
			continue
		}
		if s.d.Blobs == nil {
			return kernel.NewError(kernel.CodeUnsupportedCapability, "blob storage is not configured")
		}
		if err := s.d.Blobs.Verify(ctx, wc.Principal, w.ct.Base(), f.Path, b); err != nil {
			errs = append(errs, kernel.FieldError{Path: f.Path, Code: "blob", Message: err.Error()})
		}
	}
	if len(errs) > 0 {
		return kernel.NewValidationError(errs...)
	}
	return nil
}

// link adds signed download URLs to the blob fields of a view (spec
// 14.4: only principals who can read the field get one).
func (s *Service) link(ct *schemaapi.CompiledType, v contentapi.FeatureView) contentapi.FeatureView {
	if s.d.Blobs == nil {
		return v
	}
	for _, f := range ct.TopFields() {
		if f.Blob == nil {
			continue
		}
		m, ok := v.Data[f.Path[1:]].(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		if id == "" {
			continue
		}
		linked := make(map[string]any, len(m)+2)
		for k, x := range m {
			linked[k] = x
		}
		url, thumbs := s.d.Blobs.Link(id, f.Blob.Thumbnails)
		linked["url"] = url
		if len(thumbs) > 0 {
			linked["thumbnails"] = thumbs
		}
		v.Data[f.Path[1:]] = linked
	}
	return v
}

// AuthorizeBlobField checks that p may set field of an existing feature
// (update policy) or of a new feature of typeName under parent (create
// policy, root by default) before an upload (spec 14.4).
func (s *Service) AuthorizeBlobField(ctx context.Context, p accessapi.Principal, typeName, feature, parent, field string) (*schemaapi.CompiledType, error) {
	ct, err := s.typeByName(typeName)
	if err != nil {
		return nil, err
	}
	wc := WriteContext{Principal: p}
	if feature != "" {
		id, err := kernel.ParseID(feature)
		if err != nil {
			return nil, kernel.ErrNotFound
		}
		f, fct, err := s.load(ctx, id)
		if err != nil {
			return nil, err
		}
		if fct.Base() != ct.Base() {
			return nil, kernel.ErrNotFound
		}
		ref := s.Ref(f, fct)
		aud, err := s.canRead(ctx, p, ref)
		if err != nil {
			return nil, err
		}
		if err := s.authorize(ctx, wc, accessapi.CheckRequest{Op: accessapi.OpUpdate, Type: fct, Feature: ref}); err != nil {
			return nil, err
		}
		if !fct.View(aud, schemaapi.DirUpdate).Writable[field] {
			return nil, kernel.NewError(kernel.CodeForbidden, "field %s is not writable", field)
		}
		return fct, nil
	}
	pid := contentapi.RootID
	if parent != "" {
		if pid, err = kernel.ParseID(parent); err != nil {
			return nil, kernel.ErrNotFound
		}
	}
	pf, pct, err := s.load(ctx, pid)
	if err != nil {
		return nil, err
	}
	pref := s.Ref(pf, pct)
	if _, err := s.canRead(ctx, p, pref); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, wc, accessapi.CheckRequest{Op: accessapi.OpCreate, Type: ct, Parent: pref}); err != nil {
		return nil, err
	}
	if !ct.View(s.createAudience(wc, ct), schemaapi.DirCreate).Writable[field] {
		return nil, kernel.NewError(kernel.CodeForbidden, "field %s is not writable", field)
	}
	return ct, nil
}
