package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// execRecordingTx wraps fakeTx to record every SQL statement passed to Exec,
// so a test can prove which of ROLLBACK TO SAVEPOINT / RELEASE SAVEPOINT
// actually ran for a row, not just what the resulting report claims.
type execRecordingTx struct {
	fakeTx
	execCalls []string
}

func (t *execRecordingTx) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	t.execCalls = append(t.execCalls, sql)
	return pgconn.CommandTag{}, nil
}

// execRecordingRotatingRepo behaves exactly like fakeRotatingRepo but hands
// ImportRotating an execRecordingTx instead of a plain fakeTx, and keeps a
// reference to it so the test can inspect execCalls after the run.
type execRecordingRotatingRepo struct {
	fakeRotatingRepo
	tx *execRecordingTx
}

func (f *execRecordingRotatingRepo) WithTransaction(ctx context.Context, fn func(pgx.Tx) error) error {
	f.tx = &execRecordingTx{}
	return fn(f.tx)
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

	// IMPORTANT 2: UpsertStudentRotating already applies the rotation (new
	// hash + bumped token_version) and RELEASEs its savepoint before this
	// switch used to run, so an outcome string outside "created"/"updated"
	// used to be counted Skipped with the rotation already durably committed
	// and no ImportRowError naming the row -- a student locked out with
	// nothing pointing at them. This is unreachable via the real repository
	// today (it always returns "created"/"updated"), but guards the exact
	// failure mode that appears the moment those literals ever drift.
	t.Run("unexpected outcome rolls back the savepoint instead of releasing it, and names the row", func(t *testing.T) {
		repo := &execRecordingRotatingRepo{fakeRotatingRepo: fakeRotatingRepo{outcomes: []string{"bogus"}}}
		service := NewStudentImportService(repo)

		report, err := service.ImportRotating(ctx, 7, []ParsedStudentRow{
			{RowNumber: 1, StudentNumber: "N1", StudentIDExt: "s1", Email: "s1@example.com"},
		})
		if err != nil {
			t.Fatalf("ImportRotating: %v", err)
		}
		if report.Skipped != 1 {
			t.Fatalf("Skipped = %d, want 1", report.Skipped)
		}
		if len(report.Accounts) != 0 {
			t.Fatalf("Accounts = %+v, want none: an unexpected outcome must not be reported as a rotated account", report.Accounts)
		}
		if len(report.Errors) != 1 || report.Errors[0].Row != 1 {
			t.Fatalf("Errors = %+v, want one entry naming row 1", report.Errors)
		}

		rolledBack, released := false, false
		for _, sql := range repo.tx.execCalls {
			if strings.HasPrefix(sql, "ROLLBACK TO SAVEPOINT") {
				rolledBack = true
			}
			if strings.HasPrefix(sql, "RELEASE SAVEPOINT") {
				released = true
			}
		}
		if !rolledBack {
			t.Errorf("no ROLLBACK TO SAVEPOINT was executed; exec calls: %v", repo.tx.execCalls)
		}
		if released {
			t.Errorf("RELEASE SAVEPOINT was executed, which commits the already-applied rotation; exec calls: %v", repo.tx.execCalls)
		}
	})

	// CRITICAL 1 (rotating path): same nil-vs-empty-slice bug as ImportReport,
	// asserted on the marshalled bytes so a struct-equality check (which would
	// pass for both nil and []) can't paper over it.
	t.Run("a completely clean rotating import marshals errors as [] not null", func(t *testing.T) {
		repo := &fakeRotatingRepo{outcomes: []string{"created", "updated"}}
		service := NewStudentImportService(repo)

		report, err := service.ImportRotating(ctx, 7, []ParsedStudentRow{
			{RowNumber: 1, StudentNumber: "N1", StudentIDExt: "s1", Email: "s1@example.com"},
			{RowNumber: 2, StudentNumber: "N2", StudentIDExt: "s2", Email: "s2@example.com"},
		})
		if err != nil {
			t.Fatalf("ImportRotating: %v", err)
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
	})

	// CRITICAL 1 (rotating path), symmetric case: when every row is skipped,
	// Accounts must marshal as [] not null. Before the fix this null would
	// throw inside BulkImportStudentsCredentials.vue's hasHeldResults
	// computed, and therefore inside submitImport and the beforeunload watch.
	t.Run("an all-skipped rotating import marshals accounts as [] not null", func(t *testing.T) {
		repo := &fakeRotatingRepo{}
		service := NewStudentImportService(repo)

		report, err := service.ImportRotating(ctx, 7, []ParsedStudentRow{
			{RowNumber: 1, StudentNumber: "", StudentIDExt: "s1"},
		})
		if err != nil {
			t.Fatalf("ImportRotating: %v", err)
		}
		if len(report.Accounts) != 0 {
			t.Fatalf("expected zero accounts, got %+v", report.Accounts)
		}
		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		body := string(raw)
		if strings.Contains(body, `"accounts":null`) {
			t.Errorf(`marshalled report has "accounts":null, want "accounts":[] — body: %s`, body)
		}
		if !strings.Contains(body, `"accounts":[]`) {
			t.Errorf(`marshalled report is missing "accounts":[] — body: %s`, body)
		}
	})
}
