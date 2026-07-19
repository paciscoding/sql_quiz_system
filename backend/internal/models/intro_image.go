package models

import "time"

// IntroImage is the metadata row for an introduction image attached to either a
// practice category or an examination. The actual binary content is stored in
// the database (image_bytes column) but is never included in this struct — it
// is streamed separately by the dedicated GET-bytes handler.
type IntroImage struct {
	ID ImageID `json:"id"`
	// OwnerID is intentionally left as a plain int (ARCH-006 exclusion): it is
	// a polymorphic FK (the owner is either an examination or a practice
	// category depending on context), so it has no single prefix type, and it
	// is never used as a route param (images are fetched under a known
	// parent path).
	OwnerID   int       `json:"owner_id"`
	SortOrder int       `json:"sort_order"`
	MimeType  string    `json:"mime_type"`
	ByteSize  int       `json:"byte_size"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
