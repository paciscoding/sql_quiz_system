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

			// Establish savepoint for this row
			savepointName := fmt.Sprintf("row_%d", row.RowNumber)
			_, err := tx.Exec(ctx, "SAVEPOINT "+savepointName)
			if err != nil {
				return fmt.Errorf("failed to establish savepoint: %w", err)
			}

			// ARCH-002: Hash student_number for initial password
			hash, err := bcrypt.GenerateFromPassword([]byte(row.StudentNumber), bcrypt.DefaultCost)
			if err != nil {
				// This is a system error, but we treat it as a row error to keep moving
				tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+savepointName)
				report.Skipped++
				report.Errors = append(report.Errors, ImportRowError{
					Row:           row.RowNumber,
					StudentNumber: row.StudentNumber,
					Message:       fmt.Sprintf("failed to hash password: %v", err),
				})
				continue
			}

			importRow := models.StudentImportRow{
				StudentNumber: row.StudentNumber,
				StudentIDExt:  row.StudentIDExt,
				SecLec:        row.SecLec,
				SecLab:        row.SecLab,
				FirstName:     row.FirstName,
				LastName:      row.LastName,
				PasswordHash:  string(hash),
			}

			outcome, _, err := s.repo.UpsertStudentByNumber(ctx, tx, importRow, creatorID)
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
