package services

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

// ImportRepo defines the repository subset required by StudentImportService.
// *repository.PostgresRepository satisfies this interface.
type ImportRepo interface {
	UpsertStudentByNumber(ctx context.Context, tx pgx.Tx, in models.StudentImportRow, creatorID int) (string, int, error)
	UpsertStudentRotating(ctx context.Context, tx pgx.Tx, in models.StudentImportRow, creatorID int) (string, int, error)
	WithTransaction(ctx context.Context, fn func(pgx.Tx) error) error
}

// ImportRowError carries failure details for a specific row in the roster.
type ImportRowError struct {
	Row           int    `json:"row"`
	StudentNumber string `json:"student_number"`
	Message       string `json:"message"`
}

// CreatedStudent carries the generated credential for a newly-inserted student
// so the caller can email it and/or surface it to the admin. UserID is used
// internally to log the password event and is not serialized.
type CreatedStudent struct {
	UserID        int    `json:"-"`
	Row           int    `json:"row"`
	StudentNumber string `json:"student_number"`
	StudentIDExt  string `json:"student_id_ext"`
	Email         string `json:"email"`
	Password      string `json:"password"`
}

// ImportReport summarizes the outcome of the import operation.
type ImportReport struct {
	Total   int              `json:"total"`
	Created int              `json:"created"`
	Updated int              `json:"updated"`
	Skipped int              `json:"skipped"`
	Errors  []ImportRowError `json:"errors"`
	// CreatedAccounts lists the generated credentials for every newly-inserted
	// student (empty for updates). The caller emails these and shows them once.
	CreatedAccounts []CreatedStudent `json:"created_accounts"`
}

// StudentImportService coordinates the roster import logic with per-row savepoints.
type StudentImportService struct {
	repo ImportRepo
}

// NewStudentImportService creates a new StudentImportService.
func NewStudentImportService(repo ImportRepo) *StudentImportService {
	return &StudentImportService{repo: repo}
}

// StudentUpsertInput is the canonical set of roster fields for creating or
// updating a single student. It is the shared input for BOTH the CSV importer
// and the single POST /students endpoint.
type StudentUpsertInput struct {
	StudentNumber string
	StudentIDExt  string
	SecLec        string
	SecLab        string
	FirstName     string
	LastName      string
	Email         string
}

// UpsertOne is the SINGLE source of truth for turning roster fields into a
// system_users row. It generates a random password, hashes it, and calls the
// repository upsert on the supplied transaction. Both the bulk importer and the
// single-student endpoint funnel through here, so a one-row import and a manual
// add produce an identical row by construction. The returned plaintext password
// is only meaningful when outcome == "created": the upsert leaves password_hash
// untouched on conflict, so an update keeps the student's existing password.
func (s *StudentImportService) UpsertOne(ctx context.Context, tx pgx.Tx, in StudentUpsertInput, creatorID int) (outcome string, userID int, password string, err error) {
	// Both identifiers are mandatory: student_number is the upsert key,
	// student_id_ext is the login username (UNIQUE NOT NULL).
	if in.StudentNumber == "" {
		return "", 0, "", fmt.Errorf("student number is required")
	}
	if in.StudentIDExt == "" {
		return "", 0, "", fmt.Errorf("student ID is required")
	}
	password = GeneratePassword()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", 0, "", err
	}
	row := models.StudentImportRow{
		StudentNumber: in.StudentNumber,
		StudentIDExt:  in.StudentIDExt,
		SecLec:        in.SecLec,
		SecLab:        in.SecLab,
		FirstName:     in.FirstName,
		LastName:      in.LastName,
		Email:         in.Email,
		PasswordHash:  string(hash),
	}
	outcome, userID, err = s.repo.UpsertStudentByNumber(ctx, tx, row, creatorID)
	if err != nil && repository.IsEmailConflict(err) {
		return "", 0, "", ErrEmailInUse
	}
	return outcome, userID, password, err
}

// CreateSingle upserts one student in its own transaction. It shares UpsertOne
// with the bulk path, guaranteeing manual==bulk parity. It returns the
// generated password, meaningful only when outcome == "created".
func (s *StudentImportService) CreateSingle(ctx context.Context, creatorID int, in StudentUpsertInput) (outcome string, userID int, password string, err error) {
	txErr := s.repo.WithTransaction(ctx, func(tx pgx.Tx) error {
		o, id, pw, e := s.UpsertOne(ctx, tx, in, creatorID)
		outcome, userID, password = o, id, pw
		return e
	})
	if txErr != nil {
		return "", 0, "", txErr
	}
	return outcome, userID, password, nil
}

// Import processes a roster of students, performing an upsert for each row.
// It uses a single outer transaction with savepoints to allow partial successes.
func (s *StudentImportService) Import(ctx context.Context, creatorID int, rows []ParsedStudentRow) (ImportReport, error) {
	// Errors/CreatedAccounts start as non-nil empty slices, not nil: Go
	// marshals a nil slice as JSON `null`, and a clean import (every row
	// succeeds) would otherwise leave both null. BulkImportStudents.vue reads
	// result.errors.length unguarded, so `null` throws a TypeError during
	// render and the admin sees a success toast with no results table.
	report := ImportReport{
		Total:           len(rows),
		Errors:          []ImportRowError{},
		CreatedAccounts: []CreatedStudent{},
	}

	err := s.repo.WithTransaction(ctx, func(tx pgx.Tx) error {
		for _, row := range rows {
			if row.StudentNumber == "" {
				report.Skipped++
				report.Errors = append(report.Errors, ImportRowError{
					Row:           row.RowNumber,
					StudentNumber: "",
					Message:       "Student number is required",
				})
				continue
			}
			// student_id_ext becomes the login username (UNIQUE NOT NULL), so it
			// must be present or the row cannot produce a valid account.
			if row.StudentIDExt == "" {
				report.Skipped++
				report.Errors = append(report.Errors, ImportRowError{
					Row:           row.RowNumber,
					StudentNumber: row.StudentNumber,
					Message:       "Student ID is required",
				})
				continue
			}

			// Establish savepoint for this row
			savepointName := fmt.Sprintf("row_%d", row.RowNumber)
			_, err := tx.Exec(ctx, "SAVEPOINT "+savepointName)
			if err != nil {
				return fmt.Errorf("failed to establish savepoint: %w", err)
			}

			// Route through the SHARED UpsertOne so a bulk row is identical to a
			// single POST /students create (hashing + upsert live in one place).
			outcome, uid, password, err := s.UpsertOne(ctx, tx, StudentUpsertInput{
				StudentNumber: row.StudentNumber,
				StudentIDExt:  row.StudentIDExt,
				SecLec:        row.SecLec,
				SecLab:        row.SecLab,
				FirstName:     row.FirstName,
				LastName:      row.LastName,
				Email:         row.Email,
			}, creatorID)
			if err != nil {
				// Rollback to savepoint on error
				tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+savepointName)
				report.Skipped++
				report.Errors = append(report.Errors, ImportRowError{
					Row:           row.RowNumber,
					StudentNumber: row.StudentNumber,
					Message:       err.Error(),
				})
				continue
			}

			// Release savepoint on success
			_, err = tx.Exec(ctx, "RELEASE SAVEPOINT "+savepointName)
			if err != nil {
				return fmt.Errorf("failed to release savepoint: %w", err)
			}

			if outcome == "created" {
				report.Created++
				report.CreatedAccounts = append(report.CreatedAccounts, CreatedStudent{
					UserID:        uid,
					Row:           row.RowNumber,
					StudentNumber: row.StudentNumber,
					StudentIDExt:  row.StudentIDExt,
					Email:         row.Email,
					Password:      password,
				})
			} else if outcome == "updated" {
				report.Updated++
			} else {
				// Should not happen based on ARCH-005 spec
				report.Skipped++
			}
		}
		return nil
	})

	return report, err
}

// Outcome and EmailStatus values for RotatedAccount. Callers outside this
// package (the controller, for password-event routing and email selection)
// must use these constants rather than re-typing the literals: an
// uncoordinated rename here would otherwise misroute password events or skip
// emails with no compile error to catch it.
const (
	OutcomeCreated     = "created"
	OutcomeUpdated     = "updated"
	EmailStatusQueued  = "queued"
	EmailStatusNoEmail = "no_email"
	// EmailStatusNotConfigured marks a row that WOULD have been queued (it has
	// an email address) but was not, because no real email sender is wired in
	// (the no-op stub is active). Set by the controller after ImportRotating
	// returns, once it knows whether AuthService.EmailConfigured(). Without
	// this distinct status, every row would read "queued" even though nothing
	// was ever sent.
	EmailStatusNotConfigured = "not_configured"
	// EmailStatusSkipped marks a row that has an email address, on a server
	// where email IS configured, that was deliberately not emailed because the
	// admin chose "don't send email" for this import. Distinct from
	// EmailStatusNotConfigured (a server limitation the admin did not choose)
	// and from EmailStatusNoEmail (no address on file): the admin needs to
	// know these students were never told their password, and that the
	// results table is the only copy. Set by the controller, like
	// EmailStatusNotConfigured.
	EmailStatusSkipped = "skipped"
)

// RotatedAccount is one account touched by the rotating importer. Unlike
// CreatedStudent it is emitted for updates too, because on that path the
// student's password is replaced and the plaintext is meaningful for every row.
type RotatedAccount struct {
	UserID        int    `json:"-"`
	Row           int    `json:"row"`
	StudentNumber string `json:"student_number"`
	StudentIDExt  string `json:"student_id_ext"`
	Email         string `json:"email"`
	Password      string `json:"password"`
	Outcome       string `json:"outcome"`      // OutcomeCreated | OutcomeUpdated
	EmailStatus   string `json:"email_status"` // EmailStatusQueued | EmailStatusNoEmail
}

// RotatingImportReport summarizes a rotating import. JobID identifies the
// background email job and is filled in by the controller, not the service.
// ExamActive is likewise filled in by the controller (see
// ImportStudentsWithCredentials): it records whether any exam was active at
// the moment this import ran, so the audit/report reflects it even when the
// UI's own preflight check was bypassed.
type RotatingImportReport struct {
	Total      int              `json:"total"`
	Created    int              `json:"created"`
	Updated    int              `json:"updated"`
	Skipped    int              `json:"skipped"`
	Errors     []ImportRowError `json:"errors"`
	Accounts   []RotatedAccount `json:"accounts"`
	JobID      string           `json:"job_id"`
	ExamActive bool             `json:"exam_active"`
}

// ImportRotating is the admin-only counterpart to Import: it rotates the
// credential of EVERY row in the roster — new and returning students alike —
// and reports the plaintext for each so it can be emailed and shown once. The
// transaction/savepoint structure is deliberately identical to Import's, so a
// single bad row is isolated instead of failing the file.
func (s *StudentImportService) ImportRotating(ctx context.Context, creatorID int, rows []ParsedStudentRow) (RotatingImportReport, error) {
	// Same nil-vs-empty-slice reasoning as Import above: a clean roster (no
	// errors) or an all-skipped roster (no accounts) must marshal `[]`, not
	// `null` — BulkImportStudentsCredentials.vue's hasHeldResults computed and
	// its render both dereference these arrays.
	report := RotatingImportReport{
		Total:    len(rows),
		Errors:   []ImportRowError{},
		Accounts: []RotatedAccount{},
	}

	err := s.repo.WithTransaction(ctx, func(tx pgx.Tx) error {
		for _, row := range rows {
			if row.StudentNumber == "" {
				report.Skipped++
				report.Errors = append(report.Errors, ImportRowError{
					Row:           row.RowNumber,
					StudentNumber: "",
					Message:       "Student number is required",
				})
				continue
			}
			if row.StudentIDExt == "" {
				report.Skipped++
				report.Errors = append(report.Errors, ImportRowError{
					Row:           row.RowNumber,
					StudentNumber: row.StudentNumber,
					Message:       "Student ID is required",
				})
				continue
			}

			savepointName := fmt.Sprintf("row_%d", row.RowNumber)
			if _, err := tx.Exec(ctx, "SAVEPOINT "+savepointName); err != nil {
				return fmt.Errorf("failed to establish savepoint: %w", err)
			}

			password := GeneratePassword()
			hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if err != nil {
				tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+savepointName)
				report.Skipped++
				report.Errors = append(report.Errors, ImportRowError{
					Row: row.RowNumber, StudentNumber: row.StudentNumber, Message: err.Error(),
				})
				continue
			}

			outcome, uid, err := s.repo.UpsertStudentRotating(ctx, tx, models.StudentImportRow{
				StudentNumber: row.StudentNumber,
				StudentIDExt:  row.StudentIDExt,
				SecLec:        row.SecLec,
				SecLab:        row.SecLab,
				FirstName:     row.FirstName,
				LastName:      row.LastName,
				Email:         row.Email,
				PasswordHash:  string(hash),
			}, creatorID)
			if err != nil {
				tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+savepointName)
				if repository.IsEmailConflict(err) {
					err = ErrEmailInUse
				}
				report.Skipped++
				report.Errors = append(report.Errors, ImportRowError{
					Row: row.RowNumber, StudentNumber: row.StudentNumber, Message: err.Error(),
				})
				continue
			}

			// Validate the outcome BEFORE releasing the savepoint: UpsertStudentRotating
			// has already applied the rotation (new hash + bumped token_version) at
			// this point, but RELEASE SAVEPOINT has not run yet, so a savepoint
			// ROLLBACK here still fully undoes it. Checking after RELEASE (as this
			// code used to) would be too late — the rotation would already be
			// committed with no audit row, no email, and no CSV line naming the
			// affected student (see IMPORTANT 2 in the final review).
			if outcome != OutcomeCreated && outcome != OutcomeUpdated {
				tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+savepointName)
				report.Skipped++
				report.Errors = append(report.Errors, ImportRowError{
					Row:           row.RowNumber,
					StudentNumber: row.StudentNumber,
					Message:       fmt.Sprintf("unexpected outcome %q from credential rotation; row rolled back", outcome),
				})
				continue
			}

			if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT "+savepointName); err != nil {
				return fmt.Errorf("failed to release savepoint: %w", err)
			}

			if outcome == OutcomeCreated {
				report.Created++
			} else {
				report.Updated++
			}

			// Decided synchronously, before any send is attempted, so the value is
			// accurate even though delivery is asynchronous.
			emailStatus := EmailStatusNoEmail
			if row.Email != "" {
				emailStatus = EmailStatusQueued
			}
			report.Accounts = append(report.Accounts, RotatedAccount{
				UserID:        uid,
				Row:           row.RowNumber,
				StudentNumber: row.StudentNumber,
				StudentIDExt:  row.StudentIDExt,
				Email:         row.Email,
				Password:      password,
				Outcome:       outcome,
				EmailStatus:   emailStatus,
			})
		}
		return nil
	})

	return report, err
}
