package models

// bulk_reset.go defines the types behind the admin bulk password-reset
// endpoint (ARCH-031). BulkResetTarget is the repository-internal read
// projection (never serialized); BulkResetResult and BulkResetFailure are the
// per-student rows of the HTTP response and carry the JSON tags ARCH-031 (7)
// specifies.

// BulkResetTarget is one row of the profile columns ResolveBulkResetTargets
// selects from app.system_users BEFORE a password is generated for it.
// UserID is the raw internal id (this type never crosses the JSON boundary),
// matching the int-native repository/service convention used elsewhere
// (ARCH-002) — compare models.PasswordEventRow, which is likewise
// internal-only and untagged.
type BulkResetTarget struct {
	UserID        int
	Username      string
	StudentNumber string
	FirstName     string
	LastName      string
	SecLec        string
	SecLab        string
}

// BulkResetResult is one successfully reset student, as returned to the
// admin in the bulk-reset-password response. Password is the ONLY channel
// the plaintext ever travels on — there is deliberately no endpoint that can
// re-fetch it (ARCH-031/032 S5).
type BulkResetResult struct {
	UserID        UserID `json:"user_id"`
	Username      string `json:"username"`
	StudentNumber string `json:"student_number"`
	FirstName     string `json:"first_name"`
	LastName      string `json:"last_name"`
	SecLec        string `json:"sec_lec"`
	SecLab        string `json:"sec_lab"`
	Password      string `json:"password"`
}

// BulkResetFailure records one target that could not be reset within a batch.
// The batch is NOT transactional across students (ARCH-031 (5)): a failure
// here means this one student's password/token_version are UNCHANGED, while
// every other target in the batch still succeeds.
type BulkResetFailure struct {
	UserID   UserID `json:"user_id"`
	Username string `json:"username"`
	Error    string `json:"error"`
}
