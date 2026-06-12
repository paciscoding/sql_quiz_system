package models

import "time"

// IntroImage is the metadata row for an introduction image attached to either a
// practice category or an examination. The actual binary content is stored in
// the database (image_bytes column) but is never included in this struct — it
// is streamed separately by the dedicated GET-bytes handler.
type IntroImage struct {
	ID        int       `json:"id"`
	OwnerID   int       `json:"owner_id"`
	SortOrder int       `json:"sort_order"`
	MimeType  string    `json:"mime_type"`
	ByteSize  int       `json:"byte_size"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
