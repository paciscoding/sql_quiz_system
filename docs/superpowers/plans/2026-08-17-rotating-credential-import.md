# Rotating Credential Import Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a second, admin-only bulk roster import that rotates and emails a fresh password for every row in the file, with a live email-progress bubble in AdminView.

**Architecture:** A new repository upsert rotates `password_hash` and bumps `token_version` on conflict; a new `StudentImportService.ImportRotating` reuses the existing savepoint-per-row loop and returns a password for created *and* updated rows. After commit, the controller logs one `app.password_events` row per account (without the admin change-notice fan-out), registers an in-memory email job, and hands delivery to a single background goroutine. The frontend polls that job's status endpoint and renders a floating progress bubble.

**Tech Stack:** Go 1.x + gin + pgx v5 + bcrypt (backend); Vue 3 `<script setup>` + TypeScript + vitest + `@vue/test-utils` (frontend).

**Spec:** `docs/superpowers/specs/2026-08-17-rotating-credential-import-design.md`

## Global Constraints

- **Backend tests MUST run with `-p 1`.** The password/import suites are DB-global; a plain `go test ./...` on a shared DB produces false regressions. Always: `go test -p 1 ./internal/...`.
- **Never call `AuthService.logPasswordEvent` on any bulk path.** It fires `SendChangeNotice` to `ADMIN_EMAIL` per event, turning a 200-row file into 200 admin emails (documented at `internal/services/auth_service.go:476-483`).
- **Do not modify `UpsertStudentByNumber`, `StudentImportService.Import`, `BulkImportStudents.vue`, or `POST /api/v1/lecturer/students/import`.** The existing importer must keep its exact current behaviour and permissions.
- New route paths, verbatim:
  - `POST /api/v1/students/import-credentials`
  - `GET /api/v1/students/import-credentials/:job_id/status`
- Both new routes are `controllers.RoleMiddleware(models.RoleAdmin)` — admin only.
- Confirmation phrase, verbatim: `ROTATE`. Validated server-side on every request; a missing or mismatched value is `400 {"error":"confirmation_required"}`.
- Password-event sources: `models.PasswordEventCreate` for `created` rows, `models.PasswordEventAdminReset` for `updated` rows.
- **Deviation from the spec, deliberate:** the spec describes the `ROTATE` phrase being typed into a confirm dialog. `services/confirm_service.ts`'s `ConfirmOptions` (`title`, `body`, `confirmLabel`, `cancelLabel`, `danger`) has no text-input field. The frontend therefore gates on an inline typed input that must equal `ROTATE`, exactly as `BulkPasswordReset.vue` gates on `RESET`. Task 9 records this in the spec.
- Commit after every task. Conventional Commits (`feat:`, `test:`, `docs:`).

---

### Task 1: Repository — rotating upsert

**Files:**
- Modify: `backend/internal/repository/postgres_users.go` (add after `UpsertStudentByNumber`, which ends at line 337)
- Test: `backend/internal/repository/postgres_student_rotating_import_test.go` (create)

**Interfaces:**
- Consumes: `models.StudentImportRow`, `repository.PostgresRepository.WithTransaction`, existing test helpers `openTestRepo(t)` and `uniqueUsername(prefix)` (both already in the `repository` package's test files).
- Produces:
  ```go
  func (r *PostgresRepository) UpsertStudentRotating(
      ctx context.Context, tx pgx.Tx, in models.StudentImportRow, creatorID int,
  ) (outcome string, userID int, err error)
  ```
  `outcome` is `"created"` or `"updated"`.

- [ ] **Step 1: Write the failing test**

Create `backend/internal/repository/postgres_student_rotating_import_test.go`:

```go
package repository

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// TestUpsertStudentRotating_ConflictRotatesCredential is the core guarantee of
// the rotating importer: re-importing an existing student REPLACES the password
// hash and bumps token_version in the same statement, so the new credential and
// the session eviction can never diverge.
func TestUpsertStudentRotating_ConflictRotatesCredential(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	creatorID, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("rot-importer"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser (creator): %v", err)
	}

	ext := uniqueUsername("rot-ext")
	first := models.StudentImportRow{
		StudentNumber: uniqueUsername("RN"),
		StudentIDExt:  ext,
		FirstName:     "Alice",
		LastName:      "Before",
		PasswordHash:  "hash-one",
	}
	second := first
	second.LastName = "After"
	second.PasswordHash = "hash-two"

	var id1, id2 int
	var outcome1, outcome2 string
	if err := repo.WithTransaction(ctx, func(tx pgx.Tx) error {
		var txErr error
		outcome1, id1, txErr = repo.UpsertStudentRotating(ctx, tx, first, creatorID)
		return txErr
	}); err != nil {
		t.Fatalf("UpsertStudentRotating (insert): %v", err)
	}
	if outcome1 != "created" {
		t.Fatalf("first upsert outcome = %q, want \"created\"", outcome1)
	}

	versionBefore, err := repo.GetTokenVersion(ctx, id1)
	if err != nil {
		t.Fatalf("GetTokenVersion (before): %v", err)
	}

	if err := repo.WithTransaction(ctx, func(tx pgx.Tx) error {
		var txErr error
		outcome2, id2, txErr = repo.UpsertStudentRotating(ctx, tx, second, creatorID)
		return txErr
	}); err != nil {
		t.Fatalf("UpsertStudentRotating (conflict): %v", err)
	}
	if outcome2 != "updated" {
		t.Fatalf("second upsert outcome = %q, want \"updated\"", outcome2)
	}
	if id1 != id2 {
		t.Fatalf("conflict created a new row: id1=%d id2=%d", id1, id2)
	}

	var hash, lastName string
	if err := repo.pool.QueryRow(ctx,
		`SELECT password_hash, last_name FROM app.system_users WHERE id = $1`, id2,
	).Scan(&hash, &lastName); err != nil {
		t.Fatalf("read back row: %v", err)
	}
	if hash != "hash-two" {
		t.Errorf("password_hash = %q, want \"hash-two\" (credential did not rotate)", hash)
	}
	if lastName != "After" {
		t.Errorf("last_name = %q, want \"After\"", lastName)
	}

	versionAfter, err := repo.GetTokenVersion(ctx, id2)
	if err != nil {
		t.Fatalf("GetTokenVersion (after): %v", err)
	}
	if versionAfter != versionBefore+1 {
		t.Errorf("token_version = %d, want %d (sessions were not evicted)", versionAfter, versionBefore+1)
	}
}

// TestUpsertStudentByNumber_StillPreservesPassword guards the OLD importer:
// adding the rotating variant must not change the non-rotating one.
func TestUpsertStudentByNumber_StillPreservesPassword(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	creatorID, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("plain-importer"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser (creator): %v", err)
	}

	ext := uniqueUsername("plain-ext")
	row := models.StudentImportRow{
		StudentNumber: uniqueUsername("RN"),
		StudentIDExt:  ext,
		FirstName:     "Bob",
		PasswordHash:  "original-hash",
	}
	var id int
	if err := repo.WithTransaction(ctx, func(tx pgx.Tx) error {
		var txErr error
		_, id, txErr = repo.UpsertStudentByNumber(ctx, tx, row, creatorID)
		return txErr
	}); err != nil {
		t.Fatalf("UpsertStudentByNumber (insert): %v", err)
	}

	row.PasswordHash = "replacement-hash"
	row.FirstName = "Bobby"
	if err := repo.WithTransaction(ctx, func(tx pgx.Tx) error {
		_, _, txErr := repo.UpsertStudentByNumber(ctx, tx, row, creatorID)
		return txErr
	}); err != nil {
		t.Fatalf("UpsertStudentByNumber (conflict): %v", err)
	}

	var hash, firstName string
	if err := repo.pool.QueryRow(ctx,
		`SELECT password_hash, first_name FROM app.system_users WHERE id = $1`, id,
	).Scan(&hash, &firstName); err != nil {
		t.Fatalf("read back row: %v", err)
	}
	if hash != "original-hash" {
		t.Errorf("password_hash = %q, want \"original-hash\": the plain importer must NOT rotate credentials", hash)
	}
	if firstName != "Bobby" {
		t.Errorf("first_name = %q, want \"Bobby\": attributes should still refresh", firstName)
	}
}
```

If `repo.pool` is not the field name on `PostgresRepository`, read the top of `backend/internal/repository/postgres.go` and use the actual pool field; every other assertion is unchanged.

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd backend && go test -p 1 ./internal/repository/ -run TestUpsertStudentRotating -v
```

Expected: FAIL — `repo.UpsertStudentRotating undefined (type *PostgresRepository has no field or method UpsertStudentRotating)`.

- [ ] **Step 3: Write the implementation**

Append to `backend/internal/repository/postgres_users.go`, directly after `UpsertStudentByNumber`:

```go
// UpsertStudentRotating is UpsertStudentByNumber plus credential rotation: on
// conflict it ALSO replaces password_hash and bumps token_version, so a
// re-imported student receives the freshly generated password and every
// existing session is evicted in the same statement. It backs the admin-only
// rotating credential import; the plain importer must keep using
// UpsertStudentByNumber, which leaves an existing password alone.
func (r *PostgresRepository) UpsertStudentRotating(ctx context.Context, tx pgx.Tx, in models.StudentImportRow, creatorID int) (outcome string, userID int, err error) {
	query := `
		INSERT INTO app.system_users (
			username, password_hash, role, created_by,
			student_number, student_id_ext, sec_lec, sec_lab, first_name, last_name, email
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (student_id_ext) WHERE student_id_ext IS NOT NULL DO UPDATE SET
			username = EXCLUDED.username,
			student_number = EXCLUDED.student_number,
			sec_lec = EXCLUDED.sec_lec,
			sec_lab = EXCLUDED.sec_lab,
			first_name = EXCLUDED.first_name,
			last_name = EXCLUDED.last_name,
			email = EXCLUDED.email,
			password_hash = EXCLUDED.password_hash,
			token_version = app.system_users.token_version + 1
		RETURNING id, (xmax = 0) AS inserted`

	// email is stored NULL when empty so the column stays genuinely optional.
	var emailArg *string
	if in.Email != "" {
		emailArg = &in.Email
	}

	var inserted bool
	err = tx.QueryRow(ctx, query,
		in.StudentIDExt,
		in.PasswordHash,
		string(models.RoleStudent),
		creatorID,
		in.StudentNumber,
		in.StudentIDExt,
		in.SecLec,
		in.SecLab,
		in.FirstName,
		in.LastName,
		emailArg,
	).Scan(&userID, &inserted)
	if err != nil {
		return "", 0, err
	}
	if inserted {
		return "created", userID, nil
	}
	return "updated", userID, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd backend && go test -p 1 ./internal/repository/ -run 'TestUpsertStudentRotating|TestUpsertStudentByNumber' -v
```

Expected: PASS for both new tests and every pre-existing `TestUpsertStudent*`.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/repository/postgres_users.go backend/internal/repository/postgres_student_rotating_import_test.go
git commit -m "feat(repo): add UpsertStudentRotating that rotates password and evicts sessions"
```

---

### Task 2: Service — `ImportRotating`

**Files:**
- Modify: `backend/internal/services/student_import_service.go`
- Test: `backend/internal/services/student_import_rotating_test.go` (create)

**Interfaces:**
- Consumes: `repository.PostgresRepository.UpsertStudentRotating` (Task 1), existing `ParsedStudentRow`, `GeneratePassword()`, `ImportRowError`.
- Produces:
  ```go
  type RotatedAccount struct {
      UserID        int    `json:"-"`
      Row           int    `json:"row"`
      StudentNumber string `json:"student_number"`
      StudentIDExt  string `json:"student_id_ext"`
      Email         string `json:"email"`
      Password      string `json:"password"`
      Outcome       string `json:"outcome"`
      EmailStatus   string `json:"email_status"`
  }

  type RotatingImportReport struct {
      Total    int              `json:"total"`
      Created  int              `json:"created"`
      Updated  int              `json:"updated"`
      Skipped  int              `json:"skipped"`
      Errors   []ImportRowError `json:"errors"`
      Accounts []RotatedAccount `json:"accounts"`
      JobID    string           `json:"job_id"`
  }

  func (s *StudentImportService) ImportRotating(
      ctx context.Context, creatorID int, rows []ParsedStudentRow,
  ) (RotatingImportReport, error)
  ```
  `JobID` is left `""` by the service; the controller fills it in (Task 5).

- [ ] **Step 1: Write the failing test**

Create `backend/internal/services/student_import_rotating_test.go`. It reuses `fakeTx` from `student_import_service_test.go` (same package) and adds a fake that implements the widened `ImportRepo`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd backend && go test -p 1 ./internal/services/ -run TestImportRotating -v
```

Expected: FAIL — `service.ImportRotating undefined` and `RotatingImportReport` / `RotatedAccount` undefined.

- [ ] **Step 3: Write the implementation**

In `backend/internal/services/student_import_service.go`, widen the `ImportRepo` interface (currently lines 15-18) to:

```go
// ImportRepo defines the repository subset required by StudentImportService.
// *repository.PostgresRepository satisfies this interface.
type ImportRepo interface {
	UpsertStudentByNumber(ctx context.Context, tx pgx.Tx, in models.StudentImportRow, creatorID int) (string, int, error)
	UpsertStudentRotating(ctx context.Context, tx pgx.Tx, in models.StudentImportRow, creatorID int) (string, int, error)
	WithTransaction(ctx context.Context, fn func(pgx.Tx) error) error
}
```

Then append to the same file:

```go
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
	Outcome       string `json:"outcome"`      // "created" | "updated"
	EmailStatus   string `json:"email_status"` // "queued" | "no_email"
}

// RotatingImportReport summarizes a rotating import. JobID identifies the
// background email job and is filled in by the controller, not the service.
type RotatingImportReport struct {
	Total    int              `json:"total"`
	Created  int              `json:"created"`
	Updated  int              `json:"updated"`
	Skipped  int              `json:"skipped"`
	Errors   []ImportRowError `json:"errors"`
	Accounts []RotatedAccount `json:"accounts"`
	JobID    string           `json:"job_id"`
}

// ImportRotating is the admin-only counterpart to Import: it rotates the
// credential of EVERY row in the roster — new and returning students alike —
// and reports the plaintext for each so it can be emailed and shown once. The
// transaction/savepoint structure is deliberately identical to Import's, so a
// single bad row is isolated instead of failing the file.
func (s *StudentImportService) ImportRotating(ctx context.Context, creatorID int, rows []ParsedStudentRow) (RotatingImportReport, error) {
	report := RotatingImportReport{Total: len(rows)}

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

			if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT "+savepointName); err != nil {
				return fmt.Errorf("failed to release savepoint: %w", err)
			}

			switch outcome {
			case "created":
				report.Created++
			case "updated":
				report.Updated++
			default:
				report.Skipped++
				continue
			}

			// Decided synchronously, before any send is attempted, so the value is
			// accurate even though delivery is asynchronous.
			emailStatus := "no_email"
			if row.Email != "" {
				emailStatus = "queued"
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
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd backend && go test -p 1 ./internal/services/ -run 'TestImportRotating|TestStudentImportService' -v
```

Expected: PASS. The pre-existing `TestStudentImportService` must still pass — if it fails to compile because `fakeImportRepo` no longer satisfies `ImportRepo`, add this method to `fakeImportRepo` in `student_import_service_test.go`:

```go
func (f *fakeImportRepo) UpsertStudentRotating(ctx context.Context, tx pgx.Tx, in models.StudentImportRow, creatorID int) (string, int, error) {
	return f.UpsertStudentByNumber(ctx, tx, in, creatorID)
}
```

- [ ] **Step 5: Commit**

```bash
git add backend/internal/services/student_import_service.go backend/internal/services/student_import_rotating_test.go backend/internal/services/student_import_service_test.go
git commit -m "feat(import): add ImportRotating that rotates every roster row's password"
```

---

### Task 3: Email job registry

**Files:**
- Create: `backend/internal/services/email_job_registry.go`
- Test: `backend/internal/services/email_job_registry_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  ```go
  type EmailJob struct {
      ID       string   `json:"job_id"`
      OwnerID  int      `json:"-"`
      Total    int      `json:"total"`
      Sent     int      `json:"sent"`
      Failed   int      `json:"failed"`
      Done     bool     `json:"done"`
      Failures []string `json:"failures"`
  }

  func NewEmailJobRegistry() *EmailJobRegistry
  func (r *EmailJobRegistry) Register(ownerID, total int) *EmailJob
  func (r *EmailJobRegistry) MarkSent(id string)
  func (r *EmailJobRegistry) MarkFailed(id, studentIDExt string)
  func (r *EmailJobRegistry) Finish(id string)
  func (r *EmailJobRegistry) Get(id string, ownerID int) (EmailJob, bool)
  ```
  `Get` returns a copy, and returns `false` when the id is unknown **or** the owner does not match.

- [ ] **Step 1: Write the failing test**

Create `backend/internal/services/email_job_registry_test.go`:

```go
package services

import (
	"sync"
	"testing"
	"time"
)

func TestEmailJobRegistry_CountsAndOwnership(t *testing.T) {
	reg := NewEmailJobRegistry()
	job := reg.Register(42, 3)

	if job.ID == "" {
		t.Fatal("Register returned an empty job id")
	}
	if job.Total != 3 || job.Sent != 0 || job.Done {
		t.Fatalf("fresh job = %+v, want total 3, sent 0, not done", *job)
	}

	reg.MarkSent(job.ID)
	reg.MarkFailed(job.ID, "s0002")
	reg.MarkSent(job.ID)
	reg.Finish(job.ID)

	got, ok := reg.Get(job.ID, 42)
	if !ok {
		t.Fatal("Get by the owning admin returned not-found")
	}
	if got.Sent != 2 || got.Failed != 1 || !got.Done {
		t.Errorf("job = sent:%d failed:%d done:%v, want 2/1/true", got.Sent, got.Failed, got.Done)
	}
	if len(got.Failures) != 1 || got.Failures[0] != "s0002" {
		t.Errorf("Failures = %v, want [s0002]", got.Failures)
	}

	if _, ok := reg.Get(job.ID, 43); ok {
		t.Error("a different admin could read the job: ownership is not enforced")
	}
	if _, ok := reg.Get("no-such-job", 42); ok {
		t.Error("unknown job id returned found")
	}
}

// A returned job must be a snapshot: mutating it must not corrupt the registry.
func TestEmailJobRegistry_GetReturnsCopy(t *testing.T) {
	reg := NewEmailJobRegistry()
	job := reg.Register(1, 1)
	reg.MarkSent(job.ID)

	got, _ := reg.Get(job.ID, 1)
	got.Sent = 999

	again, _ := reg.Get(job.ID, 1)
	if again.Sent != 1 {
		t.Errorf("Sent = %d, want 1: Get must return a copy", again.Sent)
	}
}

// Guards the concurrent read (HTTP poll) against the concurrent write (worker).
// Run with -race.
func TestEmailJobRegistry_ConcurrentAccess(t *testing.T) {
	reg := NewEmailJobRegistry()
	job := reg.Register(9, 100)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			reg.MarkSent(job.ID)
		}
		reg.Finish(job.ID)
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			reg.Get(job.ID, 9)
		}
	}()
	wg.Wait()

	got, _ := reg.Get(job.ID, 9)
	if got.Sent != 100 {
		t.Errorf("Sent = %d, want 100", got.Sent)
	}
}

func TestEmailJobRegistry_EvictsExpiredOnRegister(t *testing.T) {
	reg := NewEmailJobRegistry()
	old := reg.Register(1, 1)
	reg.Finish(old.ID)

	// Age the finished job past the retention window without sleeping.
	reg.mu.Lock()
	reg.jobs[old.ID].finishedAt = time.Now().Add(-2 * emailJobRetention)
	reg.mu.Unlock()

	reg.Register(1, 1) // triggers the sweep

	if _, ok := reg.Get(old.ID, 1); ok {
		t.Error("an expired finished job survived the sweep")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd backend && go test -p 1 ./internal/services/ -run TestEmailJobRegistry -v
```

Expected: FAIL — `undefined: NewEmailJobRegistry`.

- [ ] **Step 3: Write the implementation**

Create `backend/internal/services/email_job_registry.go`:

```go
package services

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// emailJobRetention is how long a FINISHED job stays readable, so a reloaded
// admin page can still show the outcome of a send that ended while it was away.
const emailJobRetention = 30 * time.Minute

// emailJobCap bounds the registry. Past this, the oldest finished job is
// evicted first; this is a runaway backstop, not an operational limit.
const emailJobCap = 100

// maxTrackedFailures caps the per-job failure list so a pathological run cannot
// grow the entry without bound. Beyond it the counter still rises.
const maxTrackedFailures = 50

// EmailJob is the progress snapshot of one background credential-email run.
// Sent+Failed approaches Total; Done marks the worker as finished.
type EmailJob struct {
	ID       string   `json:"job_id"`
	OwnerID  int      `json:"-"`
	Total    int      `json:"total"`
	Sent     int      `json:"sent"`
	Failed   int      `json:"failed"`
	Done     bool     `json:"done"`
	Failures []string `json:"failures"`

	finishedAt time.Time
}

// EmailJobRegistry is an in-memory, mutex-guarded store of email-job progress.
// It exists ONLY to back the admin progress bubble: nothing about the import's
// correctness depends on it, and every password it reports on is also present
// in the HTTP response the admin already holds. Single-process by design — the
// deployment runs one backend, and a lost job degrades to a missing bubble.
type EmailJobRegistry struct {
	mu   sync.Mutex
	jobs map[string]*EmailJob
}

func NewEmailJobRegistry() *EmailJobRegistry {
	return &EmailJobRegistry{jobs: make(map[string]*EmailJob)}
}

// Register mints a job with a 128-bit crypto/rand id — unguessable and never
// sequential, since the id is the only handle on another admin's progress.
func (r *EmailJobRegistry) Register(ownerID, total int) *EmailJob {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failing is fatal for id secrecy; degrade to no tracking
		// rather than to a predictable id.
		return &EmailJob{OwnerID: ownerID, Total: total}
	}
	job := &EmailJob{ID: hex.EncodeToString(buf), OwnerID: ownerID, Total: total}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
	r.jobs[job.ID] = job
	return job
}

// sweepLocked drops expired finished jobs, then enforces the cap by dropping
// the oldest finished job. Callers must hold r.mu.
func (r *EmailJobRegistry) sweepLocked() {
	cutoff := time.Now().Add(-emailJobRetention)
	for id, j := range r.jobs {
		if j.Done && j.finishedAt.Before(cutoff) {
			delete(r.jobs, id)
		}
	}
	for len(r.jobs) >= emailJobCap {
		oldestID := ""
		var oldest time.Time
		for id, j := range r.jobs {
			if !j.Done {
				continue
			}
			if oldestID == "" || j.finishedAt.Before(oldest) {
				oldestID, oldest = id, j.finishedAt
			}
		}
		if oldestID == "" {
			return // every job is still running; leave them alone
		}
		delete(r.jobs, oldestID)
	}
}

func (r *EmailJobRegistry) MarkSent(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok {
		j.Sent++
	}
}

func (r *EmailJobRegistry) MarkFailed(id, studentIDExt string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok {
		j.Failed++
		if len(j.Failures) < maxTrackedFailures {
			j.Failures = append(j.Failures, studentIDExt)
		}
	}
}

func (r *EmailJobRegistry) Finish(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok {
		j.Done = true
		j.finishedAt = time.Now()
	}
}

// Get returns a COPY of the job. An id belonging to another admin is
// indistinguishable from an unknown id, so the endpoint cannot be used to probe
// for other admins' activity.
func (r *EmailJobRegistry) Get(id string, ownerID int) (EmailJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok || j.OwnerID != ownerID {
		return EmailJob{}, false
	}
	copied := *j
	copied.Failures = append([]string(nil), j.Failures...)
	return copied, true
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd backend && go test -p 1 -race ./internal/services/ -run TestEmailJobRegistry -v
```

Expected: PASS, no race reports.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/services/email_job_registry.go backend/internal/services/email_job_registry_test.go
git commit -m "feat(services): add in-memory email job registry for send progress"
```

---

### Task 4: `AuthService.LogBulkCredentials`

**Files:**
- Modify: `backend/internal/services/auth_service.go` (add after `LogStudentCreate`, which ends around line 197)
- Test: `backend/internal/services/log_bulk_credentials_test.go` (create)

**Interfaces:**
- Consumes: `s.cipher.Encrypt`, `s.repo.InsertPasswordEvent`, `models.PasswordEvent`, `models.PasswordEventCreate`, `models.PasswordEventAdminReset`.
- Produces:
  ```go
  type BulkCredentialEvent struct {
      UserID int
      Source string // models.PasswordEventCreate | models.PasswordEventAdminReset
      Plaintext string
  }

  func (s *AuthService) LogBulkCredentials(ctx context.Context, actor models.User, events []BulkCredentialEvent)
  ```

- [ ] **Step 1: Write the failing test**

Create `backend/internal/services/log_bulk_credentials_test.go`. It reuses `spyEmailSender` from `password_flows_integration_test.go` (same package) — read that file's definition first and, if it does not record `SendChangeNotice` calls, use the local spy below instead:

```go
package services

import (
	"context"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// noticeSpy records every SendChangeNotice so the test can prove the bulk path
// never fans out an admin email per row.
type noticeSpy struct {
	credentials int
	notices     int
}

func (s *noticeSpy) SendCredential(toEmail, username, password string) error {
	s.credentials++
	return nil
}
func (s *noticeSpy) SendResetCode(toEmail, username, code string) error { return nil }
func (s *noticeSpy) SendChangeNotice(adminEmail string, ev models.PasswordEvent) error {
	s.notices++
	return nil
}

// LogBulkCredentials must persist one password_events row per entry and send
// NOTHING — the trap documented at auth_service.go:476-483 is that
// logPasswordEvent emails ADMIN_EMAIL for every event, which would turn a
// 200-row import into 200 admin emails.
func TestLogBulkCredentials_PersistsEventsAndSendsNoEmail(t *testing.T) {
	repo := newTestAuthRepo(t)
	spy := &noticeSpy{}
	svc := newTestAuthService(t, repo, spy)
	ctx := context.Background()

	actor := createTestAdmin(t, repo)
	s1 := createTestStudent(t, repo, "bulkcred-1")
	s2 := createTestStudent(t, repo, "bulkcred-2")

	svc.LogBulkCredentials(ctx, actor, []BulkCredentialEvent{
		{UserID: s1, Source: models.PasswordEventCreate, Plaintext: "pw-one"},
		{UserID: s2, Source: models.PasswordEventAdminReset, Plaintext: "pw-two"},
	})

	if spy.notices != 0 {
		t.Errorf("SendChangeNotice called %d times, want 0: the bulk path must never fan out admin email", spy.notices)
	}
	if spy.credentials != 0 {
		t.Errorf("SendCredential called %d times, want 0: delivery is the worker's job, not the logger's", spy.credentials)
	}

	events, err := repo.GetPasswordEvents(ctx, s1)
	if err != nil {
		t.Fatalf("GetPasswordEvents: %v", err)
	}
	if len(events) != 1 || events[0].Source != models.PasswordEventCreate {
		t.Errorf("student 1 events = %+v, want exactly one %q event", events, models.PasswordEventCreate)
	}
	events, err = repo.GetPasswordEvents(ctx, s2)
	if err != nil {
		t.Fatalf("GetPasswordEvents: %v", err)
	}
	if len(events) != 1 || events[0].Source != models.PasswordEventAdminReset {
		t.Errorf("student 2 events = %+v, want exactly one %q event", events, models.PasswordEventAdminReset)
	}
}
```

Before running, open `backend/internal/services/password_flows_integration_test.go` and reuse its existing helpers for building a real-DB `AuthService`, creating an admin, and creating a student. Substitute their real names for `newTestAuthRepo`, `newTestAuthService`, `createTestAdmin`, `createTestStudent`, and for the password-event read (`repo.GetPasswordEvents` or whatever that file uses). Do not invent helpers — that file already builds an `AuthService` against a real DB with a spy sender, which is exactly this shape.

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd backend && go test -p 1 ./internal/services/ -run TestLogBulkCredentials -v
```

Expected: FAIL — `svc.LogBulkCredentials undefined` / `undefined: BulkCredentialEvent`.

- [ ] **Step 3: Write the implementation**

Append to `backend/internal/services/auth_service.go`, right after `LogStudentCreate`:

```go
// BulkCredentialEvent is one password-event to record for a bulk credential
// operation. Source is models.PasswordEventCreate for a newly inserted student
// and models.PasswordEventAdminReset for a rotated existing one.
type BulkCredentialEvent struct {
	UserID    int
	Source    string
	Plaintext string
}

// LogBulkCredentials records one app.password_events row per entry and sends
// NO email of any kind.
//
// It deliberately does NOT call logPasswordEvent: that helper invokes
// SendChangeNotice for EVERY event whenever ADMIN_EMAIL is configured, which
// would turn a 200-row roster import into 200 emails to the admin — the same
// trap BulkResetPasswords documents. Credential delivery to the students is the
// background email worker's job, not this function's.
//
// Best-effort throughout: a failure to encrypt or insert is logged and skipped,
// never returned, because the passwords are already committed and shown to the
// admin.
func (s *AuthService) LogBulkCredentials(ctx context.Context, actor models.User, events []BulkCredentialEvent) {
	if s.cipher == nil {
		log.Printf("auth: password-event logging disabled (no key); %d bulk credential events not recorded", len(events))
		return
	}
	actorID := int(actor.ID)
	for _, ev := range events {
		ct, nonce, err := s.cipher.Encrypt(ev.Plaintext)
		if err != nil {
			log.Printf("auth: encrypt bulk credential event failed (user_id=%d): %v", ev.UserID, err)
			continue
		}
		if _, err := s.repo.InsertPasswordEvent(ctx, models.PasswordEvent{
			UserID:         ev.UserID,
			ChangedBy:      &actorID,
			Source:         ev.Source,
			NewPasswordEnc: ct,
			Nonce:          nonce,
		}); err != nil {
			log.Printf("auth: insert bulk credential event failed (user_id=%d): %v", ev.UserID, err)
		}
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
cd backend && go test -p 1 ./internal/services/ -run TestLogBulkCredentials -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/services/auth_service.go backend/internal/services/log_bulk_credentials_test.go
git commit -m "feat(auth): add LogBulkCredentials with no admin change-notice fan-out"
```

---

### Task 5: Controller + routes

**Files:**
- Modify: `backend/internal/controllers/lecturer_controller.go` (add handlers after `ImportStudents`, which ends at line 742; add a registry setter next to `SetStudentImportService` at line 79)
- Modify: `backend/cmd/main.go` (registry construction near line 149; routes next to `staff.POST("/students", ...)` at line 317)
- Test: `backend/internal/controllers/import_credentials_http_test.go` (create)

**Interfaces:**
- Consumes: `services.StudentImportService.ImportRotating` (Task 2), `services.EmailJobRegistry` (Task 3), `services.AuthService.LogBulkCredentials` + `BulkCredentialEvent` (Task 4), `services.ParseRoster`.
- Produces:
  ```go
  func (ctrl *LecturerController) SetEmailJobRegistry(reg *services.EmailJobRegistry)
  func (ctrl *LecturerController) ImportStudentsWithCredentials(c *gin.Context)
  func (ctrl *LecturerController) ImportCredentialsJobStatus(c *gin.Context)
  ```

- [ ] **Step 1: Write the failing test**

Create `backend/internal/controllers/import_credentials_http_test.go`. Model the engine/JWT bootstrap on `backend/internal/controllers/bulk_reset_http_test.go` — read it first and reuse its helpers for building a gin engine with `AuthMiddleware`, minting an admin/lecturer token, and its `bulkRecordingEmailSender`. The assertions below are what matters:

```go
package controllers

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

// multipartRoster builds a multipart body with a CSV roster and a confirm field.
func multipartRoster(t *testing.T, csv, confirm string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	part, err := w.CreateFormFile("file", "roster.csv")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte(csv)); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	if confirm != "" {
		if err := w.WriteField("confirm", confirm); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return body, w.FormDataContentType()
}

const testRosterCSV = "Student Number,Student_id,First Name,Last Name,Email\n" +
	"N001,cred-s1,Ann,One,ann@example.com\n" +
	"N002,cred-s2,Bob,Two,\n"

// A lecturer must not reach the rotating importer: it mass-rotates credentials.
func TestImportCredentials_LecturerForbidden(t *testing.T) {
	env := newImportCredentialsTestEnv(t)
	body, contentType := multipartRoster(t, testRosterCSV, "ROTATE")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/students/import-credentials", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+env.lecturerToken)
	rec := httptest.NewRecorder()
	env.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestImportCredentials_RequiresConfirmPhrase(t *testing.T) {
	for _, confirm := range []string{"", "rotate", "RESET"} {
		env := newImportCredentialsTestEnv(t)
		body, contentType := multipartRoster(t, testRosterCSV, confirm)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/students/import-credentials", body)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Authorization", "Bearer "+env.adminToken)
		rec := httptest.NewRecorder()
		env.engine.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("confirm=%q: status = %d, want 400", confirm, rec.Code)
		}
		var resp map[string]string
		json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp["error"] != "confirmation_required" {
			t.Errorf("confirm=%q: error = %q, want \"confirmation_required\"", confirm, resp["error"])
		}
	}
}

// One SendCredential per row WITH an email, zero SendChangeNotice regardless of
// ADMIN_EMAIL, and a job id the admin can poll.
func TestImportCredentials_EmailsOnlyRowsWithAddresses(t *testing.T) {
	env := newImportCredentialsTestEnv(t)
	body, contentType := multipartRoster(t, testRosterCSV, "ROTATE")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/students/import-credentials", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+env.adminToken)
	rec := httptest.NewRecorder()
	env.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var report struct {
		Total    int    `json:"total"`
		JobID    string `json:"job_id"`
		Accounts []struct {
			StudentIDExt string `json:"student_id_ext"`
			Password     string `json:"password"`
			EmailStatus  string `json:"email_status"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.Total != 2 || len(report.Accounts) != 2 {
		t.Fatalf("report total=%d accounts=%d, want 2/2", report.Total, len(report.Accounts))
	}
	if report.JobID == "" {
		t.Fatal("job_id is empty: the UI has nothing to poll")
	}
	if report.Accounts[0].EmailStatus != "queued" || report.Accounts[1].EmailStatus != "no_email" {
		t.Errorf("email_status = %q/%q, want queued/no_email",
			report.Accounts[0].EmailStatus, report.Accounts[1].EmailStatus)
	}
	for _, a := range report.Accounts {
		if a.Password == "" {
			t.Errorf("%s: empty password", a.StudentIDExt)
		}
	}

	env.waitForJobDone(t, report.JobID)

	if got := env.email.CredentialCount(); got != 1 {
		t.Errorf("SendCredential called %d times, want 1 (only the row with an address)", got)
	}
	if got := env.email.NoticeCount(); got != 0 {
		t.Errorf("SendChangeNotice called %d times, want 0", got)
	}
}

func TestImportCredentialsJobStatus_OwnershipAndUnknownIDs(t *testing.T) {
	env := newImportCredentialsTestEnv(t)
	body, contentType := multipartRoster(t, testRosterCSV, "ROTATE")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/students/import-credentials", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+env.adminToken)
	rec := httptest.NewRecorder()
	env.engine.ServeHTTP(rec, req)
	var report struct {
		JobID string `json:"job_id"`
	}
	json.Unmarshal(rec.Body.Bytes(), &report)

	cases := []struct {
		name  string
		id    string
		token string
		want  int
	}{
		{"owner reads own job", report.JobID, env.adminToken, http.StatusOK},
		{"other admin blocked", report.JobID, env.otherAdminToken, http.StatusNotFound},
		{"lecturer blocked", report.JobID, env.lecturerToken, http.StatusForbidden},
		{"unknown id", "deadbeef", env.adminToken, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet,
				"/api/v1/students/import-credentials/"+tc.id+"/status", nil)
			r.Header.Set("Authorization", "Bearer "+tc.token)
			w := httptest.NewRecorder()
			env.engine.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Errorf("status = %d, want %d (body: %s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
```

Write `newImportCredentialsTestEnv(t)` in the same file. It must build: a real-DB repo + `StudentImportService` + `AuthService` (copy the construction from `bulk_reset_http_test.go`), an `EmailJobRegistry`, a `LecturerController` with all three setters called, a gin engine registering the two routes under `AuthMiddleware`, three JWTs (admin, a second admin, lecturer), and an email spy exposing `CredentialCount()` / `NoticeCount()` under a mutex (the worker writes from another goroutine — the counters must be mutex-guarded or `-race` will flag them). `waitForJobDone(t, jobID)` polls the registry directly (not over HTTP) every 10 ms up to 5 s until `Done`, then `t.Fatal`s on timeout.

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd backend && go test -p 1 ./internal/controllers/ -run TestImportCredentials -v
```

Expected: FAIL — `ctrl.ImportStudentsWithCredentials undefined`.

- [ ] **Step 3: Write the implementation**

In `backend/internal/controllers/lecturer_controller.go`, add the registry field to the struct (line 63-68) and a setter next to `SetStudentImportService`:

```go
type LecturerController struct {
	repo          *repository.PostgresRepository
	importService *services.StudentImportService
	authService   *services.AuthService
	hub           *services.Hub
	emailJobs     *services.EmailJobRegistry
}

func (ctrl *LecturerController) SetEmailJobRegistry(reg *services.EmailJobRegistry) {
	ctrl.emailJobs = reg
}
```

Then append the handlers after `ImportStudents`:

```go
// ImportStudentsWithCredentials handles POST /api/v1/students/import-credentials.
//
// Admin-only counterpart to ImportStudents: it rotates the password of EVERY
// row in the roster — new and returning students alike — evicting their
// sessions, and emails each new credential from a background worker whose
// progress the admin can poll. Emailing plaintext passwords in bulk is the
// deliberate point of this endpoint; the typed confirmation phrase and the
// admin-only role gate are what stand in front of it.
func (ctrl *LecturerController) ImportStudentsWithCredentials(c *gin.Context) {
	if ctrl.importService == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "student import service is not configured"})
		return
	}

	userVal, _ := c.Get("user")
	admin := userVal.(models.User)

	// The confirmation phrase is checked server-side on EVERY request; the
	// server never trusts the UI's own confirm gate (same rule as
	// BulkResetPassword's "RESET").
	if c.PostForm("confirm") != "ROTATE" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "confirmation_required"})
		return
	}

	header, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
		return
	}
	file, err := header.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer file.Close()

	rows, err := services.ParseRoster(header.Filename, file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	report, err := ctrl.importService.ImportRotating(c.Request.Context(), int(admin.ID), rows)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Post-commit audit trail: one password_events row per rotated account, and
	// deliberately NO email from this path (see LogBulkCredentials).
	if ctrl.authService != nil {
		events := make([]services.BulkCredentialEvent, 0, len(report.Accounts))
		for _, a := range report.Accounts {
			source := models.PasswordEventAdminReset
			if a.Outcome == "created" {
				source = models.PasswordEventCreate
			}
			events = append(events, services.BulkCredentialEvent{
				UserID: a.UserID, Source: source, Plaintext: a.Password,
			})
		}
		ctrl.authService.LogBulkCredentials(c.Request.Context(), admin, events)
	}

	// Hand delivery to a single background goroutine. Serial by design: one SMTP
	// connection at a time keeps the send under Gmail's rate limits instead of
	// bursting into a rejection. The request context is already cancelled by the
	// time this runs, hence context.Background().
	toSend := make([]services.RotatedAccount, 0, len(report.Accounts))
	for _, a := range report.Accounts {
		if a.EmailStatus == "queued" {
			toSend = append(toSend, a)
		}
	}
	if len(toSend) > 0 && ctrl.authService != nil && ctrl.emailJobs != nil {
		job := ctrl.emailJobs.Register(int(admin.ID), len(toSend))
		report.JobID = job.ID
		go func(jobID string, accounts []services.RotatedAccount) {
			defer ctrl.emailJobs.Finish(jobID)
			for _, a := range accounts {
				if err := ctrl.authService.SendCredentialEmail(a.Email, a.StudentIDExt, a.Password); err != nil {
					log.Printf("import-credentials: SendCredential failed for %q: %v", a.StudentIDExt, err)
					ctrl.emailJobs.MarkFailed(jobID, a.StudentIDExt)
					continue
				}
				ctrl.emailJobs.MarkSent(jobID)
			}
		}(job.ID, toSend)
	}

	c.JSON(http.StatusOK, report)
}

// ImportCredentialsJobStatus handles
// GET /api/v1/students/import-credentials/:job_id/status.
// A job belonging to another admin is reported as not-found, so the endpoint
// cannot be used to probe other admins' activity.
func (ctrl *LecturerController) ImportCredentialsJobStatus(c *gin.Context) {
	if ctrl.emailJobs == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	userVal, _ := c.Get("user")
	admin := userVal.(models.User)

	job, ok := ctrl.emailJobs.Get(c.Param("job_id"), int(admin.ID))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	c.JSON(http.StatusOK, job)
}
```

`log` must be in the file's import block; add `"log"` if it is not already there.

`AuthService` has no exported credential-send method, so add this thin accessor to `backend/internal/services/auth_service.go`, directly after `LogBulkCredentials`:

```go
// SendCredentialEmail exposes the credential email to callers that own their
// own delivery loop (the rotating importer's background worker), which needs to
// record per-send success or failure rather than fire-and-forget.
func (s *AuthService) SendCredentialEmail(toEmail, username, password string) error {
	return s.email.SendCredential(toEmail, username, password)
}
```

In `backend/cmd/main.go`, after line 151 (`lecturerCtrl.SetAuthService(authService)`):

```go
	emailJobRegistry := services.NewEmailJobRegistry()
	lecturerCtrl.SetEmailJobRegistry(emailJobRegistry)
```

and next to `staff.POST("/students", ...)` (line 317):

```go
			// Admin-only rotating credential import: rotates and emails a fresh
			// password for EVERY row, unlike /lecturer/students/import.
			staff.POST("/students/import-credentials", controllers.RoleMiddleware(models.RoleAdmin), lecturerCtrl.ImportStudentsWithCredentials)
			staff.GET("/students/import-credentials/:job_id/status", controllers.RoleMiddleware(models.RoleAdmin), lecturerCtrl.ImportCredentialsJobStatus)
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd backend && go test -p 1 -race ./internal/controllers/ -run TestImportCredentials -v
cd backend && go build ./... && go test -p 1 ./internal/...
```

Expected: the new tests PASS, `go build` succeeds, and the whole backend suite stays green.

If gin panics at startup with a wildcard conflict on `/students/...`, the existing `staff.POST("/students", ...)` and the new segment coexist fine, but a pre-existing `/:something` route in the same group would collide — in that case register the two new routes on a dedicated `v1.Group("/students")` with `controllers.AuthMiddleware(authService)` applied, keeping the paths byte-identical.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/controllers/lecturer_controller.go backend/internal/controllers/import_credentials_http_test.go backend/internal/services/auth_service.go backend/cmd/main.go
git commit -m "feat(api): add admin rotating credential import and job-status endpoints"
```

---

### Task 6: Frontend — `useEmailJobStatus` composable

**Files:**
- Create: `frontend/src/composables/useEmailJobStatus.ts`
- Test: `frontend/src/__tests__/composables/useEmailJobStatus.test.ts`

**Interfaces:**
- Consumes: `GET /api/v1/students/import-credentials/:job_id/status` (Task 5).
- Produces:
  ```ts
  export interface EmailJobStatus {
    job_id: string
    total: number
    sent: number
    failed: number
    done: boolean
    failures: string[]
  }

  export const EMAIL_JOB_STORAGE_KEY = 'sql_quiz_email_job'

  export function useEmailJobStatus(): {
    job: Ref<EmailJobStatus | null>
    state: Ref<'idle' | 'running' | 'done' | 'unavailable'>
    track: (jobId: string) => void
    restore: () => void
    dismiss: () => void
  }
  ```

- [ ] **Step 1: Write the failing test**

Create `frontend/src/__tests__/composables/useEmailJobStatus.test.ts`:

```ts
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useEmailJobStatus, EMAIL_JOB_STORAGE_KEY } from '../../composables/useEmailJobStatus'

function jsonResponse(data: unknown, ok = true, status = 200) {
  return Promise.resolve({ ok, status, json: () => Promise.resolve(data) } as Response)
}

describe('useEmailJobStatus', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    localStorage.clear()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('polls until the job is done, then stops', async () => {
    const fetchMock = vi.fn()
      .mockReturnValueOnce(jsonResponse({ job_id: 'j1', total: 2, sent: 1, failed: 0, done: false, failures: [] }))
      .mockReturnValueOnce(jsonResponse({ job_id: 'j1', total: 2, sent: 2, failed: 0, done: true, failures: [] }))
    vi.stubGlobal('fetch', fetchMock)

    const { job, state, track } = useEmailJobStatus()
    track('j1')
    await vi.advanceTimersByTimeAsync(0)

    expect(state.value).toBe('running')
    expect(job.value?.sent).toBe(1)

    await vi.advanceTimersByTimeAsync(2000)
    expect(state.value).toBe('done')
    expect(job.value?.sent).toBe(2)

    const callsAtDone = fetchMock.mock.calls.length
    await vi.advanceTimersByTimeAsync(10000)
    expect(fetchMock.mock.calls.length).toBe(callsAtDone)
  })

  it('enters "unavailable" on 404 instead of erroring', async () => {
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse({ error: 'job not found' }, false, 404)))

    const { state, track } = useEmailJobStatus()
    track('gone')
    await vi.advanceTimersByTimeAsync(0)

    expect(state.value).toBe('unavailable')
  })

  it('persists the tracked job and restores it on a fresh mount', async () => {
    vi.stubGlobal('fetch', vi.fn(() =>
      jsonResponse({ job_id: 'j9', total: 5, sent: 3, failed: 1, done: false, failures: ['s1'] })))

    const first = useEmailJobStatus()
    first.track('j9')
    await vi.advanceTimersByTimeAsync(0)
    expect(localStorage.getItem(EMAIL_JOB_STORAGE_KEY)).toBe('j9')

    const second = useEmailJobStatus()
    second.restore()
    await vi.advanceTimersByTimeAsync(0)
    expect(second.job.value?.job_id).toBe('j9')
    expect(second.state.value).toBe('running')
  })

  it('dismiss clears the stored job and returns to idle', async () => {
    vi.stubGlobal('fetch', vi.fn(() =>
      jsonResponse({ job_id: 'j2', total: 1, sent: 1, failed: 0, done: true, failures: [] })))

    const { state, track, dismiss } = useEmailJobStatus()
    track('j2')
    await vi.advanceTimersByTimeAsync(0)
    expect(state.value).toBe('done')

    dismiss()
    expect(state.value).toBe('idle')
    expect(localStorage.getItem(EMAIL_JOB_STORAGE_KEY)).toBeNull()
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd frontend && npx vitest run src/__tests__/composables/useEmailJobStatus.test.ts
```

Expected: FAIL — cannot resolve `../../composables/useEmailJobStatus`.

- [ ] **Step 3: Write the implementation**

Create `frontend/src/composables/useEmailJobStatus.ts`:

```ts
import { ref, onUnmounted, type Ref } from 'vue'

export interface EmailJobStatus {
  job_id: string
  total: number
  sent: number
  failed: number
  done: boolean
  failures: string[]
}

/** The in-flight job id survives a reload: the worker outlives the page. */
export const EMAIL_JOB_STORAGE_KEY = 'sql_quiz_email_job'

const POLL_INTERVAL_MS = 2000
/** Stop polling after this long even if the job never reports done. */
const POLL_CEILING_MS = 15 * 60 * 1000

export type EmailJobState = 'idle' | 'running' | 'done' | 'unavailable'

/**
 * Polls the credential-email job endpoint so AdminView can show real send
 * progress instead of a static "queued". A 404 is NOT an error: the registry is
 * in-memory, so a server restart or the 30-minute retention window legitimately
 * drops the job — and every password is already in the admin's results table.
 */
export function useEmailJobStatus(): {
  job: Ref<EmailJobStatus | null>
  state: Ref<EmailJobState>
  track: (jobId: string) => void
  restore: () => void
  dismiss: () => void
} {
  const job = ref<EmailJobStatus | null>(null)
  const state = ref<EmailJobState>('idle')

  let timer: ReturnType<typeof setTimeout> | null = null
  let startedAt = 0

  function stop() {
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
    }
  }

  async function poll(jobId: string) {
    const token = localStorage.getItem('sql_quiz_token')
    let response: Response
    try {
      response = await fetch(
        `/api/v1/students/import-credentials/${encodeURIComponent(jobId)}/status`,
        { headers: { Authorization: `Bearer ${token}` } },
      )
    } catch {
      state.value = 'unavailable'
      stop()
      return
    }

    if (!response.ok) {
      state.value = 'unavailable'
      stop()
      return
    }

    job.value = (await response.json()) as EmailJobStatus
    if (job.value.done) {
      state.value = 'done'
      stop()
      return
    }
    state.value = 'running'
    if (Date.now() - startedAt > POLL_CEILING_MS) {
      state.value = 'unavailable'
      stop()
      return
    }
    timer = setTimeout(() => void poll(jobId), POLL_INTERVAL_MS)
  }

  function track(jobId: string) {
    if (!jobId) return
    stop()
    startedAt = Date.now()
    localStorage.setItem(EMAIL_JOB_STORAGE_KEY, jobId)
    state.value = 'running'
    void poll(jobId)
  }

  function restore() {
    const stored = localStorage.getItem(EMAIL_JOB_STORAGE_KEY)
    if (stored) track(stored)
  }

  function dismiss() {
    stop()
    localStorage.removeItem(EMAIL_JOB_STORAGE_KEY)
    job.value = null
    state.value = 'idle'
  }

  onUnmounted(stop)

  return { job, state, track, restore, dismiss }
}
```

`onUnmounted` outside a component instance logs a Vue warning in tests but is harmless; if the warning fails a strict test config, guard it with `getCurrentInstance()`.

- [ ] **Step 4: Run the test to verify it passes**

```bash
cd frontend && npx vitest run src/__tests__/composables/useEmailJobStatus.test.ts
```

Expected: PASS, 4 tests.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/composables/useEmailJobStatus.ts frontend/src/__tests__/composables/useEmailJobStatus.test.ts
git commit -m "feat(admin): add useEmailJobStatus polling composable"
```

---

### Task 7: Frontend — `EmailJobBubble.vue`

**Files:**
- Create: `frontend/src/components/EmailJobBubble.vue`
- Test: `frontend/src/__tests__/components/EmailJobBubble.test.ts`

**Interfaces:**
- Consumes: `EmailJobStatus` and `EmailJobState` from `useEmailJobStatus` (Task 6).
- Produces: a component with props `{ job: EmailJobStatus | null, state: EmailJobState }` and one emit, `dismiss`. Root element carries `data-testid="email-job-bubble"`; it renders nothing when `state === 'idle'`.

- [ ] **Step 1: Write the failing test**

Create `frontend/src/__tests__/components/EmailJobBubble.test.ts`:

```ts
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import EmailJobBubble from '../../components/EmailJobBubble.vue'

const runningJob = { job_id: 'j1', total: 40, sent: 12, failed: 2, done: false, failures: ['s7', 's9'] }

describe('EmailJobBubble', () => {
  it('renders nothing when idle', () => {
    const wrapper = mount(EmailJobBubble, { props: { job: null, state: 'idle' } })
    expect(wrapper.find('[data-testid="email-job-bubble"]').exists()).toBe(false)
  })

  it('shows the sent/total counter and the failure count while running', () => {
    const wrapper = mount(EmailJobBubble, { props: { job: runningJob, state: 'running' } })
    const text = wrapper.find('[data-testid="email-job-bubble"]').text()
    expect(text).toContain('12/40')
    expect(text).toContain('2 failed')
  })

  it('lists the failed login IDs when the job finishes with failures', () => {
    const wrapper = mount(EmailJobBubble, {
      props: { job: { ...runningJob, sent: 38, done: true }, state: 'done' },
    })
    const text = wrapper.find('[data-testid="email-job-bubble"]').text()
    expect(text).toContain('s7')
    expect(text).toContain('s9')
  })

  it('reports a clean finish without a failure list', () => {
    const wrapper = mount(EmailJobBubble, {
      props: { job: { job_id: 'j1', total: 3, sent: 3, failed: 0, done: true, failures: [] }, state: 'done' },
    })
    const text = wrapper.find('[data-testid="email-job-bubble"]').text()
    expect(text).toContain('3/3')
    expect(wrapper.find('[data-testid="email-job-failures"]').exists()).toBe(false)
  })

  it('explains the unavailable state instead of showing an error', () => {
    const wrapper = mount(EmailJobBubble, { props: { job: null, state: 'unavailable' } })
    expect(wrapper.find('[data-testid="email-job-bubble"]').text().toLowerCase())
      .toContain('progress unavailable')
  })

  it('emits dismiss when closed', async () => {
    const wrapper = mount(EmailJobBubble, { props: { job: runningJob, state: 'running' } })
    await wrapper.find('[data-testid="email-job-dismiss"]').trigger('click')
    expect(wrapper.emitted('dismiss')).toHaveLength(1)
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd frontend && npx vitest run src/__tests__/components/EmailJobBubble.test.ts
```

Expected: FAIL — cannot resolve `../../components/EmailJobBubble.vue`.

- [ ] **Step 3: Write the implementation**

Create `frontend/src/components/EmailJobBubble.vue`:

```vue
<script setup lang="ts">
import { computed } from 'vue'
import type { EmailJobStatus, EmailJobState } from '../composables/useEmailJobStatus'

const props = defineProps<{
  job: EmailJobStatus | null
  state: EmailJobState
}>()

defineEmits<{ (e: 'dismiss'): void }>()

const visible = computed(() => props.state !== 'idle')
const processed = computed(() => (props.job ? props.job.sent + props.job.failed : 0))
const total = computed(() => props.job?.total ?? 0)
const failed = computed(() => props.job?.failed ?? 0)
const failures = computed(() => props.job?.failures ?? [])

const tone = computed(() => {
  if (props.state === 'unavailable') return 'neutral'
  if (props.state === 'done') return failed.value > 0 ? 'warn' : 'ok'
  return 'busy'
})

// Ten dots is a fixed-width progress track — it reads at a glance without the
// layout shifting as the roster size changes.
const filledDots = computed(() => {
  if (!total.value) return 0
  return Math.round((processed.value / total.value) * 10)
})
</script>

<template>
  <div v-if="visible" class="bubble" :class="tone" data-testid="email-job-bubble" role="status">
    <button
      type="button"
      class="dismiss"
      data-testid="email-job-dismiss"
      aria-label="Dismiss email progress"
      @click="$emit('dismiss')"
    >×</button>

    <template v-if="state === 'unavailable'">
      <div class="headline">✉ Progress unavailable</div>
      <div class="detail">The send is still running or has finished; every password is in the results table.</div>
    </template>

    <template v-else>
      <div class="headline" aria-live="polite">
        ✉ {{ state === 'done' ? 'Passwords emailed' : 'Emailing passwords' }}
        <span class="count">{{ processed }}/{{ total }}</span>
      </div>
      <div class="track" aria-hidden="true">
        <span v-for="n in 10" :key="n" class="dot" :class="{ filled: n <= filledDots }">●</span>
      </div>
      <div v-if="failed > 0" class="detail">{{ failed }} failed</div>
      <div v-if="state === 'done' && failures.length" class="detail" data-testid="email-job-failures">
        Not delivered: {{ failures.join(', ') }} — copy their passwords from the table.
      </div>
    </template>
  </div>
</template>

<style scoped>
.bubble {
  position: fixed;
  top: calc(var(--space-6) + 3rem);
  right: var(--space-6);
  z-index: 60;
  max-width: 22rem;
  padding: var(--space-4);
  padding-right: var(--space-6);
  background: var(--color-surface);
  border: 2px solid var(--color-border);
  font-size: 0.85rem;
  color: var(--color-text);
}
.bubble.ok { border-color: var(--color-success); }
.bubble.warn { border-color: var(--color-warning, var(--color-danger)); }
.headline { font-weight: 800; display: flex; gap: var(--space-2); align-items: baseline; }
.count { font-family: var(--font-mono, monospace); }
.detail { margin-top: var(--space-1); color: var(--color-text-muted); }
.track { margin-top: var(--space-1); letter-spacing: 0.15em; color: var(--color-border); }
.dot.filled { color: var(--color-text); }
.bubble.busy .dot.filled { animation: pulse 1.4s ease-in-out infinite; }
.dismiss {
  position: absolute;
  top: 2px;
  right: 6px;
  border: none;
  background: none;
  color: var(--color-text-muted);
  font-size: 1.1rem;
  line-height: 1;
  cursor: pointer;
}
@keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.45; } }
@media (prefers-reduced-motion: reduce) {
  .bubble.busy .dot.filled { animation: none; }
}
</style>
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
cd frontend && npx vitest run src/__tests__/components/EmailJobBubble.test.ts
```

Expected: PASS, 6 tests.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/EmailJobBubble.vue frontend/src/__tests__/components/EmailJobBubble.test.ts
git commit -m "feat(admin): add email progress bubble component"
```

---

### Task 8: Frontend — rotating import card + AdminView wiring

**Files:**
- Create: `frontend/src/components/BulkImportStudentsCredentials.vue`
- Modify: `frontend/src/components/AdminView.vue` (imports at lines 1-22; the Bulk Import card block at lines 405-410)
- Test: `frontend/src/__tests__/components/BulkImportStudentsCredentials.test.ts`

**Interfaces:**
- Consumes: `POST /api/v1/students/import-credentials` (Task 5), `EmailJobBubble` (Task 7), `useEmailJobStatus` (Task 6), existing `toCsv`/`downloadCsv` from `frontend/src/utils/csv.ts`.
- Produces: `BulkImportStudentsCredentials.vue` emitting `job-started` with the `job_id` string.

- [ ] **Step 1: Write the failing test**

Create `frontend/src/__tests__/components/BulkImportStudentsCredentials.test.ts`:

```ts
import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../../utils/csv', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../utils/csv')>()
  return { ...actual, downloadCsv: vi.fn() }
})

import BulkImportStudentsCredentials from '../../components/BulkImportStudentsCredentials.vue'
import { downloadCsv } from '../../utils/csv'

const report = {
  total: 2,
  created: 1,
  updated: 1,
  skipped: 0,
  errors: [],
  job_id: 'job-abc',
  accounts: [
    { row: 1, student_number: 'N1', student_id_ext: 's1', email: 'a@example.com', password: 'pw1', outcome: 'created', email_status: 'queued' },
    { row: 2, student_number: 'N2', student_id_ext: 's2', email: '', password: 'pw2', outcome: 'updated', email_status: 'no_email' },
  ],
}

function jsonResponse(data: unknown, ok = true, status = 200) {
  return Promise.resolve({ ok, status, json: () => Promise.resolve(data) } as Response)
}

/** Attach a fake file so the component's validation passes without a real input. */
async function selectFile(wrapper: ReturnType<typeof mount>) {
  const file = new File(['x'], 'roster.csv', { type: 'text/csv' })
  const input = wrapper.find('input[type="file"]')
  Object.defineProperty(input.element, 'files', { value: [file], configurable: true })
  await input.trigger('change')
}

describe('BulkImportStudentsCredentials', () => {
  beforeEach(() => { localStorage.setItem('sql_quiz_token', 't') })
  afterEach(() => { vi.restoreAllMocks(); localStorage.clear() })

  it('refuses to submit until ROTATE is typed exactly', async () => {
    const wrapper = mount(BulkImportStudentsCredentials)
    await selectFile(wrapper)

    const button = wrapper.find('[data-testid="rotate-import-submit"]')
    expect((button.element as HTMLButtonElement).disabled).toBe(true)

    await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('rotate')
    expect((button.element as HTMLButtonElement).disabled).toBe(true)

    await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
    expect((button.element as HTMLButtonElement).disabled).toBe(false)
  })

  it('posts the file plus the confirm field and emits the job id', async () => {
    const fetchMock = vi.fn(() => jsonResponse(report))
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = mount(BulkImportStudentsCredentials)
    await selectFile(wrapper)
    await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
    await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
    await flushPromises()

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/students/import-credentials')
    expect(init.method).toBe('POST')
    const body = init.body as FormData
    expect(body.get('confirm')).toBe('ROTATE')
    expect(body.get('file')).toBeInstanceOf(File)

    expect(wrapper.emitted('job-started')).toEqual([['job-abc']])
  })

  it('renders outcome and email-status for every account row', async () => {
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(report)))

    const wrapper = mount(BulkImportStudentsCredentials)
    await selectFile(wrapper)
    await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
    await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
    await flushPromises()

    const rows = wrapper.findAll('[data-testid="rotated-accounts-table"] tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('created')
    expect(rows[0].text()).toContain('queued')
    expect(rows[1].text()).toContain('updated')
    expect(rows[1].text()).toContain('no email')
  })

  it('copy-all-as-CSV emits one line per account', async () => {
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(report)))

    const wrapper = mount(BulkImportStudentsCredentials)
    await selectFile(wrapper)
    await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
    await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
    await flushPromises()

    await wrapper.find('[data-testid="rotate-copy-csv"]').trigger('click')
    expect(downloadCsv).toHaveBeenCalledTimes(1)
    const [, content] = (downloadCsv as unknown as ReturnType<typeof vi.fn>).mock.calls[0]
    const lines = (content as string).split('\r\n')
    expect(lines).toHaveLength(3) // header + 2 rows
    expect(lines[0]).toBe('student_id_ext,email,password')
    expect(lines[1]).toContain('pw1')
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd frontend && npx vitest run src/__tests__/components/BulkImportStudentsCredentials.test.ts
```

Expected: FAIL — cannot resolve `../../components/BulkImportStudentsCredentials.vue`.

- [ ] **Step 3: Write the implementation**

Create `frontend/src/components/BulkImportStudentsCredentials.vue`:

```vue
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useSingleFlight } from '../composables/useSingleFlight'
import { useNotifications } from '../services/notification_service'
import { handleApiError } from '../services/error_handler'
import { useFormValidation } from '../composables/useFormValidation'
import { toCsv, downloadCsv } from '../utils/csv'
import FieldError from './FieldError.vue'
import Spinner from './Spinner.vue'

interface ImportError {
  row: number
  student_number: string
  message: string
}

interface RotatedAccount {
  row: number
  student_number: string
  student_id_ext: string
  email: string
  password: string
  outcome: 'created' | 'updated'
  email_status: 'queued' | 'no_email'
}

interface RotatingImportReport {
  total: number
  created: number
  updated: number
  skipped: number
  errors: ImportError[]
  accounts: RotatedAccount[]
  job_id: string
}

const emit = defineEmits<{ (e: 'job-started', jobId: string): void }>()

const { notify } = useNotifications()

const selectedFile = ref<File | null>(null)
const confirmPhrase = ref('')
const topLevelError = ref('')
const result = ref<RotatingImportReport | null>(null)

// Exact-match gate, same rule as BulkPasswordReset's RESET. The server checks
// it again on every request — this input is convenience, not security.
const confirmed = computed(() => confirmPhrase.value === 'ROTATE')

const fileValidator = (file: File | null) => {
  if (!file) return 'Please select a file'
  if (!file.name.match(/\.(csv|xlsx)$/i)) return 'File must be .csv or .xlsx'
  return null
}
const fileForm = useFormValidation({ file: { rules: [fileValidator] } })

const handleFileChange = (event: Event) => {
  const input = event.target as HTMLInputElement
  selectedFile.value = input.files?.[0] ?? null
  fileForm.values.file = selectedFile.value
  topLevelError.value = ''
  result.value = null
}

async function copyPassword(text: string) {
  try {
    if (navigator?.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      notify('Password copied to clipboard.', 'success')
    } else {
      notify('Clipboard not available in this browser.', 'error')
    }
  } catch {
    notify('Failed to copy password.', 'error')
  }
}

// Every password is shown exactly once. Rows whose email is missing — or whose
// delivery silently fails — are only recoverable from here.
function downloadAllAsCsv() {
  if (!result.value) return
  const content = toCsv(
    ['student_id_ext', 'email', 'password'],
    result.value.accounts.map(a => [a.student_id_ext, a.email, a.password]),
  )
  downloadCsv('rotated-credentials.csv', content)
}

const { inFlight: importLoading, run: submitImport } = useSingleFlight(async () => {
  if (!fileForm.validateAll()) return
  if (!selectedFile.value || !confirmed.value) return

  topLevelError.value = ''
  result.value = null

  const token = localStorage.getItem('sql_quiz_token')
  const formData = new FormData()
  formData.append('file', selectedFile.value)
  formData.append('confirm', 'ROTATE')

  try {
    const response = await fetch('/api/v1/students/import-credentials', {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
      body: formData,
    })
    const data = await response.json().catch(() => ({} as any))

    if (!response.ok) {
      if (response.status === 400) {
        topLevelError.value = data.error || 'File format rejected'
        return
      }
      throw new Error(data.error || 'Import failed')
    }

    result.value = data
    confirmPhrase.value = ''
    if (data.job_id) emit('job-started', data.job_id)
    notify('Import complete — passwords are being emailed.', 'success')
  } catch (err: any) {
    handleApiError(err, { fallback: 'Import failed' })
  }
})
</script>

<template>
  <div class="bulk-import card">
    <h2>Import &amp; Email Passwords</h2>
    <p class="desc">
      Same file format as the roster import, plus an Email column. Every student in the file —
      new and returning — gets a brand-new password emailed to them, and is signed out of any
      current session. Rows without an email address are still imported; copy their passwords below.
    </p>

    <div class="form-row">
      <input
        type="file"
        accept=".csv,.xlsx"
        :disabled="importLoading"
        class="file-input"
        aria-describedby="err-rotate-file"
        @change="handleFileChange"
      />
      <FieldError :message="fileForm.touched.file ? fileForm.errors.file : ''" id="err-rotate-file" />
    </div>

    <div class="form-row">
      <label class="confirm-label" for="rotate-confirm">Type <code>ROTATE</code> to confirm</label>
      <input
        id="rotate-confirm"
        v-model="confirmPhrase"
        type="text"
        class="file-input"
        data-testid="rotate-confirm-input"
        :disabled="importLoading"
        autocomplete="off"
      />
      <button
        class="primary-btn danger"
        data-testid="rotate-import-submit"
        :disabled="importLoading || !selectedFile || !confirmed"
        @click="submitImport"
      >
        <Spinner v-if="importLoading" size="sm" inline :invert="true" />{{ importLoading ? 'Importing' : 'Import & Email Passwords' }}
      </button>
    </div>

    <div v-if="topLevelError" class="error-box">{{ topLevelError }}</div>

    <div v-if="result !== null" class="results-section">
      <div class="summary-grid">
        <div class="summary-cell"><div class="summary-value">{{ result.total }}</div><div class="summary-label">Total</div></div>
        <div class="summary-cell"><div class="summary-value">{{ result.created }}</div><div class="summary-label">Created</div></div>
        <div class="summary-cell"><div class="summary-value">{{ result.updated }}</div><div class="summary-label">Rotated</div></div>
        <div class="summary-cell"><div class="summary-value">{{ result.skipped }}</div><div class="summary-label">Skipped</div></div>
      </div>

      <div v-if="result.accounts.length > 0" class="created-section">
        <div class="section-head">
          <h3>Passwords ({{ result.accounts.length }})</h3>
          <button type="button" class="copy-btn" data-testid="rotate-copy-csv" @click="downloadAllAsCsv">
            Copy all as CSV
          </button>
        </div>
        <p class="desc">Shown once. Download the CSV before leaving this page.</p>
        <div class="errors-table-wrap">
          <table class="errors-table" data-testid="rotated-accounts-table">
            <thead>
              <tr><th>Login ID</th><th>Outcome</th><th>Email</th><th>Password</th><th></th></tr>
            </thead>
            <tbody>
              <tr v-for="a in result.accounts" :key="a.row">
                <td>{{ a.student_id_ext }}</td>
                <td>{{ a.outcome }}</td>
                <td>{{ a.email_status === 'queued' ? `✉ queued — ${a.email}` : 'no email' }}</td>
                <td><code class="pw-cell">{{ a.password }}</code></td>
                <td><button type="button" class="copy-btn" @click="copyPassword(a.password)">Copy</button></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div v-if="result.errors.length > 0" class="errors-section">
        <h3>Row Errors ({{ result.errors.length }})</h3>
        <div class="errors-table-wrap">
          <table class="errors-table">
            <thead><tr><th>Row</th><th>Student Number</th><th>Reason</th></tr></thead>
            <tbody>
              <tr v-for="e in result.errors" :key="e.row">
                <td>{{ e.row }}</td><td>{{ e.student_number || '—' }}</td><td>{{ e.message }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
      <div v-else class="no-errors-msg">No row errors.</div>
    </div>
  </div>
</template>

<style scoped>
.bulk-import { display: flex; flex-direction: column; gap: var(--space-4); }
.card { background: var(--color-surface); padding: var(--space-6); border: 2px solid var(--color-border); border-radius: 0; }
h2 { color: var(--color-text); margin: 0; font-weight: 800; }
h3 { margin: 0 0 var(--space-2) 0; font-size: 1rem; font-weight: 800; }
.desc { color: var(--color-text-muted); margin: 0; font-size: 0.9rem; }
.form-row { display: flex; align-items: center; gap: var(--space-4); flex-wrap: wrap; }
.confirm-label { font-size: 0.85rem; font-weight: 700; }
.file-input {
  flex: 1; min-width: 200px; padding: var(--space-2);
  border: 2px solid var(--color-border); border-radius: 0;
  background: var(--color-surface); color: var(--color-text);
  font-family: var(--font-mono); font-size: 0.9rem;
}
.primary-btn {
  padding: var(--space-2) var(--space-4); background-color: var(--color-primary);
  color: var(--color-surface); border: 2px solid var(--color-border);
  font-weight: 700; cursor: pointer; white-space: nowrap;
}
.primary-btn.danger { background-color: var(--color-danger); }
.primary-btn:disabled { opacity: 0.5; cursor: not-allowed; }
.error-box {
  background-color: var(--color-danger-bg); color: var(--color-danger);
  padding: var(--space-4); border: 2px solid var(--color-danger); font-weight: 700;
}
.results-section { display: flex; flex-direction: column; gap: var(--space-4); }
.summary-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: var(--space-4); }
.summary-cell { background: var(--color-surface); border: 2px solid var(--color-border); padding: var(--space-4); text-align: center; }
.summary-value { font-size: 1.8rem; font-weight: 800; color: var(--color-text); }
.summary-label { font-size: 0.75rem; color: var(--color-text-muted); margin-top: 0.2rem; font-weight: 700; }
.section-head { display: flex; align-items: center; justify-content: space-between; gap: var(--space-4); }
.created-section, .errors-section { display: flex; flex-direction: column; gap: var(--space-2); }
.errors-table-wrap { overflow-x: auto; border: 2px solid var(--color-border); }
.errors-table { width: 100%; border-collapse: collapse; font-size: 0.85rem; background: var(--color-surface); color: var(--color-text); }
.errors-table th, .errors-table td { border: 1px solid var(--color-border); padding: 0.5rem; text-align: left; }
.errors-table th { background: var(--color-surface-muted); font-weight: 700; }
.no-errors-msg {
  color: var(--color-success); background: var(--color-success-bg);
  padding: var(--space-2); border: 2px solid var(--color-success);
  font-weight: 700; text-align: center;
}
.pw-cell { font-family: var(--font-mono, monospace); font-weight: 700; user-select: all; }
.copy-btn {
  padding: 2px 10px; border: 1px solid var(--color-border);
  background: var(--color-surface); color: var(--color-text);
  font-weight: 700; cursor: pointer; white-space: nowrap;
}
.copy-btn:hover { background: var(--color-surface-muted); }
</style>
```

Then wire `AdminView.vue`. Add to the import block (after line 20, `import BulkImportStudents from './BulkImportStudents.vue'`):

```ts
import BulkImportStudentsCredentials from './BulkImportStudentsCredentials.vue'
import EmailJobBubble from './EmailJobBubble.vue'
import { useEmailJobStatus } from '../composables/useEmailJobStatus'
```

Add to the `<script setup>` body, next to the other composable calls:

```ts
// The credential-email worker outlives this page, so a stored job id is
// restored on mount — a reload must not lose the progress bubble.
const { job: emailJob, state: emailJobState, track: trackEmailJob, restore: restoreEmailJob, dismiss: dismissEmailJob } = useEmailJobStatus()
onMounted(restoreEmailJob)
```

`onMounted` is already imported at line 2. If AdminView already has an `onMounted(...)` call, add `restoreEmailJob()` inside it instead of registering a second one.

Replace the Bulk Import card block (lines 405-410) with:

```html
      <!-- Bulk Import -->
      <div class="card" style="margin-bottom: var(--space-6);">
        <h2 style="margin: 0 0 var(--space-1) 0;">Bulk Roster Import</h2>
        <p class="muted" style="margin: 0 0 var(--space-4) 0;">Import students from CSV or XLSX. Existing students keep their password.</p>
        <BulkImportStudents />
      </div>

      <!-- Rotating credential import (admin-only) -->
      <BulkImportStudentsCredentials @job-started="trackEmailJob" />
```

And add the bubble as the last element inside AdminView's root template element:

```html
    <EmailJobBubble :job="emailJob" :state="emailJobState" @dismiss="dismissEmailJob" />
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd frontend && npx vitest run src/__tests__/components/BulkImportStudentsCredentials.test.ts
cd frontend && npx vitest run && npx vue-tsc -b
```

Expected: the 4 new tests PASS, the full vitest suite stays green, and `vue-tsc` reports no type errors.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/BulkImportStudentsCredentials.vue frontend/src/components/AdminView.vue frontend/src/__tests__/components/BulkImportStudentsCredentials.test.ts
git commit -m "feat(admin): add rotating credential import card and progress bubble wiring"
```

---

### Task 9: Documentation sync

**Files:**
- Modify: `API_SPEC.md`
- Modify: `MANUAL.md`
- Modify: `CHANGELOG.md`
- Modify: `docs/superpowers/specs/2026-08-17-rotating-credential-import-design.md`

**Interfaces:**
- Consumes: the finished behaviour of Tasks 1-8.
- Produces: no code.

- [ ] **Step 1: Document the two endpoints in `API_SPEC.md`**

Add next to the existing `POST /api/v1/lecturer/students/import` entry, following that file's existing formatting:

```markdown
### POST /api/v1/students/import-credentials

Role: **ADMIN only.** Content-Type: `multipart/form-data`.

Fields: `file` (`.csv`/`.xlsx` roster, same columns as
`/lecturer/students/import`), `confirm` (must be the literal `ROTATE`).

Rotates the password of EVERY row — new and returning students alike — bumping
`token_version` so existing sessions are evicted, and emails each credential
from a background worker. Rows with no email address are still imported and
reported with `email_status: "no_email"`.

`400 {"error":"confirmation_required"}` when `confirm` is absent or not exactly
`ROTATE`.

Response `200`:

    {
      "total": 2, "created": 1, "updated": 1, "skipped": 0,
      "errors": [],
      "accounts": [
        {"row":1,"student_number":"N1","student_id_ext":"s1","email":"a@example.com",
         "password":"…","outcome":"created","email_status":"queued"}
      ],
      "job_id": "…"
    }

`job_id` is `""` when no row carried an email address.

### GET /api/v1/students/import-credentials/:job_id/status

Role: **ADMIN only.** Returns the background email job's progress. A job
belonging to a different admin is reported as `404` — indistinguishable from an
unknown id. Progress is in-memory and retained 30 minutes after the job
finishes; a `404` after a server restart is expected.

Response `200`: `{"job_id":"…","total":40,"sent":38,"failed":2,"done":true,"failures":["s7","s9"]}`
```

- [ ] **Step 2: Document the admin workflow in `MANUAL.md`**

In the section covering roster import and admin password actions, add:

```markdown
### Import and email new passwords

The admin page has two roster importers.

**Bulk Roster Import** adds students. Existing students keep their current
password and receive no email.

**Import & Email Passwords** (admin only) gives *every* student in the file a
brand-new password and emails it to them. Returning students are included, so
anyone already signed in is signed out and must log in with the emailed
password — do not run this during an exam. Type `ROTATE` to confirm.

Rows without an email address are still imported; their passwords appear in the
results table for hand-delivery. Download **Copy all as CSV** before leaving the
page — passwords are shown once.

A bubble in the top-right corner tracks the sending progress and reports any
addresses that failed. Closing the page does not stop the send; reopening the
admin page picks the progress back up for up to 30 minutes.
```

- [ ] **Step 3: Add the `CHANGELOG.md` entry**

Under a `[2026-08-17]` heading, matching the file's existing style:

```markdown
### Added
- **Admin rotating credential import.** `POST /api/v1/students/import-credentials`
  (ADMIN only, `confirm=ROTATE`) imports a roster and rotates + emails a fresh
  password for every row, bumping `token_version` so existing sessions are
  evicted. New `UpsertStudentRotating` repository upsert and
  `StudentImportService.ImportRotating`; per-account `app.password_events` rows
  are written via `AuthService.LogBulkCredentials`, which deliberately skips
  `logPasswordEvent` so `ADMIN_EMAIL` does not receive one notice per row.
  Delivery runs on a background worker tracked by an in-memory, owner-scoped
  `EmailJobRegistry`, exposed at
  `GET /api/v1/students/import-credentials/:job_id/status` and surfaced as a
  live progress bubble in AdminView.
  The existing `POST /api/v1/lecturer/students/import` is unchanged.

### Known limitations
- The rotating import has no rate limiter — same accepted residual as
  `POST /api/v1/users/bulk-reset-password`.
- Email progress is in-memory and single-process: it is lost on restart and
  retained only 30 minutes after a job finishes. The saved per-row
  `email_status` stays `"queued"` permanently; there is no durable delivery
  record and no retry of a failed send.
```

- [ ] **Step 4: Record the confirm-input deviation in the spec**

In `docs/superpowers/specs/2026-08-17-rotating-credential-import-design.md`, in the *Frontend design* section, replace the sentence describing the confirm dialog with:

```markdown
Flow: pick file → type `ROTATE` into an inline confirmation input → press
**Import & Email Passwords**, which stays disabled until the phrase matches
exactly. This mirrors `BulkPasswordReset.vue`'s `RESET` gate;
`services/confirm_service.ts`'s `ConfirmOptions` has no text-input field, so a
modal could not carry the typed phrase. The server re-checks the phrase on every
request regardless.
```

- [ ] **Step 5: Verify and commit**

```bash
cd backend && go build ./... && go test -p 1 ./internal/...
cd ../frontend && npx vitest run && npx vue-tsc -b
```

Expected: backend builds and its suites pass; frontend suite green; no type errors.

```bash
git add API_SPEC.md MANUAL.md CHANGELOG.md docs/superpowers/specs/2026-08-17-rotating-credential-import-design.md
git commit -m "docs: document the admin rotating credential import"
```

---

## Verification checklist

Run before declaring the feature done:

```bash
cd backend && go build ./... && go test -p 1 -race ./internal/...
cd ../frontend && npx vitest run && npx vue-tsc -b
```

Manual smoke test against a running stack, logged in as an admin:

1. Import a two-row roster where one row has an email and one does not. Confirm the results table shows `created`/`updated` and `✉ queued` / `no email`, and the bubble appears.
2. Reload the page mid-send — the bubble must come back with the same counts.
3. Re-import the same file. Every row must report `updated`, every password must differ from the first run, and a student who was logged in must be signed out.
4. Attempt the same request with a lecturer account — expect `403`.
