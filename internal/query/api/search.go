package api

import (
	"context"

	"organon/internal/kernel"
	schemaapi "organon/internal/schema/api"
)

// SearchDoc is one indexed document: a feature's searchable text of one
// visibility tier (spec 7.5). A public search never matches text of the
// owner tier, so hidden matches cannot leak.
type SearchDoc struct {
	ID     kernel.ID
	Type   schemaapi.TypeID // base type ID
	Tier   schemaapi.Visibility
	Owners []kernel.ID       // principals with owner access
	Scope  []kernel.ID       // the feature and its ancestors
	Text   map[string]string // path -> text
}

// SearchQuery is a full-text query.
type SearchQuery struct {
	Text  string
	Types []schemaapi.TypeID
	// Owners: owner-tier documents match only for these principals; nil
	// means owner-tier documents are excluded, AllOwners includes them all.
	Owners    []kernel.ID
	AllOwners bool
	// Members includes member-tier documents (signed-in callers).
	Members bool
	Limit   int
	Offset  int
}

// SearchHit is one match.
type SearchHit struct {
	ID         kernel.ID
	Tier       schemaapi.Visibility
	Highlights map[string][]string // path -> fragments
}

// SearchResult is a page of hits.
type SearchResult struct {
	Hits  []SearchHit
	Total int
}

// SearchIndex is the search port (spec 7.5); adapters: Bleve (embedded,
// development), PostgreSQL full-text search, Meilisearch.
type SearchIndex interface {
	Upsert(ctx context.Context, docs []SearchDoc) error
	// Delete removes every tier of the features.
	Delete(ctx context.Context, ids []kernel.ID) error
	Search(ctx context.Context, q SearchQuery) (SearchResult, error)
}
