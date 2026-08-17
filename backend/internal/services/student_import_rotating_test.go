package services

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// fakeRotatingRepo implements the widened ImportRepo. It records every upsert
// and replays a scripted outcome/error per call index, mirroring fakeImportRepo.
type fakeRotatingRepo struct {
	rotatingCalls []models.StudentImportRow
	outcomes      []string
	errors        []error
}

func (f *fakeRotatingRepo) UpsertStudentByNumber(ctx context.Context, tx pgx.Tx, in models.StudentImportRow, creatorID int) (string, int, error) {
	return "created", 1, nil
}

func (f *fakeRotatingRepo) UpsertStudentRotating(ctx context.Context, tx pgx.Tx, in models.StudentImportRow, creatorID int) (string, int, error) {
	f.rotatingCalls = append(f.rotatingCalls, in)
	idx := len(f.rotatingCalls) - 1
	outcome := "created"
	if idx < len(f.outcomes) {
		outcome = f.outcomes[idx]
	}
	var err error
	if idx < len(f.errors) {
		err = f.errors[idx]
	}
	return outcome, idx + 1, err
}

func (f *fakeRotatingRepo) WithTransaction(ctx context.Context, fn func(pgx.Tx) error) error {
	return fn(&fakeTx{})
}

func TestImportRotating(t *testing.T) {
	ctx := context.Background()

	t.Run("every account row carries a password, created and updated alike", func(t *testing.T) {
		repo := &fakeRotatingRepo{outcomes: []string{"created", "updated"}}
		service := NewStudentImportService(repo)

		report, err := service.ImportRotating(ctx, 7, []ParsedStudentRow{
			{RowNumber: 1, StudentNumber: "N1", StudentIDExt: "s1", Email: "s1@example.com"},
			{RowNumber: 2, StudentNumber: "N2", StudentIDExt: "s2", Email: "s2@example.com"},
		})
		if err != nil {
			t.Fatalf("ImportRotating: %v", err)
		}
		if report.Total != 2 || report.Created != 1 || report.Updated != 1 || report.Skipped != 0 {
			t.Fatalf("counts = total:%d created:%d updated:%d skipped:%d, want 2/1/1/0",
				report.Total, report.Created, report.Updated, report.Skipped)
		}
		if len(report.Accounts) != 2 {
			t.Fatalf("len(Accounts) = %d, want 2 (updates must appear too)", len(report.Accounts))
		}
		for _, a := range report.Accounts {
			if a.Password == "" {
				t.Errorf("row %d has an empty password", a.Row)
			}
			if a.EmailStatus != "queued" {
				t.Errorf("row %d email_status = %q, want \"queued\"", a.Row, a.EmailStatus)
			}
		}
		if report.Accounts[0].Outcome != "created" || report.Accounts[1].Outcome != "updated" {
			t.Errorf("outcomes = %q/%q, want created/updated",
				report.Accounts[0].Outcome, report.Accounts[1].Outcome)
		}
		if report.Accounts[0].Password == report.Accounts[1].Password {
			t.Error("two rows share one password: each row must get its own GeneratePassword()")
		}
		if report.JobID != "" {
			t.Errorf("JobID = %q, want \"\": the controller assigns it", report.JobID)
		}
	})

	t.Run("row without an email is imported and flagged no_email", func(t *testing.T) {
		repo := &fakeRotatingRepo{outcomes: []string{"updated"}}
		service := NewStudentImportService(repo)

		report, err := service.ImportRotating(ctx, 7, []ParsedStudentRow{
			{RowNumber: 1, StudentNumber: "N1", StudentIDExt: "s1"},
		})
		if err != nil {
			t.Fatalf("ImportRotating: %v", err)
		}
		if report.Updated != 1 {
			t.Fatalf("Updated = %d, want 1: a missing email must not skip the row", report.Updated)
		}
		if report.Accounts[0].EmailStatus != "no_email" {
			t.Errorf("email_status = %q, want \"no_email\"", report.Accounts[0].EmailStatus)
		}
		if report.Accounts[0].Password == "" {
			t.Error("password must still be reported so the admin can hand-deliver it")
		}
	})

	t.Run("missing identifiers are skipped with a row error", func(t *testing.T) {
		repo := &fakeRotatingRepo{outcomes: []string{"created"}}
		service := NewStudentImportService(repo)

		report, err := service.ImportRotating(ctx, 7, []ParsedStudentRow{
			{RowNumber: 1, StudentNumber: "", StudentIDExt: "s1"},
			{RowNumber: 2, StudentNumber: "N2", StudentIDExt: ""},
			{RowNumber: 3, StudentNumber: "N3", StudentIDExt: "s3"},
		})
		if err != nil {
			t.Fatalf("ImportRotating: %v", err)
		}
		if report.Skipped != 2 || len(report.Errors) != 2 {
			t.Fatalf("skipped=%d errors=%d, want 2/2", report.Skipped, len(report.Errors))
		}
		if len(report.Accounts) != 1 || report.Accounts[0].Row != 3 {
			t.Fatalf("Accounts = %+v, want only row 3", report.Accounts)
		}
	})

	t.Run("a failing row is isolated and the import continues", func(t *testing.T) {
		repo := &fakeRotatingRepo{
			outcomes: []string{"created", "created"},
			errors:   []error{errors.New("boom"), nil},
		}
		service := NewStudentImportService(repo)

		report, err := service.ImportRotating(ctx, 7, []ParsedStudentRow{
			{RowNumber: 1, StudentNumber: "N1", StudentIDExt: "s1", Email: "a@example.com"},
			{RowNumber: 2, StudentNumber: "N2", StudentIDExt: "s2", Email: "b@example.com"},
		})
		if err != nil {
			t.Fatalf("ImportRotating: %v", err)
		}
		if report.Skipped != 1 || report.Created != 1 {
			t.Fatalf("skipped=%d created=%d, want 1/1", report.Skipped, report.Created)
		}
		if len(report.Errors) != 1 || report.Errors[0].Row != 1 {
			t.Fatalf("Errors = %+v, want one entry for row 1", report.Errors)
		}
	})
}
