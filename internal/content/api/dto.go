// Package api is the Content context's published contract: projected
// feature DTOs and the application service ports other contexts call
// (spec 2.3: flows call back into Content).
package api

import (
	"time"

	"organon/internal/kernel"
)

// RootID is the fixed ID of the app's root feature (spec 3.7, 8.4).
var RootID = kernel.MustParseID("00000000-0000-7000-8000-000000000002")

// FeatureView is a feature projected through a caller's read view. It is
// the only shape in which features leave the server (spec 1.4).
type FeatureView struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Parent    *string        `json:"parent,omitempty"`
	Version   kernel.Version `json:"version"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	Data      map[string]any `json:"data"`
}
