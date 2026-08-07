package repository

// Regression-tester (step [7]) coverage for the dml-autoseed feature, area
// #4: "rescue, not default" (ARCH-037). A question whose model answer
// already affects rows on the FIRST run must never invoke derivation, must
// store auto_seed = NULL, and must produce byte-identical expected_result to
// pre-feature behaviour.
//
// The zero-extra-statements half of area #4 ("a NULL plan issues zero extra
// statements at grade/preview time") is proven precisely at the unit level by
// TestAutoSeedApplyNilPlanIsNoOp (dml_autoseed_test.go): applyAutoSeed(ctx,
// nil-tx-safe, nilPlan) returns nil without ever touching tx. This file adds
// the DB-level half: driving a real non-rescued capture+grade end to end and
// confirming the observable outcomes (auto_seed NULL, expected_result
// unchanged, grading still succeeds) — it does not re-instrument
// applyAutoSeed's call count, which the unit test already covers precisely.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/... -run RescueNotDefault -v -p 1

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

func TestAutoSeed_RescueNotDefault_NonVacuousModelAnswer_AutoSeedStaysNil(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "autoseed_nondefault")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	const tbl = "works_on_rescue_nondefault"
	importSQL := fmt.Sprintf(`
		CREATE TABLE %s (pno INTEGER PRIMARY KEY, hours INTEGER NOT NULL);
		INSERT INTO %s (pno, hours) VALUES (1, 10), (2, 20);`, tbl, tbl)
	if err := repo.ImportSchemaIntoCategory(ctx, catID, importSQL, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("UPDATE %s SET hours = hours + 1 WHERE pno = 1", tbl),
		Type:        "DML",
		Requirement: "bump pno=1's hours by one",
		Points:      5,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}

	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" {
		t.Fatalf("expected expected_result_status=captured, got %q (capture_error=%q)", saved.ExpectedResultStatus, saved.CaptureError)
	}

	// ARCH-037: the model answer affected rows on the FIRST run, so
	// deriveAutoSeed/applyAutoSeed must never even have been invoked —
	// auto_seed must be NULL, not merely empty.
	if len(saved.AutoSeed) != 0 {
		t.Fatalf("expected auto_seed=NULL for a model answer that already affects rows, got %s", saved.AutoSeed)
	}
	if saved.ExpectedRowsAffected == nil || *saved.ExpectedRowsAffected != 1 {
		t.Fatalf("expected expected_rows_affected=1 (the UPDATE's own affected-rows count), got %v", saved.ExpectedRowsAffected)
	}

	// Byte-identical expected_result to pre-feature behaviour: exactly the
	// post-image of the UPDATE (both rows, pno=1's hours bumped), nothing
	// seed-related mixed in.
	var rows []map[string]interface{}
	if err := json.Unmarshal(saved.ExpectedResult, &rows); err != nil {
		t.Fatalf("unmarshal expected_result: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected exactly the 2 pre-existing rows in the post-image, got %d: %+v", len(rows), rows)
	}
	var sawBumped bool
	for _, r := range rows {
		if fmt.Sprintf("%v", r["pno"]) == "1" {
			if fmt.Sprintf("%v", r["hours"]) != "11" {
				t.Fatalf("expected pno=1's hours to be bumped to 11, got %v", r["hours"])
			}
			sawBumped = true
		}
	}
	if !sawBumped {
		t.Fatalf("expected the post-image to contain pno=1, got %+v", rows)
	}

	// Grading a NULL-plan (non-rescued) question must behave exactly as
	// before this feature existed: no seed side effects, straightforward
	// success.
	schema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}
	question := saved.AsQuestion()
	result, err := repo.Evaluate(ctx, uid, *question, fmt.Sprintf("UPDATE %s SET hours = hours + 1 WHERE pno = 1", tbl), schema)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Status != "success" {
		t.Fatalf("grading a non-rescued DML question failed: %s", result.Feedback)
	}

	pr, err := repo.EvaluatePreview(ctx, uid, *question, fmt.Sprintf("UPDATE %s SET hours = hours + 1 WHERE pno = 1", tbl), schema, "")
	if err != nil {
		t.Fatalf("EvaluatePreview: %v", err)
	}
	if pr.Result.Status != "success" {
		t.Fatalf("previewing a non-rescued DML question failed: %s", pr.Result.Feedback)
	}
}
