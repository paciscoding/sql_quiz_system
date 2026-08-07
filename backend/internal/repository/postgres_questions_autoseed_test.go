package repository

// Regression-tester (step [7]) coverage for the dml-autoseed feature, area
// #5: postgres_questions.go's exam-question capture path had NO DB test
// coverage at all — its implementor could not run DB tests and the validator
// only ran them ad hoc. Mirrors the practice-side coverage in
// dml_autoseed_schema_isolation_test.go / dml_autoseed_rescue_not_default_test.go,
// using the mkExamAndTable-style "table in the public schema" fixture
// convention already established by postgres_questions_id_test.go
// (TestCreateQuestion_AssignsDistinctIds) for exam CreateQuestion tests.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/... -run ExamCapture_AutoSeed -v -p 1

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// mkExamAndDeleteTable seeds a lecturer, an examination, and a real capture
// table (ssn TEXT PRIMARY KEY, salary INTEGER) in the public schema — the
// default Phase-0 capture schema for an exam with no per-exam curriculum
// import — with exactly one row (ssn='111111111'), so a DELETE naming a
// DIFFERENT ssn is a genuine 0-row rescue candidate. tbl is unique per call
// (keyed off examID) to avoid the pgx statement-cache collision documented in
// dml_autoseed_schema_isolation_test.go's header comment.
func mkExamAndDeleteTable(t *testing.T, repo *PostgresRepository, ctx context.Context, prefix string) (examID int, tbl string, uid int) {
	t.Helper()
	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername(prefix + "_author"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	examID, err = repo.CreateExamination(ctx, models.Examination{Title: uniqueUsername(prefix + "_exam"), CreatedBy: models.UserID(uid)})
	if err != nil {
		t.Fatalf("CreateExamination: %v", err)
	}
	tbl = fmt.Sprintf("autoseed_q_%d", examID)
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf(
		"CREATE TABLE public.%s (ssn TEXT PRIMARY KEY, salary INTEGER); INSERT INTO public.%s (ssn, salary) VALUES ('111111111', 5000)", tbl, tbl)); err != nil {
		t.Fatalf("create capture table: %v", err)
	}
	t.Cleanup(func() {
		repo.pool.Exec(context.Background(), "DROP TABLE IF EXISTS public."+tbl)
		_ = repo.DeleteExamination(context.Background(), examID)
	})
	return examID, tbl, uid
}

// TestExamCapture_AutoSeed_ZeroRowDelete_PersistsPlan proves the exam capture
// path (postgres_questions.go's CreateQuestion, insert branch) rescues a
// 0-row DELETE exactly like the practice path: expected_result_status
// captures successfully and auto_seed persists the derived plan.
func TestExamCapture_AutoSeed_ZeroRowDelete_PersistsPlan(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, tbl, uid := mkExamAndDeleteTable(t, repo, ctx, "examautoseed_create")

	eid := models.ExamID(examID)
	newID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID:      &eid,
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		Points:      5,
		Requirement: "delete a specific employee",
		CreatedBy:   models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}

	saved, err := repo.GetQuestion(ctx, newID)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" {
		t.Fatalf("expected captured, got %q (capture_error=%q)", saved.ExpectedResultStatus, saved.CaptureError)
	}
	if len(saved.AutoSeed) == 0 {
		t.Fatalf("expected a non-nil auto_seed for the exam capture path")
	}
	var plan AutoSeedPlan
	if err := json.Unmarshal(saved.AutoSeed, &plan); err != nil {
		t.Fatalf("unmarshal auto_seed: %v", err)
	}
	if plan.Table != tbl || len(plan.Columns) != 1 || plan.Columns[0].Name != "ssn" || plan.Columns[0].Value != "999999999" {
		t.Fatalf("unexpected plan: %+v", plan)
	}

	// The real capture table (in public) must still hold only its original row.
	if got := countSchemaTableRows(t, repo, ctx, "public", tbl); got != 1 {
		t.Fatalf("expected the real capture table to keep its original 1 row, got %d", got)
	}
}

// TestExamCapture_AutoSeed_UpsertBranch_ReDerivesOnUnchangedZeroRowAnswer
// covers the ON CONFLICT (id) DO UPDATE upsert branch: re-saving the SAME
// still-vacuous model answer (only an unrelated field changed) must still
// persist a non-nil auto_seed, not silently drop it.
func TestExamCapture_AutoSeed_UpsertBranch_ReDerivesOnUnchangedZeroRowAnswer(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, tbl, uid := mkExamAndDeleteTable(t, repo, ctx, "examautoseed_upsert")

	eid := models.ExamID(examID)
	base := models.Question{
		ExamID:      &eid,
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		Points:      5,
		Requirement: "original",
		CreatedBy:   models.UserID(uid),
	}
	id, err := repo.CreateQuestion(ctx, base)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	first, err := repo.GetQuestion(ctx, id)
	if err != nil {
		t.Fatalf("GetQuestion (first): %v", err)
	}
	if len(first.AutoSeed) == 0 {
		t.Fatalf("expected auto_seed after the first save")
	}

	upd := base
	upd.ID = id
	upd.Requirement = "edited" // unrelated field change; correct_sql unchanged, still vacuous
	gotID, err := repo.CreateQuestion(ctx, upd)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if gotID != id {
		t.Fatalf("expected the upsert to keep id %d, got %d", id, gotID)
	}

	second, err := repo.GetQuestion(ctx, id)
	if err != nil {
		t.Fatalf("GetQuestion (second): %v", err)
	}
	if second.ExpectedResultStatus != "captured" {
		t.Fatalf("expected re-save to still capture successfully, got %q (%s)", second.ExpectedResultStatus, second.CaptureError)
	}
	if len(second.AutoSeed) == 0 {
		t.Fatalf("expected the upsert (ON CONFLICT) branch to persist a re-derived auto_seed, got NULL")
	}
	if second.Requirement != "edited" {
		t.Fatalf("expected the unrelated field edit to have applied, got requirement=%q", second.Requirement)
	}
}

// TestExamCapture_AutoSeed_ClearedWhenModelAnswerBecomesEffective proves the
// upsert branch clears a stale plan once the lecturer fixes the WHERE clause
// so the model answer affects rows on its own — auto_seed must not survive as
// a leftover from the old vacuous version.
func TestExamCapture_AutoSeed_ClearedWhenModelAnswerBecomesEffective(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, tbl, uid := mkExamAndDeleteTable(t, repo, ctx, "examautoseed_clear")

	eid := models.ExamID(examID)
	base := models.Question{
		ExamID:      &eid,
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl), // vacuous: no such row
		Type:        "DML",
		Points:      5,
		Requirement: "vacuous initially",
		CreatedBy:   models.UserID(uid),
	}
	id, err := repo.CreateQuestion(ctx, base)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	first, err := repo.GetQuestion(ctx, id)
	if err != nil {
		t.Fatalf("GetQuestion (first): %v", err)
	}
	if len(first.AutoSeed) == 0 {
		t.Fatalf("expected auto_seed after the first (vacuous) save")
	}

	upd := base
	upd.ID = id
	upd.CorrectSQL = fmt.Sprintf("DELETE FROM %s WHERE ssn = '111111111'", tbl) // matches the real pre-existing row
	gotID, err := repo.CreateQuestion(ctx, upd)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if gotID != id {
		t.Fatalf("expected the upsert to keep id %d, got %d", id, gotID)
	}

	second, err := repo.GetQuestion(ctx, id)
	if err != nil {
		t.Fatalf("GetQuestion (second): %v", err)
	}
	if second.ExpectedResultStatus != "captured" {
		t.Fatalf("expected the now-effective model answer to capture successfully, got %q (%s)", second.ExpectedResultStatus, second.CaptureError)
	}
	if len(second.AutoSeed) != 0 {
		t.Fatalf("expected auto_seed to clear to NULL once the model answer affects rows on its own, got %s", second.AutoSeed)
	}

	// Capture always runs inside a rolled-back transaction, so the DELETE run
	// during THIS capture must not have actually removed the real row.
	if got := countSchemaTableRows(t, repo, ctx, "public", tbl); got != 1 {
		t.Fatalf("expected the real capture table to still hold its original row after capture, got %d rows", got)
	}
}

// TestExamCapture_AutoSeed_NoIDCollision_TwoRescuedQuestionsGetDistinctIDs
// combines area #5 with the pre-existing id=0 collision regression
// (postgres_questions_id_test.go): two no-client-id creates that BOTH take
// the auto-seed rescue branch (and therefore bind one extra auto_seed
// parameter each) must still get two distinct, positive ids — proving the
// new bind parameter didn't reintroduce the id=0 collision via a placeholder
// off-by-one.
func TestExamCapture_AutoSeed_NoIDCollision_TwoRescuedQuestionsGetDistinctIDs(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, tbl, uid := mkExamAndDeleteTable(t, repo, ctx, "examautoseed_idcollision")

	mk := func(req string) models.Question {
		eid := models.ExamID(examID)
		return models.Question{
			ExamID:      &eid,
			TargetTable: tbl,
			CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
			Type:        "DML",
			Points:      5,
			Requirement: req,
			CreatedBy:   models.UserID(uid),
		}
	}
	id1, err := repo.CreateQuestion(ctx, mk("Q ONE"))
	if err != nil {
		t.Fatalf("CreateQuestion 1: %v", err)
	}
	id2, err := repo.CreateQuestion(ctx, mk("Q TWO"))
	if err != nil {
		t.Fatalf("CreateQuestion 2: %v", err)
	}
	if id1 <= 0 || id2 <= 0 || id1 == id2 {
		t.Fatalf("expected two distinct positive ids, got id1=%d id2=%d", id1, id2)
	}
	q1, err := repo.GetQuestion(ctx, id1)
	if err != nil {
		t.Fatalf("GetQuestion(id1): %v", err)
	}
	q2, err := repo.GetQuestion(ctx, id2)
	if err != nil {
		t.Fatalf("GetQuestion(id2): %v", err)
	}
	if len(q1.AutoSeed) == 0 || len(q2.AutoSeed) == 0 {
		t.Fatalf("expected both rescued questions to persist a non-nil auto_seed")
	}

	var cnt int
	if err := repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM app.questions WHERE exam_id=$1`, examID).Scan(&cnt); err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 2 {
		t.Fatalf("expected 2 persisted questions, got %d (one clobbered the other)", cnt)
	}
}

// TestAutoSeed_RefusalMessages_Parity_PracticeVsExam covers area #6: the four
// ARCH-039 refusal/rescue strings must be byte-identical between the
// practice (postgres_practice.go) and exam (postgres_questions.go) capture
// paths, for every one of the four message shapes the design doc calls out.
func TestAutoSeed_RefusalMessages_Parity_PracticeVsExam(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	// (1) Non-'=' operator, naming the operator and column.
	t.Run("non_equality_operator", func(t *testing.T) {
		catID, uid := mkPracticeCategory(t, repo, ctx, "parity_op_practice")
		defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()
		const ptbl = "employee_parity_op_p"
		if err := repo.ImportSchemaIntoCategory(ctx, catID, fmt.Sprintf(
			"CREATE TABLE %s (ssn TEXT PRIMARY KEY, salary INTEGER); INSERT INTO %s (ssn, salary) VALUES ('1', 100);", ptbl, ptbl), uid, false); err != nil {
			t.Fatalf("ImportSchemaIntoCategory: %v", err)
		}
		pq := &models.PracticeQuestion{
			CreatedBy: models.UserID(uid), CategoryID: models.CategoryID(catID),
			TargetTable: ptbl, CorrectSQL: fmt.Sprintf("DELETE FROM %s WHERE salary > 50000", ptbl),
			Type: "DML", Requirement: "r", Points: 5,
		}
		if err := repo.CreatePracticeQuestion(ctx, pq); err != nil {
			t.Fatalf("CreatePracticeQuestion: %v", err)
		}
		psaved, err := repo.GetPracticeQuestion(ctx, int(pq.ID))
		if err != nil {
			t.Fatalf("GetPracticeQuestion: %v", err)
		}

		examID, etbl, euid := mkExamAndDeleteTable(t, repo, ctx, "parity_op_exam")
		eid := models.ExamID(examID)
		newID, err := repo.CreateQuestion(ctx, models.Question{
			ExamID: &eid, TargetTable: etbl,
			CorrectSQL: fmt.Sprintf("DELETE FROM %s WHERE salary > 50000", etbl),
			Type:       "DML", Points: 5, Requirement: "r", CreatedBy: models.UserID(euid),
		})
		if err != nil {
			t.Fatalf("CreateQuestion: %v", err)
		}
		esaved, err := repo.GetQuestion(ctx, newID)
		if err != nil {
			t.Fatalf("GetQuestion: %v", err)
		}

		if psaved.ExpectedResultStatus != "failed" || esaved.ExpectedResultStatus != "failed" {
			t.Fatalf("expected both captures to fail, got practice=%q exam=%q", psaved.ExpectedResultStatus, esaved.ExpectedResultStatus)
		}
		if psaved.CaptureError != esaved.CaptureError {
			t.Fatalf("ARCH-039 message drift between practice and exam:\npractice=%q\nexam=%q", psaved.CaptureError, esaved.CaptureError)
		}
	})

	// (2) Still 0 rows after seeding (an unpinned column happens to make the
	// rescue itself pointless — use a WHERE clause that matches nothing even
	// after seeding by pinning a value that can never equal the freshly
	// inserted row's OWN value... instead, simplest reproduction: pin every
	// NOT NULL column so the seed inserts successfully, then the re-run of
	// the SAME correct_sql (same predicate) must always affect exactly 1 row
	// — so to hit "still 0 rows after seeding", drive it with a table that
	// has a BEFORE trigger silently discarding the DELETE. This keeps the
	// fixture in one place and identical between practice/exam.)
	t.Run("still_zero_after_seed", func(t *testing.T) {
		catID, uid := mkPracticeCategory(t, repo, ctx, "parity_stillzero_practice")
		defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()
		const ptbl = "employee_parity_stillzero_p"
		// Unqualified names: ImportSchemaIntoCategory already runs with
		// search_path pinned to the category's own schema and rejects
		// schema-qualified identifiers in the import script (ErrSchemaQualifiedName).
		importSQL := fmt.Sprintf(`
			CREATE TABLE %s (ssn TEXT PRIMARY KEY);
			CREATE OR REPLACE FUNCTION swallow_delete_p() RETURNS trigger AS $$
			BEGIN RETURN NULL; END;
			$$ LANGUAGE plpgsql;
			CREATE TRIGGER swallow_delete_p_trg BEFORE DELETE ON %s
			FOR EACH ROW EXECUTE FUNCTION swallow_delete_p();`, ptbl, ptbl)
		if err := repo.ImportSchemaIntoCategory(ctx, catID, importSQL, uid, false); err != nil {
			t.Fatalf("ImportSchemaIntoCategory: %v", err)
		}
		pq := &models.PracticeQuestion{
			CreatedBy: models.UserID(uid), CategoryID: models.CategoryID(catID),
			TargetTable: ptbl, CorrectSQL: fmt.Sprintf("DELETE FROM %s WHERE ssn = '999'", ptbl),
			Type: "DML", Requirement: "r", Points: 5,
		}
		if err := repo.CreatePracticeQuestion(ctx, pq); err != nil {
			t.Fatalf("CreatePracticeQuestion: %v", err)
		}
		psaved, err := repo.GetPracticeQuestion(ctx, int(pq.ID))
		if err != nil {
			t.Fatalf("GetPracticeQuestion: %v", err)
		}

		examID, euid := mkExamOnlyWithAuthor(t, repo, ctx, "parity_stillzero_exam")
		etbl := fmt.Sprintf("autoseed_stillzero_%d", examID)
		if _, err := repo.pool.Exec(ctx, fmt.Sprintf(`
			CREATE TABLE public.%s (ssn TEXT PRIMARY KEY);
			CREATE OR REPLACE FUNCTION public.swallow_delete_e_%d() RETURNS trigger AS $$
			BEGIN RETURN NULL; END;
			$$ LANGUAGE plpgsql;
			CREATE TRIGGER swallow_delete_e_%d_trg BEFORE DELETE ON public.%s
			FOR EACH ROW EXECUTE FUNCTION public.swallow_delete_e_%d();`, etbl, examID, examID, etbl, examID)); err != nil {
			t.Fatalf("create exam capture table+trigger: %v", err)
		}
		t.Cleanup(func() {
			repo.pool.Exec(context.Background(), "DROP TABLE IF EXISTS public."+etbl)
		})
		eid := models.ExamID(examID)
		newID, err := repo.CreateQuestion(ctx, models.Question{
			ExamID: &eid, TargetTable: etbl,
			CorrectSQL: fmt.Sprintf("DELETE FROM %s WHERE ssn = '999'", etbl),
			Type:       "DML", Points: 5, Requirement: "r", CreatedBy: models.UserID(euid),
		})
		if err != nil {
			t.Fatalf("CreateQuestion: %v", err)
		}
		esaved, err := repo.GetQuestion(ctx, newID)
		if err != nil {
			t.Fatalf("GetQuestion: %v", err)
		}

		wantMsg := "Model answer affected 0 rows, and still affected 0 rows after auto-seeding a matching row. Check the WHERE clause of the correct SQL."
		if psaved.CaptureError != wantMsg {
			t.Fatalf("practice capture_error = %q, want %q", psaved.CaptureError, wantMsg)
		}
		if esaved.CaptureError != wantMsg {
			t.Fatalf("exam capture_error = %q, want %q", esaved.CaptureError, wantMsg)
		}
		if psaved.CaptureError != esaved.CaptureError {
			t.Fatalf("ARCH-039 message drift between practice and exam:\npractice=%q\nexam=%q", psaved.CaptureError, esaved.CaptureError)
		}
	})

	// (3) 0-row INSERT keeps the ORIGINAL generic message (no auto-seed
	// wording at all — INSERT questions are never derived).
	t.Run("zero_row_insert_keeps_generic_message", func(t *testing.T) {
		catID, uid := mkPracticeCategory(t, repo, ctx, "parity_insert_practice")
		defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()
		const ptbl = "employee_parity_insert_p"
		if err := repo.ImportSchemaIntoCategory(ctx, catID, fmt.Sprintf(
			"CREATE TABLE %s (ssn TEXT PRIMARY KEY);", ptbl), uid, false); err != nil {
			t.Fatalf("ImportSchemaIntoCategory: %v", err)
		}
		pq := &models.PracticeQuestion{
			CreatedBy: models.UserID(uid), CategoryID: models.CategoryID(catID),
			TargetTable: ptbl, CorrectSQL: fmt.Sprintf("INSERT INTO %s SELECT ssn FROM %s WHERE ssn = 'nope'", ptbl, ptbl),
			Type: "DML", Requirement: "r", Points: 5,
		}
		if err := repo.CreatePracticeQuestion(ctx, pq); err != nil {
			t.Fatalf("CreatePracticeQuestion: %v", err)
		}
		psaved, err := repo.GetPracticeQuestion(ctx, int(pq.ID))
		if err != nil {
			t.Fatalf("GetPracticeQuestion: %v", err)
		}

		examID, etbl, euid := mkExamAndDeleteTable(t, repo, ctx, "parity_insert_exam")
		eid := models.ExamID(examID)
		newID, err := repo.CreateQuestion(ctx, models.Question{
			ExamID: &eid, TargetTable: etbl,
			CorrectSQL: fmt.Sprintf("INSERT INTO %s SELECT ssn, salary FROM %s WHERE ssn = 'nope'", etbl, etbl),
			Type:       "DML", Points: 5, Requirement: "r", CreatedBy: models.UserID(euid),
		})
		if err != nil {
			t.Fatalf("CreateQuestion: %v", err)
		}
		esaved, err := repo.GetQuestion(ctx, newID)
		if err != nil {
			t.Fatalf("GetQuestion: %v", err)
		}

		wantMsg := "The model answer affected 0 rows, so this question cannot tell a correct answer apart from a statement that does nothing. Fix the WHERE clause of the correct SQL, or fix the seed data in this category's database."
		if psaved.CaptureError != wantMsg {
			t.Fatalf("practice capture_error = %q, want %q", psaved.CaptureError, wantMsg)
		}
		if esaved.CaptureError != wantMsg {
			t.Fatalf("exam capture_error = %q, want %q", esaved.CaptureError, wantMsg)
		}
		if len(psaved.AutoSeed) != 0 || len(esaved.AutoSeed) != 0 {
			t.Fatalf("expected auto_seed to stay NULL for a 0-row INSERT on both paths, got practice=%s exam=%s", psaved.AutoSeed, esaved.AutoSeed)
		}
	})
}

// mkExamOnlyWithAuthor is the same shape as mkExamAndDeleteTable's exam+user
// setup, WITHOUT creating a capture table (the still_zero_after_seed
// subtest above needs a table + trigger combo that mkExamAndDeleteTable
// doesn't build).
func mkExamOnlyWithAuthor(t *testing.T, repo *PostgresRepository, ctx context.Context, prefix string) (examID, uid int) {
	t.Helper()
	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername(prefix + "_author"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	examID, err = repo.CreateExamination(ctx, models.Examination{Title: uniqueUsername(prefix + "_exam"), CreatedBy: models.UserID(uid)})
	if err != nil {
		t.Fatalf("CreateExamination: %v", err)
	}
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })
	return examID, uid
}
