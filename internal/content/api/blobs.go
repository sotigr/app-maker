package api

import (
	"context"

	accessapi "organon/internal/access/api"
	"organon/internal/kernel"
	schemaapi "organon/internal/schema/api"
)

// Blobs connects Blob fields to blob storage (spec 14.4); implemented by
// the Integration context.
type Blobs interface {
	// Verify accepts a blob newly set on field path of type t: an upload by
	// p for that type and field, of the declared size and content type.
	Verify(ctx context.Context, p accessapi.Principal, t schemaapi.TypeID, path string, b kernel.Blob) error
	// Link returns a short-lived signed download URL and thumbnail URLs.
	Link(id string, thumbnails []int) (url string, thumbs map[string]string)
}
