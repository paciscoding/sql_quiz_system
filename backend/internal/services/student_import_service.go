package services

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"golang.org/x/crypto/bcrypt"
)

// ImportRepo defines the repository subset required by StudentImportService.
// *repository.PostgresRepository satisfies this interface.
type ImportRepo interface {
	UpsertStudentByNumber(ctx context.Context, tx pgx.Tx, in models.StudentImportRow, creatorID int) (string, int, error)
	WithTransaction(ctx context.Context, fn func(pgx.Tx) error) error
}

// ImportRowError carries failure details for a specific row in the roster.
type ImportRowError struct {
	Row           int    `json:"row"`
	StudentNumber string `json:"student_number"`
	Message       string `json:"message"`
}

// ImportReport summarizes the outcome of the import operation.
type ImportReport struct {
	Total   int              `json:"total"`
	Created int              `json:"created"`
	Updated int              `json:"updated"`
	Skipped int              `json:"skipped"`
	Errors  []ImportRowError `json:"errors"`
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
// system_users row. It hashes the default password (= student_number) and calls
// the repository upsert on the supplied transaction. Both the bulk importer and
// the single-student endpoint funnel through here, so a one-row import and a
// manual add produce an identical row by construction.
func (s *StudentImportService) UpsertOne(ctx context.Context, tx pgx.Tx, in StudentUpsertInput, creatorID int) (outcome string, userID int, err error) {
	// Both identifiers are mandatory: student_number is the upsert key + default
	// password, student_id_ext is the login username (UNIQUE NOT NULL).
	if in.StudentNumber == "" {
		return "", 0, fmt.Errorf("student number is required")
	}
	if in.StudentIDExt == "" {
		return "", 0, fmt.Errorf("student ID is required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.StudentNumber), bcrypt.DefaultCost)
	if err != nil {
		return "", 0, err
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
	return s.repo.UpsertStudentByNumber(ctx, tx, row, creatorID)
}

// CreateSingle upserts one student in its own transaction. It shares UpsertOne
// with the bulk path, guaranteeing manual==bulk parity. The default password is
// the student_number.
func (s *StudentImportService) CreateSingle(ctx context.Context, creatorID int, in StudentUpsertInput) (outcome string, userID int, err error) {
	txErr := s.repo.WithTransaction(ctx, func(tx pgx.Tx) error {
		o, id, e := s.UpsertOne(ctx, tx, in, creatorID)
		outcome, userID = o, id
		return e
	})
	if txErr != nil {
		return "", 0, txErr
	}
	return outcome, userID, nil
}

// Import processes a roster of students, performing an upsert for each row.
// It uses a single outer transaction with savepoints to allow partial successes.
func (s *StudentImportService) Import(ctx context.Context, creatorID int, rows []ParsedStudentRow) (ImportReport, error) {
	report := ImportReport{
		Total: len(rows),
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
			outcome, _, err := s.UpsertOne(ctx, tx, StudentUpsertInput{
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
