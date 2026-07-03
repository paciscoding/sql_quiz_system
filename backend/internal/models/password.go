package models

import "time"

// Password-event source values. Persisted in app.password_events.source and
// enforced by a CHECK constraint.
const (
	PasswordEventCreate      = "create"
	PasswordEventSelfChange  = "self_change"
	PasswordEventAdminReset  = "admin_reset"
	PasswordEventForgotReset = "forgot_reset"
)

// PasswordEvent is the raw, at-rest representation of a password-change record.
// NewPasswordEnc holds AES-256-GCM ciphertext of the new password value; the
// plaintext is never stored.
type PasswordEvent struct {
	ID             int
	UserID         int
	ChangedBy      *int
	Source         string
	NewPasswordEnc []byte
	Nonce          []byte
	NotifiedAdmin  bool
	CreatedAt      time.Time
}

// PasswordEventRow is a changelog row as fetched from the DB, with the actor's
// username resolved via join. The ciphertext is decrypted by the service layer
// before being exposed through PasswordEventView.
type PasswordEventRow struct {
	Source            string
	ChangedByUsername string
	CreatedAt         time.Time
	NewPasswordEnc    []byte
	Nonce             []byte
}

// PasswordEventView is the admin-facing changelog entry. NewPassword is only
// populated when the encryption key is configured; otherwise it is omitted.
type PasswordEventView struct {
	Source            string    `json:"source"`
	ChangedByUsername string    `json:"changed_by_username"`
	CreatedAt         time.Time `json:"created_at"`
	NewPassword       string    `json:"new_password,omitempty"`
}
