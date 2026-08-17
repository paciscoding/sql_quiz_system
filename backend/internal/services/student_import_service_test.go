package services

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

type fakeImportRepo struct {
	upsertCalls []models.StudentImportRow
	outcomes    []string
	errors      []error
}

func (f *fakeImportRepo) UpsertStudentByNumber(ctx context.Context, tx pgx.Tx, in models.StudentImportRow, creatorID int) (string, int, error) {
	f.upsertCalls = append(f.upsertCalls, in)

	idx := len(f.upsertCalls) - 1
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

func (f *fakeImportRepo) UpsertStudentRotating(ctx context.Context, tx pgx.Tx, in models.StudentImportRow, creatorID int) (string, int, error) {
	return f.UpsertStudentByNumber(ctx, tx, in, creatorID)
}

func (f *fakeImportRepo) WithTransaction(ctx context.Context, fn func(pgx.Tx) error) error {
	return fn(&fakeTx{})
}

type fakeTx struct{}

func (f *fakeTx) Begin(ctx context.Context) (pgx.Tx, error) { return f, nil }
func (f *fakeTx) Commit(ctx context.Context) error          { return nil }
func (f *fakeTx) Rollback(ctx context.Context) error        { return nil }
func (f *fakeTx) CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error) {
	return 0, nil
}
func (f *fakeTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults { return nil }
func (f *fakeTx) LargeObjects() pgx.LargeObjects                              { return pgx.LargeObjects{} }
func (f *fakeTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	return nil, nil
}
func (f *fakeTx) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (f *fakeTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, nil
}
func (f *fakeTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row { return nil }
func (f *fakeTx) Conn() *pgx.Conn                                              { return nil }

func TestStudentImportService(t *testing.T) {
	ctx := context.Background()

	t.Run("all rows new", func(t *testing.T) {
		repo := &fakeImportRepo{
			outcomes: []string{"created", "created", "created"},
		}
		service := NewStudentImportService(repo)

		report, err := service.Import(ctx, 99, []ParsedStudentRow{
			{RowNumber: 2, StudentNumber: "1001", StudentIDExt: "EXT1", SecLec: "L1", SecLab: "B1", FirstName: "A", LastName: "One"},
			{RowNumber: 3, StudentNumber: "1002", StudentIDExt: "EXT2", SecLec: "L1", SecLab: "B1", FirstName: "B", LastName: "Two"},
			{RowNumber: 4, StudentNumber: "1003", StudentIDExt: "EXT3", SecLec: "L1", SecLab: "B1", FirstName: "C", LastName: "Three"},
		})
		if err != nil {
			t.Fatalf("Import returned error: %v", err)
		}
		if report.Created != 3 || report.Updated != 0 || report.Skipped != 0 {
			t.Fatalf("unexpected report counts: %+v", report)
		}
		if len(repo.upsertCalls) != 3 {
			t.Fatalf("expected 3 upsert calls, got %d", len(repo.upsertCalls))
		}
	})

	t.Run("one row already exists", func(t *testing.T) {
		repo := &fakeImportRepo{
			outcomes: []string{"created", "updated", "created"},
		}
		service := NewStudentImportService(repo)

		report, err := service.Import(ctx, 99, []ParsedStudentRow{
			{RowNumber: 2, StudentNumber: "2001", StudentIDExt: "E2001"},
			{RowNumber: 3, StudentNumber: "2002", StudentIDExt: "E2002"},
			{RowNumber: 4, StudentNumber: "2003", StudentIDExt: "E2003"},
		})
		if err != nil {
			t.Fatalf("Import returned error: %v", err)
		}
		if report.Created != 2 || report.Updated != 1 || report.Skipped != 0 {
			t.Fatalf("unexpected report counts: %+v", report)
		}
		if len(repo.upsertCalls) != 3 {
			t.Fatalf("expected 3 upsert calls, got %d", len(repo.upsertCalls))
		}
	})

	t.Run("blank student number is skipped", func(t *testing.T) {
		repo := &fakeImportRepo{
			outcomes: []string{"created", "created"},
		}
		service := NewStudentImportService(repo)

		report, err := service.Import(ctx, 99, []ParsedStudentRow{
			{RowNumber: 2, StudentNumber: "3001", StudentIDExt: "E3001"},
			{RowNumber: 3, StudentNumber: ""},
			{RowNumber: 4, StudentNumber: "3003", StudentIDExt: "E3003"},
		})
		if err != nil {
			t.Fatalf("Import returned error: %v", err)
		}
		if report.Created != 2 || report.Updated != 0 || report.Skipped != 1 {
			t.Fatalf("unexpected report counts: %+v", report)
		}
		if len(repo.upsertCalls) != 2 {
			t.Fatalf("expected 2 upsert calls, got %d", len(repo.upsertCalls))
		}
		if len(report.Errors) != 1 {
			t.Fatalf("expected 1 error, got %+v", report.Errors)
		}
		if report.Errors[0].Row != 3 || report.Errors[0].StudentNumber != "" {
			t.Fatalf("unexpected error record: %+v", report.Errors[0])
		}
		if report.Errors[0].Message != "Student number is required" {
			t.Fatalf("unexpected error message: %q", report.Errors[0].Message)
		}
	})

	t.Run("blank student id is skipped", func(t *testing.T) {
		repo := &fakeImportRepo{
			outcomes: []string{"created", "created"},
		}
		service := NewStudentImportService(repo)

		// student_id_ext is the login username now — a row missing it is skipped
		// (not upserted) even though its student_number is present.
		report, err := service.Import(ctx, 99, []ParsedStudentRow{
			{RowNumber: 2, StudentNumber: "4001", StudentIDExt: "E4001"},
			{RowNumber: 3, StudentNumber: "4002", StudentIDExt: ""},
			{RowNumber: 4, StudentNumber: "4003", StudentIDExt: "E4003"},
		})
		if err != nil {
			t.Fatalf("Import returned error: %v", err)
		}
		if report.Created != 2 || report.Skipped != 1 {
			t.Fatalf("unexpected report counts: %+v", report)
		}
		if len(repo.upsertCalls) != 2 {
			t.Fatalf("expected 2 upsert calls, got %d", len(repo.upsertCalls))
		}
		if len(report.Errors) != 1 || report.Errors[0].Row != 3 {
			t.Fatalf("expected 1 error on row 3, got %+v", report.Errors)
		}
		if report.Errors[0].StudentNumber != "4002" {
			t.Fatalf("error should carry the student_number: %+v", report.Errors[0])
		}
		if report.Errors[0].Message != "Student ID is required" {
			t.Fatalf("unexpected error message: %q", report.Errors[0].Message)
		}
	})

	// CRITICAL 1: a completely clean import (every row succeeds, so Errors and
	// CreatedAccounts are never appended to) must still marshal both fields as
	// `[]`, never `null`. A nil Go slice marshals as JSON null, and
	// BulkImportStudents.vue reads result.errors.length with no guard — null
	// throws a TypeError during render and the whole results table fails to
	// mount, even though the toast reports success.
	//
	// This asserts on the marshalled BYTES, not the Go struct: a struct
	// equality check (e.g. reflect.DeepEqual against []ImportRowError{}) would
	// pass for both nil and empty slices and would never have caught this bug.
	t.Run("a completely clean import marshals errors and created_accounts as [] not null", func(t *testing.T) {
		repo := &fakeImportRepo{outcomes: []string{"created", "created"}}
		service := NewStudentImportService(repo)

		report, err := service.Import(ctx, 99, []ParsedStudentRow{
			{RowNumber: 2, StudentNumber: "5001", StudentIDExt: "E5001"},
			{RowNumber: 3, StudentNumber: "5002", StudentIDExt: "E5002"},
		})
		if err != nil {
			t.Fatalf("Import returned error: %v", err)
		}
		if len(report.Errors) != 0 {
			t.Fatalf("expected zero errors, got %+v", report.Errors)
		}

		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		body := string(raw)
		if strings.Contains(body, `"errors":null`) {
			t.Errorf(`marshalled report has "errors":null, want "errors":[] — body: %s`, body)
		}
		if !strings.Contains(body, `"errors":[]`) {
			t.Errorf(`marshalled report is missing "errors":[] — body: %s`, body)
		}
		// created_accounts is populated here (both rows created), so also prove
		// the CreatedAccounts field itself is never left nil by construction:
		// re-marshal a report with zero created rows (all updates) to exercise
		// the empty-but-non-nil path for that field specifically.
		repo2 := &fakeImportRepo{outcomes: []string{"updated", "updated"}}
		service2 := NewStudentImportService(repo2)
		report2, err := service2.Import(ctx, 99, []ParsedStudentRow{
			{RowNumber: 2, StudentNumber: "6001", StudentIDExt: "E6001"},
			{RowNumber: 3, StudentNumber: "6002", StudentIDExt: "E6002"},
		})
		if err != nil {
			t.Fatalf("Import returned error: %v", err)
		}
		raw2, err := json.Marshal(report2)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		body2 := string(raw2)
		if strings.Contains(body2, `"created_accounts":null`) {
			t.Errorf(`marshalled report has "created_accounts":null, want "created_accounts":[] — body: %s`, body2)
		}
		if !strings.Contains(body2, `"created_accounts":[]`) {
			t.Errorf(`marshalled report is missing "created_accounts":[] — body: %s`, body2)
		}
	})
}
