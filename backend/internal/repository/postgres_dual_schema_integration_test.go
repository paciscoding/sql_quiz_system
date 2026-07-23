package repository

// Integration tests for ARCH-017/TODO-019: end-to-end proof of the
// dual-schema-grading FEATURE ITSELF (spec 2026-07-22-dual-schema-grading-design.md),
// which no other test file proves end-to-end. Everything already covered
// elsewhere is deliberately NOT repeated here:
//   - services/validation_service_dual_leak_test.go: the ValidationService.Validate
//     scrub of ValidationDetails/Columns/Feedback for dual questions.
//   - controllers/dual_leak_payload_test.go: dual payloads omit
//     capture_error/expected_rows_affected.
//   - repository/postgres_preview_role_test.go: the ARCH-020 preview-role
//     existence/idempotency/grant-topology and the schema-qualified-read-of-B
//     exploit proofs (using synthetic, uncaptured models.Question values).
//   - repository/postgres_preview_schema_delete_test.go: orphaned decoy drop
//     on hard delete.
//
// What THIS file proves that nothing above does: a full CreatePracticeQuestion /
// CreateQuestion round trip (real capture against B) followed by BOTH
// repo.Evaluate (grading) and repo.EvaluatePreview (preview) on the SAME
// question, asserting the preview surfaces A's distinguishable content while
// the grading path's raw output matches B's captured expected_result — i.e.
// the split the whole feature exists for, driven through the real capture
// pipeline rather than a hand-built models.Question.
//
// All tests use openTestRepo(t) (shared with postgres_password_events_test.go)
// and SKIP cleanly when TEST_DATABASE_URL is unset, so `go test ./...` stays
// green on a machine with no database. Every test creates its own scratch
// practice category / examination and cleans it up on return.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/... -run DualSchema

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// mkExamWithAuthor creates a lecturer + a fresh examination and returns
// (examID, lecturerUserID). mkExam (postgres_preview_schema_delete_test.go)
// only returns the exam id, which is not enough here: curriculum/decoy
// imports need the author's user id too.
func mkExamWithAuthor(t *testing.T, repo *PostgresRepository, ctx context.Context, namePrefix string) (examID, uid int) {
	t.Helper()
	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername(namePrefix + "_author"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	examID, err = repo.CreateExamination(ctx, models.Examination{
		Title:     uniqueUsername(namePrefix + "_exam"),
		CreatedBy: models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateExamination: %v", err)
	}
	return examID, uid
}

// rowVal extracts a single-row, single-column value from a []map[string]interface{}
// JSON-decoded row set, failing the test if the shape doesn't match exactly one row.
func rowVal(t *testing.T, rows []map[string]interface{}, col string) interface{} {
	t.Helper()
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 row, got %d: %+v", len(rows), rows)
	}
	v, ok := rows[0][col]
	if !ok {
		t.Fatalf("row missing column %q: %+v", col, rows[0])
	}
	return v
}

func unmarshalRows(t *testing.T, raw json.RawMessage) []map[string]interface{} {
	t.Helper()
	var rows []map[string]interface{}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("unmarshal rows: %v", err)
	}
	return rows
}

// normalizeRows JSON round-trips a raw []map[string]interface{} (as returned
// directly by pgx row-scanning, e.g. PreviewResult.Rows, where a Postgres
// INTEGER decodes to a Go int32) into the same shape json.Unmarshal produces
// (where every JSON number decodes to float64), so it can be compared with
// reflect.DeepEqual against a json.RawMessage-derived row set without a false
// mismatch purely from differing Go numeric types.
func normalizeRows(t *testing.T, rows []map[string]interface{}) []map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal rows for normalization: %v", err)
	}
	return unmarshalRows(t, raw)
}

// ─────────────────────────────────────────────────────────────────────────────
// (a) Dual QUERY, practice: preview=A, grade=B
// ─────────────────────────────────────────────────────────────────────────────

// TestDualSchema_Practice_QUERY_PreviewA_GradeB is the core positive proof for
// practice QUERY: a captured dual question's preview reads the decoy schema A
// (distinguishable content) while the raw grading evaluation reads the real
// schema B and matches the captured expected_result byte-for-byte.
func TestDualSchema_Practice_QUERY_PreviewA_GradeB(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "dualq_practice")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE secret (id INTEGER PRIMARY KEY, val TEXT NOT NULL); INSERT INTO secret VALUES (1, 'B-real-data');",
		uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	if _, err := repo.ImportSchemaIntoCategoryPreview(ctx, catID,
		"CREATE TABLE secret (id INTEGER PRIMARY KEY, val TEXT NOT NULL); INSERT INTO secret VALUES (1, 'A-decoy-data');",
		uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategoryPreview: %v", err)
	}

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		CorrectSQL:  "SELECT * FROM secret",
		Type:        "QUERY",
		Requirement: "return every row of secret",
		Points:      5,
		DualSchema:  true,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}

	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" {
		t.Fatalf("expected captured status, got %q (capture_error=%q)", saved.ExpectedResultStatus, saved.CaptureError)
	}
	if !saved.DualSchema {
		t.Fatal("expected DualSchema=true to round-trip")
	}
	if saved.CategoryPreviewSchema == "" {
		t.Fatal("expected CategoryPreviewSchema to be populated after decoy import")
	}
	// Sanity: capture ran against B, never A.
	if rowVal(t, unmarshalRows(t, saved.ExpectedResult), "val") != "B-real-data" {
		t.Fatalf("expected captured expected_result to hold B's data, got %s", saved.ExpectedResult)
	}

	question := saved.AsQuestion()

	// Grading: same query against B (the schema the student's grading eval
	// always targets), should reproduce exactly the captured expected_result.
	gradeResult, err := repo.Evaluate(ctx, uid, *question, "SELECT * FROM secret", saved.CategorySchema)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if gradeResult.Status != "success" {
		t.Fatalf("grading evaluation failed: %s", gradeResult.Feedback)
	}
	if rowVal(t, unmarshalRows(t, gradeResult.ValidationDetails), "val") != "B-real-data" {
		t.Fatalf("expected grading ValidationDetails to hold B's data, got %s", gradeResult.ValidationDetails)
	}
	if !reflect.DeepEqual(unmarshalRows(t, gradeResult.ValidationDetails), unmarshalRows(t, saved.ExpectedResult)) {
		t.Fatalf("grading output %s does not match captured expected_result %s", gradeResult.ValidationDetails, saved.ExpectedResult)
	}

	// Preview: same query, resolved to the decoy schema A - must return A's
	// distinguishable content, never B's.
	pr, err := repo.EvaluatePreview(ctx, uid, *question, "SELECT * FROM secret", saved.CategorySchema, saved.CategoryPreviewSchema)
	if err != nil {
		t.Fatalf("EvaluatePreview: %v", err)
	}
	if pr.Result.Status != "success" {
		t.Fatalf("preview evaluation failed: %s", pr.Result.Feedback)
	}
	if len(pr.Rows) != 1 || pr.Rows[0]["val"] != "A-decoy-data" {
		t.Fatalf("expected preview to surface A's decoy row, got %+v", pr.Rows)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (b) Dual QUERY, exam: preview=A, grade=B
// ─────────────────────────────────────────────────────────────────────────────

func TestDualSchema_Exam_QUERY_PreviewA_GradeB(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, uid := mkExamWithAuthor(t, repo, ctx, "dualq_exam")
	defer func() { _ = repo.DeleteExamination(ctx, examID) }()

	if err := repo.ImportSchemaIntoExamCurriculum(ctx, examID,
		"CREATE TABLE secret (id INTEGER PRIMARY KEY, val TEXT NOT NULL); INSERT INTO secret VALUES (1, 'B-real-data');",
		uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCurriculum: %v", err)
	}
	if _, err := repo.ImportSchemaIntoExamCurriculumPreview(ctx, examID,
		"CREATE TABLE secret (id INTEGER PRIMARY KEY, val TEXT NOT NULL); INSERT INTO secret VALUES (1, 'A-decoy-data');",
		uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCurriculumPreview: %v", err)
	}

	eid := models.ExamID(examID)
	newID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID:      &eid,
		CorrectSQL:  "SELECT * FROM secret",
		Type:        "QUERY",
		Points:      5,
		Requirement: "return every row of secret",
		CreatedBy:   models.UserID(uid),
		DualSchema:  true,
	})
	if err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}

	saved, err := repo.GetQuestion(ctx, newID)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" {
		t.Fatalf("expected captured status, got %q (capture_error=%q)", saved.ExpectedResultStatus, saved.CaptureError)
	}
	if !saved.DualSchema {
		t.Fatal("expected DualSchema=true to round-trip")
	}
	if rowVal(t, unmarshalRows(t, saved.ExpectedResult), "val") != "B-real-data" {
		t.Fatalf("expected captured expected_result to hold B's data, got %s", saved.ExpectedResult)
	}

	previewSchema, err := repo.GetExamPreviewCurriculumSchema(ctx, examID)
	if err != nil {
		t.Fatalf("GetExamPreviewCurriculumSchema: %v", err)
	}
	if previewSchema == "" {
		t.Fatal("expected a resolved decoy curriculum schema after import")
	}

	// Grading: querySchemaFor(question, sandboxSchema) resolves via
	// question.CurriculumSchema (real B) regardless of the sandboxSchema arg
	// for QUERY questions - pass the exam sandbox name exactly as the
	// production caller (quiz_controller.SubmitAnswer) does.
	gradeResult, err := repo.Evaluate(ctx, uid, saved, "SELECT * FROM secret", ExamSandboxName(examID))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if gradeResult.Status != "success" {
		t.Fatalf("grading evaluation failed: %s", gradeResult.Feedback)
	}
	if rowVal(t, unmarshalRows(t, gradeResult.ValidationDetails), "val") != "B-real-data" {
		t.Fatalf("expected grading ValidationDetails to hold B's data, got %s", gradeResult.ValidationDetails)
	}
	if !reflect.DeepEqual(unmarshalRows(t, gradeResult.ValidationDetails), unmarshalRows(t, saved.ExpectedResult)) {
		t.Fatalf("grading output %s does not match captured expected_result %s", gradeResult.ValidationDetails, saved.ExpectedResult)
	}

	pr, err := repo.EvaluatePreview(ctx, uid, saved, "SELECT * FROM secret", ExamSandboxName(examID), previewSchema)
	if err != nil {
		t.Fatalf("EvaluatePreview: %v", err)
	}
	if pr.Result.Status != "success" {
		t.Fatalf("preview evaluation failed: %s", pr.Result.Feedback)
	}
	if len(pr.Rows) != 1 || pr.Rows[0]["val"] != "A-decoy-data" {
		t.Fatalf("expected preview to surface A's decoy row, got %+v", pr.Rows)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (c) Dual DML, practice: A post-image previewed, graded on B
// ─────────────────────────────────────────────────────────────────────────────

func TestDualSchema_Practice_DML_PreviewA_GradeB(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "dualdml_practice")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE accounts (id INTEGER PRIMARY KEY, balance INTEGER NOT NULL); INSERT INTO accounts VALUES (1, 100);",
		uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	// Decoy starts from a deliberately different balance so the post-image
	// diverges from B's post-image even though the same DML runs on both.
	if _, err := repo.ImportSchemaIntoCategoryPreview(ctx, catID,
		"CREATE TABLE accounts (id INTEGER PRIMARY KEY, balance INTEGER NOT NULL); INSERT INTO accounts VALUES (1, 999);",
		uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategoryPreview: %v", err)
	}

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		TargetTable: "accounts",
		CorrectSQL:  "UPDATE accounts SET balance = balance + 1 WHERE id = 1",
		Type:        "DML",
		Requirement: "bump id=1's balance by one",
		Points:      5,
		DualSchema:  true,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}

	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" {
		t.Fatalf("expected captured status, got %q (capture_error=%q)", saved.ExpectedResultStatus, saved.CaptureError)
	}
	wantBBalance := rowVal(t, unmarshalRows(t, saved.ExpectedResult), "balance")
	if fmt.Sprintf("%v", wantBBalance) != "101" {
		t.Fatalf("expected captured B post-image balance=101, got %v", wantBBalance)
	}

	question := saved.AsQuestion()
	studentSQL := "UPDATE accounts SET balance = balance + 1 WHERE id = 1"

	gradeResult, err := repo.Evaluate(ctx, uid, *question, studentSQL, saved.CategorySchema)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if gradeResult.Status != "success" {
		t.Fatalf("grading evaluation failed: %s", gradeResult.Feedback)
	}
	gotBBalance := rowVal(t, unmarshalRows(t, gradeResult.ValidationDetails), "balance")
	if fmt.Sprintf("%v", gotBBalance) != "101" {
		t.Fatalf("expected graded B post-image balance=101, got %v", gotBBalance)
	}

	pr, err := repo.EvaluatePreview(ctx, uid, *question, studentSQL, saved.CategorySchema, saved.CategoryPreviewSchema)
	if err != nil {
		t.Fatalf("EvaluatePreview: %v", err)
	}
	if pr.Result.Status != "success" {
		t.Fatalf("preview evaluation failed: %s", pr.Result.Feedback)
	}
	gotABalance := rowVal(t, pr.Rows, "balance")
	if fmt.Sprintf("%v", gotABalance) != "1000" {
		t.Fatalf("expected preview A post-image balance=1000 (999+1), got %v", gotABalance)
	}

	// Confirm B's real row is genuinely unaffected by the preview's write
	// (the preview always rolls back its own schema's savepoint).
	var realBalance int
	if err := repo.pool.QueryRow(ctx, fmt.Sprintf("SELECT balance FROM %s.accounts WHERE id = 1", saved.CategorySchema)).Scan(&realBalance); err != nil {
		t.Fatalf("read back B's real balance: %v", err)
	}
	if realBalance != 100 {
		t.Fatalf("expected B's real (ungraded, unpreviewed) balance to remain 100, got %d", realBalance)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (d) Dual DML, exam: preview sandbox cloned from the decoy curriculum, graded
//     on the real sandbox
// ─────────────────────────────────────────────────────────────────────────────

func TestDualSchema_Exam_DML_PreviewA_GradeB(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, uid := mkExamWithAuthor(t, repo, ctx, "dualdml_exam")
	defer func() { _ = repo.DeleteExamination(ctx, examID) }()

	if err := repo.ImportSchemaIntoExamCurriculum(ctx, examID,
		"CREATE TABLE accounts (id INTEGER PRIMARY KEY, balance INTEGER NOT NULL); INSERT INTO accounts VALUES (1, 100);",
		uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCurriculum: %v", err)
	}
	if _, err := repo.ImportSchemaIntoExamCurriculumPreview(ctx, examID,
		"CREATE TABLE accounts (id INTEGER PRIMARY KEY, balance INTEGER NOT NULL); INSERT INTO accounts VALUES (1, 999);",
		uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCurriculumPreview: %v", err)
	}

	// Real sandbox clones from the real curriculum (balance=100).
	if err := repo.InitializeExamSandbox(ctx, examID); err != nil {
		t.Fatalf("InitializeExamSandbox: %v", err)
	}
	// Preview sandbox clones from the decoy curriculum (balance=999).
	if err := repo.EnsureExamPreviewSandboxReady(ctx, examID); err != nil {
		t.Fatalf("EnsureExamPreviewSandboxReady: %v", err)
	}

	eid := models.ExamID(examID)
	newID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID:      &eid,
		TargetTable: "accounts",
		CorrectSQL:  "UPDATE accounts SET balance = balance + 1 WHERE id = 1",
		Type:        "DML",
		Points:      5,
		Requirement: "bump id=1's balance by one",
		CreatedBy:   models.UserID(uid),
		DualSchema:  true,
	})
	if err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}

	saved, err := repo.GetQuestion(ctx, newID)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" {
		t.Fatalf("expected captured status, got %q (capture_error=%q)", saved.ExpectedResultStatus, saved.CaptureError)
	}
	wantBBalance := rowVal(t, unmarshalRows(t, saved.ExpectedResult), "balance")
	if fmt.Sprintf("%v", wantBBalance) != "101" {
		t.Fatalf("expected captured B post-image balance=101, got %v", wantBBalance)
	}

	studentSQL := "UPDATE accounts SET balance = balance + 1 WHERE id = 1"

	gradeResult, err := repo.Evaluate(ctx, uid, saved, studentSQL, ExamSandboxName(examID))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if gradeResult.Status != "success" {
		t.Fatalf("grading evaluation failed: %s", gradeResult.Feedback)
	}
	gotBBalance := rowVal(t, unmarshalRows(t, gradeResult.ValidationDetails), "balance")
	if fmt.Sprintf("%v", gotBBalance) != "101" {
		t.Fatalf("expected graded B post-image balance=101, got %v", gotBBalance)
	}

	pr, err := repo.EvaluatePreview(ctx, uid, saved, studentSQL, ExamSandboxName(examID), ExamPreviewSandboxName(examID))
	if err != nil {
		t.Fatalf("EvaluatePreview: %v", err)
	}
	if pr.Result.Status != "success" {
		t.Fatalf("preview evaluation failed: %s", pr.Result.Feedback)
	}
	gotABalance := rowVal(t, pr.Rows, "balance")
	if fmt.Sprintf("%v", gotABalance) != "1000" {
		t.Fatalf("expected preview A post-image balance=1000 (999+1), got %v", gotABalance)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (e) Decoy-import parity warning
// ─────────────────────────────────────────────────────────────────────────────

// TestImportSchemaIntoCategoryPreview_ParityWarning proves a structurally
// mismatched decoy (missing a table B has) still imports successfully but
// returns a non-empty parityWarning, and preview_schema_name is still stamped.
func TestImportSchemaIntoCategoryPreview_ParityWarning(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "parity_practice")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE t1 (id INTEGER PRIMARY KEY); CREATE TABLE t2 (id INTEGER PRIMARY KEY);",
		uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}

	// Decoy deliberately omits t2 - a structural mismatch against B.
	parityWarning, err := repo.ImportSchemaIntoCategoryPreview(ctx, catID,
		"CREATE TABLE t1 (id INTEGER PRIMARY KEY);",
		uid, false)
	if err != nil {
		t.Fatalf("ImportSchemaIntoCategoryPreview: %v", err)
	}
	if parityWarning == "" {
		t.Fatal("expected a non-empty parity warning for a decoy missing table t2")
	}

	previewSchema := PracticeCategoryPreviewSchemaName(catID)
	if !schemaExists(t, repo, ctx, previewSchema) {
		t.Errorf("expected decoy schema %q to exist despite the parity warning", previewSchema)
	}
	got, err := repo.GetPracticeCategoryPreviewSchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategoryPreviewSchema: %v", err)
	}
	if got != previewSchema {
		t.Errorf("expected preview_schema_name to be stamped to %q despite the parity warning, got %q", previewSchema, got)
	}
}

// TestImportSchemaIntoExamCurriculumPreview_ParityWarning is the exam-side
// analogue: a decoy curriculum with a differing column set on a shared table
// still imports and stamps preview_curriculum_schema, but reports the
// mismatch.
func TestImportSchemaIntoExamCurriculumPreview_ParityWarning(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, uid := mkExamWithAuthor(t, repo, ctx, "parity_exam")
	defer func() { _ = repo.DeleteExamination(ctx, examID) }()

	if err := repo.ImportSchemaIntoExamCurriculum(ctx, examID,
		"CREATE TABLE t1 (id INTEGER PRIMARY KEY, name TEXT NOT NULL);",
		uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCurriculum: %v", err)
	}

	// Decoy's t1 has a different column set (missing "name").
	parityWarning, err := repo.ImportSchemaIntoExamCurriculumPreview(ctx, examID,
		"CREATE TABLE t1 (id INTEGER PRIMARY KEY);",
		uid, false)
	if err != nil {
		t.Fatalf("ImportSchemaIntoExamCurriculumPreview: %v", err)
	}
	if parityWarning == "" {
		t.Fatal("expected a non-empty parity warning for a decoy t1 with a different column set")
	}

	previewSchema := ExamPreviewCurriculumSchemaName(examID)
	if !schemaExists(t, repo, ctx, previewSchema) {
		t.Errorf("expected decoy curriculum schema %q to exist despite the parity warning", previewSchema)
	}
	got, err := repo.GetExamPreviewCurriculumSchema(ctx, examID)
	if err != nil {
		t.Fatalf("GetExamPreviewCurriculumSchema: %v", err)
	}
	if got != previewSchema {
		t.Errorf("expected preview_curriculum_schema to be stamped to %q despite the parity warning, got %q", previewSchema, got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (f) DML eval lock keyed on the PREVIEW schema
// ─────────────────────────────────────────────────────────────────────────────

// TestEvaluatePreview_DMLLock_KeyedOnPreviewSchema adapts the
// postgres_practice_reset_lock_test.go / postgres_dml_grading_integration_test.go
// shared-vs-exclusive advisory lock idiom to prove acquireSchemaEvalLock
// inside evaluateDMLPreview is taken on the RESOLVED schema (the decoy A when
// previewSchema is non-empty), not on sandboxSchema (B): holding the decoy's
// exclusive rebuild lock blocks a concurrent EvaluatePreview against that
// decoy, while an EvaluatePreview against the unrelated real schema B does
// not block.
func TestEvaluatePreview_DMLLock_KeyedOnPreviewSchema(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "lock_preview")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE t (id INTEGER PRIMARY KEY, val INTEGER NOT NULL); INSERT INTO t VALUES (1, 100);",
		uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	if _, err := repo.ImportSchemaIntoCategoryPreview(ctx, catID,
		"CREATE TABLE t (id INTEGER PRIMARY KEY, val INTEGER NOT NULL); INSERT INTO t VALUES (1, 999);",
		uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategoryPreview: %v", err)
	}
	realSchema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}
	decoySchema, err := repo.GetPracticeCategoryPreviewSchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategoryPreviewSchema: %v", err)
	}

	// Hold the DECOY schema's exclusive rebuild lock in a separate connection,
	// exactly as a confirm-replace import would while holding it.
	lockConn, err := repo.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire lock conn: %v", err)
	}
	defer lockConn.Release()
	lockTx, err := lockConn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock tx: %v", err)
	}
	if _, err := lockTx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", schemaRebuildLockKey(decoySchema)); err != nil {
		t.Fatalf("take exclusive lock on decoy: %v", err)
	}

	q := models.Question{Type: "DML", DualSchema: true, TargetTable: "t"}
	studentSQL := "UPDATE t SET val = val + 1 WHERE id = 1"

	// (1) A preview against the DECOY (previewSchema=decoySchema) must block
	// while the exclusive lock is held on decoySchema.
	done := make(chan error, 1)
	go func() {
		_, evalErr := repo.EvaluatePreview(ctx, uid, q, studentSQL, realSchema, decoySchema)
		done <- evalErr
	}()

	select {
	case err := <-done:
		t.Fatalf("EvaluatePreview(previewSchema=decoy) completed while the decoy's exclusive lock was held (err=%v); the DML eval lock is not keyed on the preview schema", err)
	case <-time.After(400 * time.Millisecond):
		// Correctly blocked.
	}

	// (2) Meanwhile, a preview that resolves to the unrelated REAL schema B
	// (previewSchema="") must NOT block, proving the lock is per-schema, not
	// global.
	unrelatedResult, err := repo.EvaluatePreview(ctx, uid, q, studentSQL, realSchema, "")
	if err != nil {
		t.Fatalf("EvaluatePreview(previewSchema=\"\", against B): %v", err)
	}
	if unrelatedResult.Result.Status != "success" {
		t.Fatalf("expected the unrelated real-schema preview to succeed while the decoy lock is held, got status=%q feedback=%q", unrelatedResult.Result.Status, unrelatedResult.Result.Feedback)
	}

	// Release the decoy lock; the blocked preview must now proceed.
	if err := lockTx.Rollback(ctx); err != nil {
		t.Fatalf("rollback lock tx: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EvaluatePreview(previewSchema=decoy) failed after lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EvaluatePreview(previewSchema=decoy) did not complete after the decoy lock was released")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (g) Dual-without-decoy (spec 6.1): preview AND grade both land on B
// ─────────────────────────────────────────────────────────────────────────────

// TestDualSchema_Practice_NoDecoy_PreviewAndGradeBothOnB proves the resolver
// pattern's precondition: with no decoy ever imported,
// GetPracticeCategoryPreviewSchema returns "", so a caller following the
// pinned resolver pattern (practice_controller.Preview) passes
// previewSchema="" to EvaluatePreview - which then returns B's rows,
// identical to what repo.Evaluate (grading) returns. The student is never
// blocked and never sees a different dataset.
func TestDualSchema_Practice_NoDecoy_PreviewAndGradeBothOnB(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "nodecoy_practice")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE secret (id INTEGER PRIMARY KEY, val TEXT NOT NULL); INSERT INTO secret VALUES (1, 'B-only-data');",
		uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	// Deliberately NO ImportSchemaIntoCategoryPreview call.

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		CorrectSQL:  "SELECT * FROM secret",
		Type:        "QUERY",
		Requirement: "return every row of secret",
		Points:      5,
		DualSchema:  true,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}
	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" {
		t.Fatalf("expected captured status, got %q (capture_error=%q)", saved.ExpectedResultStatus, saved.CaptureError)
	}
	if saved.CategoryPreviewSchema != "" {
		t.Fatalf("test setup error: expected no decoy schema, got %q", saved.CategoryPreviewSchema)
	}
	preview, err := repo.GetPracticeCategoryPreviewSchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategoryPreviewSchema: %v", err)
	}
	if preview != "" {
		t.Fatalf("expected GetPracticeCategoryPreviewSchema to return \"\" with no decoy imported, got %q", preview)
	}

	question := saved.AsQuestion()

	gradeResult, err := repo.Evaluate(ctx, uid, *question, "SELECT * FROM secret", saved.CategorySchema)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if gradeResult.Status != "success" {
		t.Fatalf("grading evaluation failed: %s", gradeResult.Feedback)
	}

	// Resolver pattern: previewSchema stays "" because GetPracticeCategoryPreviewSchema
	// returned "" above - never blocking, never told, per spec 6.1.
	pr, err := repo.EvaluatePreview(ctx, uid, *question, "SELECT * FROM secret", saved.CategorySchema, "")
	if err != nil {
		t.Fatalf("EvaluatePreview: %v", err)
	}
	if pr.Result.Status != "success" {
		t.Fatalf("preview evaluation failed (student must never be blocked by a missing decoy): %s", pr.Result.Feedback)
	}
	if len(pr.Rows) != 1 || pr.Rows[0]["val"] != "B-only-data" {
		t.Fatalf("expected the no-decoy fallback preview to surface B's own row, got %+v", pr.Rows)
	}
	if !reflect.DeepEqual(unmarshalRows(t, gradeResult.ValidationDetails), normalizeRows(t, pr.Rows)) {
		t.Fatalf("expected preview and grade to be identical when no decoy exists: grade=%s preview=%+v", gradeResult.ValidationDetails, pr.Rows)
	}
}

// TestDualSchema_Exam_NoDecoy_PreviewSchemaResolverReturnsEmpty is the exam
// analogue of the resolver precondition: with no decoy curriculum imported,
// GetExamPreviewCurriculumSchema returns "", so the exam preview resolver
// (quiz_controller.PreviewAnswer) falls back to previewSchema="" and
// EvaluatePreview lands on the exam's real curriculum (B), matching grading.
func TestDualSchema_Exam_NoDecoy_PreviewSchemaResolverReturnsEmpty(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, uid := mkExamWithAuthor(t, repo, ctx, "nodecoy_exam")
	defer func() { _ = repo.DeleteExamination(ctx, examID) }()

	if err := repo.ImportSchemaIntoExamCurriculum(ctx, examID,
		"CREATE TABLE secret (id INTEGER PRIMARY KEY, val TEXT NOT NULL); INSERT INTO secret VALUES (1, 'B-only-data');",
		uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCurriculum: %v", err)
	}
	// Deliberately NO ImportSchemaIntoExamCurriculumPreview call.

	got, err := repo.GetExamPreviewCurriculumSchema(ctx, examID)
	if err != nil {
		t.Fatalf("GetExamPreviewCurriculumSchema: %v", err)
	}
	if got != "" {
		t.Fatalf("expected GetExamPreviewCurriculumSchema to return \"\" with no decoy imported, got %q", got)
	}

	eid := models.ExamID(examID)
	newID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID:      &eid,
		CorrectSQL:  "SELECT * FROM secret",
		Type:        "QUERY",
		Points:      5,
		Requirement: "return every row of secret",
		CreatedBy:   models.UserID(uid),
		DualSchema:  true,
	})
	if err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}
	saved, err := repo.GetQuestion(ctx, newID)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}

	gradeResult, err := repo.Evaluate(ctx, uid, saved, "SELECT * FROM secret", ExamSandboxName(examID))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if gradeResult.Status != "success" {
		t.Fatalf("grading evaluation failed: %s", gradeResult.Feedback)
	}

	pr, err := repo.EvaluatePreview(ctx, uid, saved, "SELECT * FROM secret", ExamSandboxName(examID), "" /* resolver found no decoy */)
	if err != nil {
		t.Fatalf("EvaluatePreview: %v", err)
	}
	if pr.Result.Status != "success" {
		t.Fatalf("preview evaluation failed (student must never be blocked by a missing decoy): %s", pr.Result.Feedback)
	}
	if len(pr.Rows) != 1 || pr.Rows[0]["val"] != "B-only-data" {
		t.Fatalf("expected the no-decoy fallback preview to surface B's own row, got %+v", pr.Rows)
	}
	if !reflect.DeepEqual(unmarshalRows(t, gradeResult.ValidationDetails), normalizeRows(t, pr.Rows)) {
		t.Fatalf("expected preview and grade to be identical when no decoy exists: grade=%s preview=%+v", gradeResult.ValidationDetails, pr.Rows)
	}
}
