package repository

// Regression-tester (step [7]) coverage for Task 6 of the lecturer-authored
// seed rows feature (2026-08-08-lecturer-seed-rows-design.md, ARCH-045/047):
// grading (evaluateDML) and student preview (evaluateDMLPreview) must replay
// a lecturer's seed_sql exactly like they already replay a derived
// auto_seed plan — the two mechanisms share one entry point,
// applyQuestionSeed, so what dml_autoseed_schema_isolation_test.go already
// proves about schema isolation for the derived plan must hold identically
// for the lecturer-authored one.
//
// Modelled on dml_autoseed_schema_isolation_test.go (read first, fixtures
// reused): openTestRepo(t), mkPracticeCategory, ImportSchemaIntoCategory,
// GetPracticeCategorySchema, countSchemaTableRows. Every test builds a
// models.Question directly (rather than persisting through
// CreatePracticeQuestion/GetPracticeQuestion) because repository.Evaluate /
// EvaluatePreview only need a Question value — this keeps the test entirely
// inside this file's scope (postgres_evaluation.go) and independent of the
// practice/exam persistence paths (Tasks 4/5), which are out of scope here.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/... -run TestSeedSQLEvaluation -v -p 1

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// decodeValidationDetails unmarshals a ValidationResult.ValidationDetails
// (the JSON-encoded post-image row set evaluateDML captures) into a slice of
// row maps, for asserting on the exact rows present after grading.
func decodeValidationDetails(t *testing.T, details json.RawMessage) []map[string]interface{} {
	t.Helper()
	if len(details) == 0 {
		return nil
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(details, &rows); err != nil {
		t.Fatalf("decode ValidationDetails: %v", err)
	}
	return rows
}

func hasSSN(rows []map[string]interface{}, ssn string) bool {
	for _, row := range rows {
		if v, ok := row["ssn"]; ok && fmt.Sprintf("%v", v) == ssn {
			return true
		}
	}
	return false
}

// TestSeedSQLEvaluation_GradingDiscrimination is the point of the whole
// exercise (ARCH-008): a DELETE question seeded via seed_sql (not a derived
// auto_seed plan) must grade a matching DELETE differently from a statement
// that does nothing. The seeded row is present at the start of every
// evaluation (the seed is unconditional and rolled back each time), so the
// discriminator is whether the post-image still contains it.
func TestSeedSQLEvaluation_GradingDiscrimination(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "seedsql_grade")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	const tbl = "employee_seedsql_grade"
	importSQL := fmt.Sprintf(`
		CREATE TABLE %s (ssn TEXT PRIMARY KEY, name TEXT);
		INSERT INTO %s (ssn, name) VALUES ('111111111', 'Alice');`, tbl, tbl)
	if err := repo.ImportSchemaIntoCategory(ctx, catID, importSQL, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	schema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	question := &models.Question{
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		DMLKind:     "DELETE",
		Requirement: "delete the seeded employee",
		SeedSQL:     fmt.Sprintf("INSERT INTO %s (ssn, name) VALUES ('999999999', 'Seeded')", tbl),
	}

	// Matching DELETE: the seeded row must be gone from the post-image.
	correct, err := repo.Evaluate(ctx, uid, *question, question.CorrectSQL, schema)
	if err != nil {
		t.Fatalf("Evaluate (matching DELETE): %v", err)
	}
	if correct.Status != "success" {
		t.Fatalf("Evaluate (matching DELETE): unexpected error status: %s", correct.Feedback)
	}
	if rows := decodeValidationDetails(t, correct.ValidationDetails); hasSSN(rows, "999999999") {
		t.Fatalf("matching DELETE should have consumed the seeded row, but it is still present: %v", rows)
	}

	// No-op statement: the seeded row must still be present in the post-image.
	noop, err := repo.Evaluate(ctx, uid, *question, fmt.Sprintf("DELETE FROM %s WHERE ssn = 'no-such-row'", tbl), schema)
	if err != nil {
		t.Fatalf("Evaluate (no-op): %v", err)
	}
	if noop.Status != "success" {
		t.Fatalf("Evaluate (no-op): unexpected error status: %s", noop.Feedback)
	}
	if rows := decodeValidationDetails(t, noop.ValidationDetails); !hasSSN(rows, "999999999") {
		t.Fatalf("no-op DELETE must leave the seeded row untouched, but it is missing: %v", rows)
	}
}

// TestSeedSQLEvaluation_SchemaIsolation is the core safety guarantee: after
// grading AND previewing a seed_sql-seeded question, the real category
// schema's row count is unchanged and no seeded row remains — the seed always
// runs inside SAVEPOINT student_eval, which is unconditionally rolled back.
func TestSeedSQLEvaluation_SchemaIsolation(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "seedsql_iso")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	const tbl = "employee_seedsql_iso"
	importSQL := fmt.Sprintf(`
		CREATE TABLE %s (ssn TEXT PRIMARY KEY, name TEXT);
		INSERT INTO %s (ssn, name) VALUES ('111111111', 'Alice'), ('222222222', 'Bob');`, tbl, tbl)
	if err := repo.ImportSchemaIntoCategory(ctx, catID, importSQL, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	schema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	baseline := countSchemaTableRows(t, repo, ctx, schema, tbl)
	if baseline != 2 {
		t.Fatalf("fixture precondition: expected 2 baseline rows, got %d", baseline)
	}

	question := &models.Question{
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		DMLKind:     "DELETE",
		Requirement: "delete the seeded employee",
		SeedSQL:     fmt.Sprintf("INSERT INTO %s (ssn, name) VALUES ('999999999', 'Seeded')", tbl),
	}

	for i := 0; i < 3; i++ {
		result, err := repo.Evaluate(ctx, uid, *question, question.CorrectSQL, schema)
		if err != nil {
			t.Fatalf("Evaluate (grade, iter %d): %v", i, err)
		}
		if result.Status != "success" {
			t.Fatalf("Evaluate (grade, iter %d): unexpected error status: %s", i, result.Feedback)
		}
		if got := countSchemaTableRows(t, repo, ctx, schema, tbl); got != baseline {
			t.Fatalf("Evaluate (grade, iter %d): row count changed from %d to %d", i, baseline, got)
		}
	}

	for i := 0; i < 3; i++ {
		pr, err := repo.EvaluatePreview(ctx, uid, *question, question.CorrectSQL, schema, "")
		if err != nil {
			t.Fatalf("EvaluatePreview (iter %d): %v", i, err)
		}
		if pr.Result.Status != "success" {
			t.Fatalf("EvaluatePreview (iter %d): unexpected error status: %s", i, pr.Result.Feedback)
		}
		if got := countSchemaTableRows(t, repo, ctx, schema, tbl); got != baseline {
			t.Fatalf("EvaluatePreview (iter %d): row count changed from %d to %d", i, baseline, got)
		}
	}

	if got := countSchemaTableRows(t, repo, ctx, schema, tbl); got != baseline {
		t.Fatalf("after grading+previewing, expected row count to remain %d, got %d", baseline, got)
	}
	assertSeedRowAbsentBySSN(t, repo, ctx, schema, tbl, "999999999")
}

// TestSeedSQLEvaluation_PreviewReturnsSeededRow proves evaluateDMLPreview
// applies seed_sql (not only evaluateDML), so the student's preview observes
// the same seeded state grading will observe.
func TestSeedSQLEvaluation_PreviewReturnsSeededRow(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "seedsql_preview")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	const tbl = "employee_seedsql_preview"
	importSQL := fmt.Sprintf(`CREATE TABLE %s (ssn TEXT PRIMARY KEY, name TEXT);`, tbl)
	if err := repo.ImportSchemaIntoCategory(ctx, catID, importSQL, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	schema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	question := &models.Question{
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		DMLKind:     "DELETE",
		Requirement: "delete the seeded employee",
		SeedSQL:     fmt.Sprintf("INSERT INTO %s (ssn, name) VALUES ('999999999', 'Seeded')", tbl),
	}

	// A no-op student statement leaves the seeded row in the preview's result
	// set, proving the preview applied the seed before executing the student's
	// SQL, not just that the target table happens to be non-empty.
	pr, err := repo.EvaluatePreview(ctx, uid, *question, fmt.Sprintf("DELETE FROM %s WHERE ssn = 'no-such-row'", tbl), schema, "")
	if err != nil {
		t.Fatalf("EvaluatePreview: %v", err)
	}
	if pr.Result.Status != "success" {
		t.Fatalf("EvaluatePreview: unexpected error status: %s", pr.Result.Feedback)
	}
	if !hasSSN(pr.Rows, "999999999") {
		t.Fatalf("expected the preview's result set to contain the seeded row, got %v", pr.Rows)
	}
}

// TestSeedSQLEvaluation_SeedFailureNotStudentFault covers ARCH-048: a
// seed_sql referencing a dropped column must surface as
// ValidationResult{Status:"error", Feedback: AutoSeedFailureFeedback} with a
// NIL Go error, exactly like a stale derived auto_seed plan — never a
// wrong-answer verdict — and must leave the connection pool usable for a
// subsequent evaluation.
func TestSeedSQLEvaluation_SeedFailureNotStudentFault(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "seedsql_failnotstudent")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	const tbl = "employee_seedsql_failnotstudent"
	importSQL := fmt.Sprintf(`CREATE TABLE %s (ssn TEXT PRIMARY KEY, name TEXT);`, tbl)
	if err := repo.ImportSchemaIntoCategory(ctx, catID, importSQL, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	schema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	question := &models.Question{
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		DMLKind:     "DELETE",
		Requirement: "delete the seeded employee",
		// References a column that does not exist on the imported table —
		// simulates a category re-import that dropped a column seed_sql relies
		// on (design doc "Known limitations" #2).
		SeedSQL: fmt.Sprintf("INSERT INTO %s (ssn, no_such_column) VALUES ('999999999', 'x')", tbl),
	}

	before := countSchemaTableRows(t, repo, ctx, schema, tbl)
	result, err := repo.Evaluate(ctx, uid, *question, question.CorrectSQL, schema)
	if err != nil {
		t.Fatalf("Evaluate must return a nil Go error on a seed failure (a non-nil error surfaces as HTTP 500 with raw internal text), got: %v", err)
	}
	if result.Status != "error" {
		t.Fatalf("expected Status=\"error\" for a broken seed_sql, got %q", result.Status)
	}
	if result.Feedback != AutoSeedFailureFeedback {
		t.Fatalf("expected feedback=%q, got %q", AutoSeedFailureFeedback, result.Feedback)
	}
	after := countSchemaTableRows(t, repo, ctx, schema, tbl)
	if after != before {
		t.Fatalf("a failed seed must not touch the real schema: before=%d after=%d", before, after)
	}

	// The pool connection must be reusable afterwards: a trivial follow-up
	// evaluation on the same schema must succeed cleanly.
	followUp := models.Question{Type: "QUERY", Requirement: "trivial follow-up"}
	followUpResult, err := repo.Evaluate(ctx, uid, followUp, "SELECT 1", schema)
	if err != nil {
		t.Fatalf("follow-up Evaluate after a seed failure: %v", err)
	}
	if followUpResult.Status != "success" {
		t.Fatalf("follow-up Evaluate after a seed failure: expected success, got status=%q feedback=%q", followUpResult.Status, followUpResult.Feedback)
	}
}

// TestSeedSQLEvaluation_SequenceGrantForSerialPK is a regression test for
// ARCH-049: a seed INSERT into a table whose primary key is `serial`,
// omitting that column, must succeed under the exam_student role. Before the
// sequence-usage grant fix this fails with "permission denied for sequence"
// — a defect that predates seed_sql (it already affects the derived
// auto-seed) but that seed_sql hits constantly since the lecturer routinely
// omits a serial primary key.
func TestSeedSQLEvaluation_SequenceGrantForSerialPK(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "seedsql_seqgrant")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	const tbl = "employee_seedsql_seqgrant"
	importSQL := fmt.Sprintf(`
		CREATE TABLE %s (id SERIAL PRIMARY KEY, ssn TEXT UNIQUE NOT NULL, name TEXT);
		INSERT INTO %s (ssn, name) VALUES ('111111111', 'Alice');`, tbl, tbl)
	if err := repo.ImportSchemaIntoCategory(ctx, catID, importSQL, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	schema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	question := &models.Question{
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		DMLKind:     "DELETE",
		Requirement: "delete the seeded employee",
		// Deliberately omits the serial `id` column, forcing a nextval() call
		// under the exam_student role.
		SeedSQL: fmt.Sprintf("INSERT INTO %s (ssn, name) VALUES ('999999999', 'Seeded')", tbl),
	}

	result, err := repo.Evaluate(ctx, uid, *question, question.CorrectSQL, schema)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Status != "success" {
		if strings.Contains(result.Feedback, "permission denied for sequence") || result.Feedback == AutoSeedFailureFeedback {
			t.Fatalf("FINDING: seed INSERT into a serial-PK table was denied sequence usage under exam_student — the ARCH-049 grant fix in postgres_schema.go is not reaching this schema: %s", result.Feedback)
		}
		t.Fatalf("Evaluate: unexpected error status: %s", result.Feedback)
	}
}
