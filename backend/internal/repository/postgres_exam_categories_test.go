package repository

// DB-gated integration tests for TODO-062 (ARCH-035 (2)/(3) names this file
// explicitly), covering the whole exam-category chain: ARCH-026 (migration),
// ARCH-027 (schema provisioning + grants), ARCH-028 (per-category grading
// resolution + legacy sync), ARCH-029 (CRUD/import), ARCH-030 (ordering, not
// re-tested here — see controllers/exam_category_http_test.go and
// exam_category_resolution_test.go), and ARCH-036 (the delete-refuses-when-
// non-empty override).
//
// Uses openTestRepo(t)/TEST_DATABASE_URL (shared with
// postgres_password_events_test.go) and SKIPs cleanly when it is unset, so
// `go test ./...` stays green on a machine with no database. Reuses the
// package's existing fixture helpers: mkExamWithAuthor
// (postgres_dual_schema_integration_test.go), schemaExists
// (postgres_preview_schema_delete_test.go), hasSchemaUsage/hasTablePriv
// (postgres_preview_role_test.go), unmarshalRows/rowVal/normalizeRows
// (postgres_dual_schema_integration_test.go). Every test creates its own
// scratch examination(s) and cleans them up via t.Cleanup(DeleteExamination),
// which itself exercises the ARCH-027 (5)(a) cleanup path.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/... -run 'ExamCategor|MigrateExamCategories|ByteIdentical|DualSchemaPerCategory|LegacySync|DeleteExamination_DropsCategorySchemas|ArchiveExamination_DropsOnlyCategorySandboxes' -v

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// ─────────────────────────────────────────────────────────────────────────────
// Small local helpers, distinct names from the package's existing ones.
// ─────────────────────────────────────────────────────────────────────────────

// hasSchemaPriv reports has_schema_privilege(role, schema, priv) for an
// arbitrary privilege (hasSchemaUsage, reused from postgres_preview_role_test.go,
// is USAGE-specific).
func hasSchemaPriv(t *testing.T, repo *PostgresRepository, ctx context.Context, role, schema, priv string) bool {
	t.Helper()
	var ok bool
	if err := repo.pool.QueryRow(ctx,
		"SELECT has_schema_privilege($1, $2, $3)", role, schema, priv).Scan(&ok); err != nil {
		t.Fatalf("has_schema_privilege(%s, %s, %s): %v", role, schema, priv, err)
	}
	return ok
}

// examCatNamespaceSnapshot returns every schema name in pg_namespace, sorted,
// for the ARCH-026 A3 "zero schema side effects" before/after diff.
func examCatNamespaceSnapshot(t *testing.T, repo *PostgresRepository, ctx context.Context) []string {
	t.Helper()
	rows, err := repo.pool.Query(ctx, "SELECT nspname FROM pg_namespace ORDER BY nspname")
	if err != nil {
		t.Fatalf("pg_namespace snapshot: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan nspname: %v", err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("pg_namespace snapshot iterate: %v", err)
	}
	return names
}

// readIntColumn reads one integer column of one row (matched on id) via the
// privileged pool directly — ground truth, independent of any student-role
// sandboxing.
func readIntColumn(t *testing.T, repo *PostgresRepository, ctx context.Context, schema, table, col string, id int) int {
	t.Helper()
	safeTable := pgx.Identifier{schema, table}.Sanitize()
	safeCol := pgx.Identifier{col}.Sanitize()
	var v int
	if err := repo.pool.QueryRow(ctx,
		fmt.Sprintf("SELECT %s FROM %s WHERE id = $1", safeCol, safeTable), id).Scan(&v); err != nil {
		t.Fatalf("readIntColumn(%s.%s.%s, id=%d): %v", schema, table, col, id, err)
	}
	return v
}

// categoryPairs snapshots (question_id -> category_id, 0 for NULL) so two
// snapshots can be compared for the ARCH-026 A4 idempotence assertion.
func categoryPairs(qs []models.Question) map[int]int {
	m := make(map[int]int, len(qs))
	for _, q := range qs {
		if q.CategoryID != nil {
			m[q.ID] = int(*q.CategoryID)
		} else {
			m[q.ID] = 0
		}
	}
	return m
}

// ─────────────────────────────────────────────────────────────────────────────
// ARCH-026 A2/A3: migration correctness + zero schema side effects.
// ─────────────────────────────────────────────────────────────────────────────

// TestMigrateExamCategories_PerExamCorrectness_NoSchemaSideEffects seeds three
// exams — E1 on the default curriculum_schema='public', E2 with a controlled
// per-exam curriculum_schema/preview_curriculum_schema pair, E3 with zero
// questions — and proves migrateExamCategories gives each exactly one
// "General" category whose schema names are copied VERBATIM from the exam
// row, attaches E1/E2's questions to their own exam's category only (no
// cross-exam category_id pointers), and creates/copies/renames/drops ZERO
// PostgreSQL schemas (ARCH-026 A2/A3).
func TestMigrateExamCategories_PerExamCorrectness_NoSchemaSideEffects(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	e1ID, e1Uid := mkExamWithAuthor(t, repo, ctx, "mig_e1")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), e1ID) })
	e1eid := models.ExamID(e1ID)
	if _, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &e1eid, CorrectSQL: "SELECT 1", Type: "QUERY", Points: 1,
		Requirement: "e1 question", CreatedBy: models.UserID(e1Uid),
	}); err != nil {
		t.Fatalf("CreateQuestion (E1): %v", err)
	}

	e2ID, e2Uid := mkExamWithAuthor(t, repo, ctx, "mig_e2")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), e2ID) })
	e2Schema := fmt.Sprintf("curriculum_exam_%d", e2ID)
	e2Preview := e2Schema + "_preview"
	if _, err := repo.pool.Exec(ctx,
		"UPDATE app.examinations SET curriculum_schema = $1, preview_curriculum_schema = $2 WHERE id = $3",
		e2Schema, e2Preview, e2ID); err != nil {
		t.Fatalf("seed E2 curriculum_schema: %v", err)
	}
	e2eid := models.ExamID(e2ID)
	if _, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &e2eid, CorrectSQL: "SELECT 1", Type: "QUERY", Points: 1,
		Requirement: "e2 question", CreatedBy: models.UserID(e2Uid),
	}); err != nil {
		t.Fatalf("CreateQuestion (E2): %v", err)
	}

	e3ID, _ := mkExamWithAuthor(t, repo, ctx, "mig_e3")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), e3ID) })
	// E3 deliberately has zero questions.

	before := examCatNamespaceSnapshot(t, repo, ctx)
	var auditBefore int
	if err := repo.pool.QueryRow(ctx, "SELECT COUNT(*) FROM app.schema_import_audit").Scan(&auditBefore); err != nil {
		t.Fatalf("count schema_import_audit before: %v", err)
	}

	if err := repo.migrateExamCategories(ctx); err != nil {
		t.Fatalf("migrateExamCategories: %v", err)
	}

	after := examCatNamespaceSnapshot(t, repo, ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("ARCH-026 A3 VIOLATED: pg_namespace changed by migrateExamCategories.\nbefore=%v\nafter=%v", before, after)
	}
	var auditAfter int
	if err := repo.pool.QueryRow(ctx, "SELECT COUNT(*) FROM app.schema_import_audit").Scan(&auditAfter); err != nil {
		t.Fatalf("count schema_import_audit after: %v", err)
	}
	if auditAfter != auditBefore {
		t.Fatalf("ARCH-026 A3 VIOLATED: app.schema_import_audit row count changed (before=%d after=%d)", auditBefore, auditAfter)
	}

	e1Cats, err := repo.ListExamCategories(ctx, e1ID)
	if err != nil {
		t.Fatalf("ListExamCategories(E1): %v", err)
	}
	if len(e1Cats) != 1 || e1Cats[0].Name != "General" {
		t.Fatalf("expected E1 to have exactly one 'General' category, got %+v", e1Cats)
	}
	if e1Cats[0].SchemaName != "public" || e1Cats[0].PreviewSchemaName != "" {
		t.Fatalf("expected E1's General to copy schema_name='public' preview_schema_name='', got schema_name=%q preview_schema_name=%q",
			e1Cats[0].SchemaName, e1Cats[0].PreviewSchemaName)
	}
	if e1Cats[0].OwnsSchema {
		t.Fatalf("expected E1's General to be legacy-adopted (OwnsSchema=false), got true")
	}

	e2Cats, err := repo.ListExamCategories(ctx, e2ID)
	if err != nil {
		t.Fatalf("ListExamCategories(E2): %v", err)
	}
	if len(e2Cats) != 1 || e2Cats[0].Name != "General" {
		t.Fatalf("expected E2 to have exactly one 'General' category, got %+v", e2Cats)
	}
	if e2Cats[0].SchemaName != e2Schema || e2Cats[0].PreviewSchemaName != e2Preview {
		t.Fatalf("expected E2's General to copy schema_name=%q preview_schema_name=%q, got schema_name=%q preview_schema_name=%q",
			e2Schema, e2Preview, e2Cats[0].SchemaName, e2Cats[0].PreviewSchemaName)
	}

	e3Cats, err := repo.ListExamCategories(ctx, e3ID)
	if err != nil {
		t.Fatalf("ListExamCategories(E3): %v", err)
	}
	if len(e3Cats) != 1 || e3Cats[0].Name != "General" {
		t.Fatalf("expected E3 (zero questions) to still get exactly one 'General' category, got %+v", e3Cats)
	}

	e1Questions, err := repo.GetQuestionsByExamID(ctx, e1ID)
	if err != nil {
		t.Fatalf("GetQuestionsByExamID(E1): %v", err)
	}
	if len(e1Questions) != 1 || e1Questions[0].CategoryID == nil || *e1Questions[0].CategoryID != e1Cats[0].ID {
		t.Fatalf("expected E1's question attached to E1's own General (id=%v), got %+v", e1Cats[0].ID, e1Questions)
	}
	e2Questions, err := repo.GetQuestionsByExamID(ctx, e2ID)
	if err != nil {
		t.Fatalf("GetQuestionsByExamID(E2): %v", err)
	}
	if len(e2Questions) != 1 || e2Questions[0].CategoryID == nil || *e2Questions[0].CategoryID != e2Cats[0].ID {
		t.Fatalf("expected E2's question attached to E2's own General (id=%v), got %+v", e2Cats[0].ID, e2Questions)
	}
	if e1Cats[0].ID == e2Cats[0].ID {
		t.Fatalf("test invariant violated: E1 and E2 General categories share an id")
	}
	if *e1Questions[0].CategoryID == e2Cats[0].ID {
		t.Fatalf("CROSS-EXAM CONTAMINATION: E1's question points at E2's category")
	}
}

// TestMigrateExamCategories_IdempotentAcrossReruns proves ARCH-026 A4: running
// the migration three times leaves both the category count and the
// (question_id, category_id) pairs unchanged after runs 2 and 3.
func TestMigrateExamCategories_IdempotentAcrossReruns(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, uid := mkExamWithAuthor(t, repo, ctx, "mig_idem")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

	eid := models.ExamID(examID)
	if _, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &eid, CorrectSQL: "SELECT 1", Type: "QUERY", Points: 1,
		Requirement: "r", CreatedBy: models.UserID(uid),
	}); err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}

	if err := repo.migrateExamCategories(ctx); err != nil {
		t.Fatalf("migrateExamCategories (1st run): %v", err)
	}
	cats1, err := repo.ListExamCategories(ctx, examID)
	if err != nil || len(cats1) != 1 {
		t.Fatalf("expected exactly 1 category after 1st run, got %+v (err=%v)", cats1, err)
	}
	qs1, err := repo.GetQuestionsByExamID(ctx, examID)
	if err != nil {
		t.Fatalf("GetQuestionsByExamID (1st run): %v", err)
	}
	pairs1 := categoryPairs(qs1)

	if err := repo.migrateExamCategories(ctx); err != nil {
		t.Fatalf("migrateExamCategories (2nd run): %v", err)
	}
	if err := repo.migrateExamCategories(ctx); err != nil {
		t.Fatalf("migrateExamCategories (3rd run): %v", err)
	}

	cats2, err := repo.ListExamCategories(ctx, examID)
	if err != nil || len(cats2) != 1 || cats2[0].ID != cats1[0].ID {
		t.Fatalf("expected the same single category after reruns 2/3, got %+v (1st was %+v, err=%v)", cats2, cats1, err)
	}
	qs2, err := repo.GetQuestionsByExamID(ctx, examID)
	if err != nil {
		t.Fatalf("GetQuestionsByExamID (after reruns): %v", err)
	}
	pairs2 := categoryPairs(qs2)
	if !reflect.DeepEqual(pairs1, pairs2) {
		t.Fatalf("ARCH-026 A4 VIOLATED: (question_id, category_id) pairs changed across idempotent reruns: before=%v after=%v", pairs1, pairs2)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ARCH-026 A5: the three backfill-stop/resume cases.
// ─────────────────────────────────────────────────────────────────────────────

func TestMigrateExamCategories_BackfillStopAndResumeGuards(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	t.Run("schema_flipped_stops_backfill", func(t *testing.T) {
		examID, uid := mkExamWithAuthor(t, repo, ctx, "mig_stop_a")
		t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })
		eid := models.ExamID(examID)
		if _, err := repo.CreateQuestion(ctx, models.Question{
			ExamID: &eid, CorrectSQL: "SELECT 1", Type: "QUERY", Points: 1,
			Requirement: "r1", CreatedBy: models.UserID(uid),
		}); err != nil {
			t.Fatalf("CreateQuestion: %v", err)
		}
		if err := repo.migrateExamCategories(ctx); err != nil {
			t.Fatalf("migrateExamCategories: %v", err)
		}
		cats, err := repo.ListExamCategories(ctx, examID)
		if err != nil || len(cats) != 1 {
			t.Fatalf("expected exactly 1 General category, got %+v (err=%v)", cats, err)
		}
		generalID := int(cats[0].ID)

		// Flip General to own its schema via a real import (ARCH-026 (4b) guard a).
		if err := repo.ImportSchemaIntoExamCategory(ctx, examID, generalID, "CREATE TABLE t (id INTEGER PRIMARY KEY);", uid, false); err != nil {
			t.Fatalf("ImportSchemaIntoExamCategory: %v", err)
		}

		newID, err := repo.CreateQuestion(ctx, models.Question{
			ExamID: &eid, CorrectSQL: "SELECT 1", Type: "QUERY", Points: 1,
			Requirement: "r2 (post-flip, no category)", CreatedBy: models.UserID(uid),
		})
		if err != nil {
			t.Fatalf("CreateQuestion (post-flip): %v", err)
		}

		if err := repo.migrateExamCategories(ctx); err != nil {
			t.Fatalf("migrateExamCategories (post-flip rerun): %v", err)
		}
		got, err := repo.GetQuestion(ctx, newID)
		if err != nil {
			t.Fatalf("GetQuestion: %v", err)
		}
		if got.CategoryID != nil {
			t.Fatalf("ARCH-026 A5(a) VIOLATED: expected the new question's category_id to stay NULL once General owns its schema, got %v", *got.CategoryID)
		}
	})

	t.Run("second_category_stops_backfill", func(t *testing.T) {
		examID, uid := mkExamWithAuthor(t, repo, ctx, "mig_stop_b")
		t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })
		eid := models.ExamID(examID)
		if _, err := repo.CreateQuestion(ctx, models.Question{
			ExamID: &eid, CorrectSQL: "SELECT 1", Type: "QUERY", Points: 1,
			Requirement: "r1", CreatedBy: models.UserID(uid),
		}); err != nil {
			t.Fatalf("CreateQuestion: %v", err)
		}
		if err := repo.migrateExamCategories(ctx); err != nil {
			t.Fatalf("migrateExamCategories: %v", err)
		}

		if _, err := repo.CreateExamCategory(ctx, examID, "Part B", &uid); err != nil {
			t.Fatalf("CreateExamCategory: %v", err)
		}

		newID, err := repo.CreateQuestion(ctx, models.Question{
			ExamID: &eid, CorrectSQL: "SELECT 1", Type: "QUERY", Points: 1,
			Requirement: "r2 (post-2nd-category, no category)", CreatedBy: models.UserID(uid),
		})
		if err != nil {
			t.Fatalf("CreateQuestion (post-2nd-category): %v", err)
		}

		if err := repo.migrateExamCategories(ctx); err != nil {
			t.Fatalf("migrateExamCategories (post-2nd-category rerun): %v", err)
		}
		got, err := repo.GetQuestion(ctx, newID)
		if err != nil {
			t.Fatalf("GetQuestion: %v", err)
		}
		if got.CategoryID != nil {
			t.Fatalf("ARCH-026 A5(b) VIOLATED: expected the new question's category_id to stay NULL once the exam has 2 categories, got %v", *got.CategoryID)
		}
	})

	t.Run("single_legacy_general_resumes_backfill", func(t *testing.T) {
		examID, uid := mkExamWithAuthor(t, repo, ctx, "mig_stop_c")
		t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

		// Run the migration FIRST, with zero questions yet, so General exists
		// but nothing has attached to it.
		if err := repo.migrateExamCategories(ctx); err != nil {
			t.Fatalf("migrateExamCategories (pre-question): %v", err)
		}
		cats, err := repo.ListExamCategories(ctx, examID)
		if err != nil || len(cats) != 1 {
			t.Fatalf("expected exactly 1 (General) category, got %+v (err=%v)", cats, err)
		}

		eid := models.ExamID(examID)
		newID, err := repo.CreateQuestion(ctx, models.Question{
			ExamID: &eid, CorrectSQL: "SELECT 1", Type: "QUERY", Points: 1,
			Requirement: "late-added, no category", CreatedBy: models.UserID(uid),
		})
		if err != nil {
			t.Fatalf("CreateQuestion (late-added): %v", err)
		}

		if err := repo.migrateExamCategories(ctx); err != nil {
			t.Fatalf("migrateExamCategories (positive re-attach rerun): %v", err)
		}
		got, err := repo.GetQuestion(ctx, newID)
		if err != nil {
			t.Fatalf("GetQuestion: %v", err)
		}
		if got.CategoryID == nil || int(*got.CategoryID) != int(cats[0].ID) {
			t.Fatalf("ARCH-026 A5(c) VIOLATED: expected the late-added question re-attached to General (id=%v), got %v", cats[0].ID, got.CategoryID)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// ARCH-027 B1/B3/B4: grant parity for the query/sandbox/decoy schemas.
// ─────────────────────────────────────────────────────────────────────────────

// TestExamCategoryGrants_QueryAndSandboxParityWithCurriculum is the highest-
// priority assertion in this batch: an exam_cat_<id> reference schema must
// grant exam_student_readonly EXACTLY what a curriculum_exam_<n> schema
// grants it (probed with has_schema_privilege/has_table_privilege, not by
// reading DDL), and exam_student must get nothing on the reference schema —
// only on its own lazily-provisioned sandbox.
func TestExamCategoryGrants_QueryAndSandboxParityWithCurriculum(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	curriculumExamID, curriculumUID := mkExamWithAuthor(t, repo, ctx, "grantparity_curriculum")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), curriculumExamID) })
	if err := repo.ImportSchemaIntoExamCurriculum(ctx, curriculumExamID, "CREATE TABLE t (id INTEGER PRIMARY KEY);", curriculumUID, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCurriculum: %v", err)
	}
	curriculumSchema := ExamCurriculumSchemaName(curriculumExamID)

	examID, uid := mkExamWithAuthor(t, repo, ctx, "grantparity_cat")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })
	cat, err := repo.CreateExamCategory(ctx, examID, "GrantParity", &uid)
	if err != nil {
		t.Fatalf("CreateExamCategory: %v", err)
	}
	if err := repo.ImportSchemaIntoExamCategory(ctx, examID, int(cat.ID), "CREATE TABLE t (id INTEGER PRIMARY KEY);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCategory: %v", err)
	}
	catSchema := ExamCategorySchemaName(int(cat.ID))

	probes := []struct {
		name string
		fn   func(schema string) bool
	}{
		{"exam_student_readonly USAGE", func(s string) bool { return hasSchemaUsage(t, repo, ctx, "exam_student_readonly", s) }},
		{"exam_student_readonly SELECT on t", func(s string) bool { return hasTablePriv(t, repo, ctx, "exam_student_readonly", s, "t", "SELECT") }},
		{"exam_student CREATE (parity: both false)", func(s string) bool { return hasSchemaPriv(t, repo, ctx, "exam_student", s, "CREATE") }},
		{"exam_student INSERT on t (parity: both false)", func(s string) bool { return hasTablePriv(t, repo, ctx, "exam_student", s, "t", "INSERT") }},
	}
	for _, p := range probes {
		t.Run(p.name, func(t *testing.T) {
			catResult := p.fn(catSchema)
			curriculumResult := p.fn(curriculumSchema)
			if catResult != curriculumResult {
				t.Fatalf("ARCH-027 B1 VIOLATED: exam_cat_<id> probe = %v, curriculum_exam_<n> probe = %v — grant parity broken for %q", catResult, curriculumResult, p.name)
			}
		})
	}
	if !hasSchemaUsage(t, repo, ctx, "exam_student_readonly", catSchema) {
		t.Fatal("ARCH-027 B1 VIOLATED: exam_student_readonly has NO USAGE on the category schema — every student SELECT against a categorised question would fail with 'permission denied'")
	}
	if hasSchemaPriv(t, repo, ctx, "exam_student", catSchema, "CREATE") {
		t.Fatal("expected exam_student to have NO privileges on the category's reference schema (DML runs in its own sandbox)")
	}

	// B3: the writable DML sandbox, lazily provisioned by EnsureExamCategorySchemasReady.
	eid := models.ExamID(examID)
	if err := repo.ImportSchemaIntoExamCategory(ctx, examID, int(cat.ID), "CREATE TABLE t (id INTEGER PRIMARY KEY); INSERT INTO t VALUES (1);", uid, true); err != nil {
		t.Fatalf("ImportSchemaIntoExamCategory (seed row via replace): %v", err)
	}
	if _, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &eid, CategoryID: &cat.ID, TargetTable: "t",
		CorrectSQL: "UPDATE t SET id = id WHERE id = 1", Type: "DML", Points: 1,
		Requirement: "sandbox provisioning trigger", CreatedBy: models.UserID(uid),
	}); err != nil {
		t.Fatalf("CreateQuestion (DML): %v", err)
	}
	if err := repo.EnsureExamCategorySchemasReady(ctx, examID); err != nil {
		t.Fatalf("EnsureExamCategorySchemasReady: %v", err)
	}
	sandboxSchema := ExamCategorySandboxName(int(cat.ID))
	hasTables, err := repo.SchemaHasTables(ctx, sandboxSchema)
	if err != nil || !hasTables {
		t.Fatalf("expected sandbox schema %q to be provisioned with tables, hasTables=%v err=%v", sandboxSchema, hasTables, err)
	}
	for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
		if !hasTablePriv(t, repo, ctx, "exam_student", sandboxSchema, "t", priv) {
			t.Errorf("ARCH-027 B3 VIOLATED: expected exam_student to have %s on sandbox %s.t", priv, sandboxSchema)
		}
	}
}

// TestExamCategoryGrants_DecoyPosture is ARCH-027 B4: the decoy reference
// schema grants only exam_student_preview_readonly (never exam_student /
// exam_student_readonly — the ARCH-020 asymmetry), and the decoy's own DML
// sandbox additionally grants exam_student_preview full DML.
func TestExamCategoryGrants_DecoyPosture(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, uid := mkExamWithAuthor(t, repo, ctx, "decoy_cat")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

	cat, err := repo.CreateExamCategory(ctx, examID, "Decoy", &uid)
	if err != nil {
		t.Fatalf("CreateExamCategory: %v", err)
	}
	if _, err := repo.ImportSchemaIntoExamCategoryPreview(ctx, examID, int(cat.ID), "CREATE TABLE t (id INTEGER PRIMARY KEY);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCategoryPreview: %v", err)
	}
	decoySchema := ExamCategoryPreviewSchemaName(int(cat.ID))

	if !hasSchemaUsage(t, repo, ctx, "exam_student_preview_readonly", decoySchema) {
		t.Error("ARCH-027 B4 VIOLATED: expected exam_student_preview_readonly to have USAGE on the decoy schema")
	}
	if !hasTablePriv(t, repo, ctx, "exam_student_preview_readonly", decoySchema, "t", "SELECT") {
		t.Error("ARCH-027 B4 VIOLATED: expected exam_student_preview_readonly to have SELECT on the decoy schema's table")
	}
	if hasSchemaUsage(t, repo, ctx, "exam_student_preview", decoySchema) {
		t.Error("ARCH-027 B4 VIOLATED: expected exam_student_preview to have NO privileges on a QUERY-only decoy (allowDML=false)")
	}
	for _, role := range []string{"exam_student", "exam_student_readonly"} {
		if hasSchemaUsage(t, repo, ctx, role, decoySchema) {
			t.Errorf("ARCH-027 B4 VIOLATED (ARCH-020 asymmetry): expected %s to have NO privileges on the decoy schema", role)
		}
	}

	if err := repo.EnsureExamCategoryPreviewSandboxReady(ctx, int(cat.ID)); err != nil {
		t.Fatalf("EnsureExamCategoryPreviewSandboxReady: %v", err)
	}
	decoySandbox := ExamCategoryPreviewSandboxName(int(cat.ID))
	for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
		if !hasTablePriv(t, repo, ctx, "exam_student_preview", decoySandbox, "t", priv) {
			t.Errorf("ARCH-027 B4 VIOLATED: expected exam_student_preview to have %s on the decoy sandbox %s.t", priv, decoySandbox)
		}
	}
}

// TestExamCategoryPreviewGrants_ReconcileSelfHeals is ARCH-027 B7: a decoy
// schema matching the exam_cat_<n>_preview name pattern, left with the WRONG
// (legacy) grants, is self-healed by a single CreateRestrictedUser call (the
// boot path) — no migration, no manual SQL.
func TestExamCategoryPreviewGrants_ReconcileSelfHeals(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	legacyDecoy := fmt.Sprintf("exam_cat_%d_preview", time.Now().UnixNano())
	if err := repo.EnsureSchema(ctx, legacyDecoy); err != nil {
		t.Fatalf("EnsureSchema(%q): %v", legacyDecoy, err)
	}
	t.Cleanup(func() {
		safe := pgx.Identifier{legacyDecoy}.Sanitize()
		_, _ = repo.pool.Exec(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safe))
	})
	safeDecoy := pgx.Identifier{legacyDecoy}.Sanitize()
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf("CREATE TABLE %s.t (id INTEGER PRIMARY KEY)", safeDecoy)); err != nil {
		t.Fatalf("create legacy decoy table: %v", err)
	}
	// Simulate the WRONG (pre-ARCH-020-style) grant: exam_student directly.
	if err := repo.GrantSchemaPermissions(ctx, legacyDecoy); err != nil {
		t.Fatalf("simulate legacy exam_student grant: %v", err)
	}
	if !hasSchemaUsage(t, repo, ctx, "exam_student", legacyDecoy) {
		t.Fatal("test setup error: expected exam_student to (wrongly) have USAGE before reconcile")
	}

	if err := repo.CreateRestrictedUser(ctx); err != nil {
		t.Fatalf("CreateRestrictedUser: %v", err)
	}

	if hasSchemaUsage(t, repo, ctx, "exam_student", legacyDecoy) {
		t.Fatal("SECURITY REGRESSION: exam_student still has USAGE on the exam_cat_*_preview schema after reconcile")
	}
	if !hasSchemaUsage(t, repo, ctx, "exam_student_preview_readonly", legacyDecoy) {
		t.Fatal("expected exam_student_preview_readonly to gain USAGE after reconcile")
	}
	if !hasTablePriv(t, repo, ctx, "exam_student_preview_readonly", legacyDecoy, "t", "SELECT") {
		t.Fatal("expected exam_student_preview_readonly to gain SELECT after reconcile")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ARCH-027 B8: DeleteExamination / ArchiveExamination cleanup scope.
// ─────────────────────────────────────────────────────────────────────────────

// TestDeleteExamination_DropsCategorySchemas_LeavesLegacyPublicIntact proves
// the ARCH-027 (5)(a)/(c) discipline: cleanup drops schemas by CANONICAL name
// derived from the category id, never by the row's stored schema_name — so a
// legacy-adopted General category (schema_name='public') never takes 'public'
// down with it, while an owning category's four canonical schemas are all
// reclaimed.
func TestDeleteExamination_DropsCategorySchemas_LeavesLegacyPublicIntact(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, uid := mkExamWithAuthor(t, repo, ctx, "del_examcat")

	if err := repo.migrateExamCategories(ctx); err != nil {
		t.Fatalf("migrateExamCategories: %v", err)
	}
	legacyCats, err := repo.ListExamCategories(ctx, examID)
	if err != nil || len(legacyCats) != 1 || legacyCats[0].SchemaName != "public" {
		t.Fatalf("test setup: expected exactly 1 legacy General category with schema_name='public', got %+v (err=%v)", legacyCats, err)
	}

	owningCat, err := repo.CreateExamCategory(ctx, examID, "Owning", &uid)
	if err != nil {
		t.Fatalf("CreateExamCategory: %v", err)
	}
	if err := repo.ImportSchemaIntoExamCategory(ctx, examID, int(owningCat.ID), "CREATE TABLE t (id INTEGER PRIMARY KEY);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCategory: %v", err)
	}
	if _, err := repo.ImportSchemaIntoExamCategoryPreview(ctx, examID, int(owningCat.ID), "CREATE TABLE t (id INTEGER PRIMARY KEY);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCategoryPreview: %v", err)
	}
	eid := models.ExamID(examID)
	if _, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &eid, CategoryID: &owningCat.ID, TargetTable: "t",
		CorrectSQL: "UPDATE t SET id = id", Type: "DML", Points: 1,
		Requirement: "trigger sandbox provisioning", CreatedBy: models.UserID(uid),
	}); err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}
	if err := repo.EnsureExamCategorySchemasReady(ctx, examID); err != nil {
		t.Fatalf("EnsureExamCategorySchemasReady: %v", err)
	}
	if err := repo.EnsureExamCategoryPreviewSandboxReady(ctx, int(owningCat.ID)); err != nil {
		t.Fatalf("EnsureExamCategoryPreviewSandboxReady: %v", err)
	}

	realSchema := ExamCategorySchemaName(int(owningCat.ID))
	previewSchema := ExamCategoryPreviewSchemaName(int(owningCat.ID))
	sandboxSchema := ExamCategorySandboxName(int(owningCat.ID))
	previewSandboxSchema := ExamCategoryPreviewSandboxName(int(owningCat.ID))
	for _, s := range []string{realSchema, previewSchema, sandboxSchema, previewSandboxSchema} {
		if !schemaExists(t, repo, ctx, s) {
			t.Fatalf("test setup: expected schema %q to exist before delete", s)
		}
	}

	// Prove 'public' — the legacy General's stored schema_name — is never
	// touched: seed a uniquely named table directly in public and confirm it
	// survives the delete.
	publicMarker := fmt.Sprintf("del_examcat_marker_%d", examID)
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf(
		"CREATE TABLE public.%s (id INTEGER PRIMARY KEY); INSERT INTO public.%s VALUES (1)", publicMarker, publicMarker)); err != nil {
		t.Fatalf("seed public marker table: %v", err)
	}
	t.Cleanup(func() { _, _ = repo.pool.Exec(context.Background(), "DROP TABLE IF EXISTS public."+publicMarker) })

	if err := repo.DeleteExamination(ctx, examID); err != nil {
		t.Fatalf("DeleteExamination: %v", err)
	}

	for _, s := range []string{realSchema, previewSchema, sandboxSchema, previewSandboxSchema} {
		if schemaExists(t, repo, ctx, s) {
			t.Errorf("ARCH-027 B8 VIOLATED: expected schema %q to be dropped by DeleteExamination, but it still exists", s)
		}
	}
	if !schemaExists(t, repo, ctx, "public") {
		t.Fatal("DATA-LOSS REGRESSION: schema 'public' was dropped by DeleteExamination")
	}
	var markerCount int
	if err := repo.pool.QueryRow(ctx, fmt.Sprintf("SELECT COUNT(*) FROM public.%s", publicMarker)).Scan(&markerCount); err != nil {
		t.Fatalf("DATA-LOSS REGRESSION: public.%s no longer readable after DeleteExamination (a legacy category's schema_name='public' must never be dropped): %v", publicMarker, err)
	}
	if markerCount != 1 {
		t.Fatalf("expected the public marker table's row to survive, got count=%d", markerCount)
	}
}

// TestArchiveExamination_DropsOnlyCategorySandboxes is the second half of
// ARCH-027 B8: archive drops only the two regenerable sandbox clones, leaving
// both reference schemas (real + decoy) intact.
func TestArchiveExamination_DropsOnlyCategorySandboxes(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, uid := mkExamWithAuthor(t, repo, ctx, "archive_examcat")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

	cat, err := repo.CreateExamCategory(ctx, examID, "Archived", &uid)
	if err != nil {
		t.Fatalf("CreateExamCategory: %v", err)
	}
	if err := repo.ImportSchemaIntoExamCategory(ctx, examID, int(cat.ID), "CREATE TABLE t (id INTEGER PRIMARY KEY);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCategory: %v", err)
	}
	if _, err := repo.ImportSchemaIntoExamCategoryPreview(ctx, examID, int(cat.ID), "CREATE TABLE t (id INTEGER PRIMARY KEY);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCategoryPreview: %v", err)
	}
	eid := models.ExamID(examID)
	if _, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &eid, CategoryID: &cat.ID, TargetTable: "t",
		CorrectSQL: "UPDATE t SET id = id", Type: "DML", Points: 1,
		Requirement: "trigger sandbox provisioning", CreatedBy: models.UserID(uid),
	}); err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}
	if err := repo.EnsureExamCategorySchemasReady(ctx, examID); err != nil {
		t.Fatalf("EnsureExamCategorySchemasReady: %v", err)
	}
	if err := repo.EnsureExamCategoryPreviewSandboxReady(ctx, int(cat.ID)); err != nil {
		t.Fatalf("EnsureExamCategoryPreviewSandboxReady: %v", err)
	}

	realSchema := ExamCategorySchemaName(int(cat.ID))
	previewSchema := ExamCategoryPreviewSchemaName(int(cat.ID))
	sandboxSchema := ExamCategorySandboxName(int(cat.ID))
	previewSandboxSchema := ExamCategoryPreviewSandboxName(int(cat.ID))

	if err := repo.ArchiveExamination(ctx, examID); err != nil {
		t.Fatalf("ArchiveExamination: %v", err)
	}

	if schemaExists(t, repo, ctx, sandboxSchema) {
		t.Errorf("ARCH-027 B8 VIOLATED: expected sandbox %q dropped by archive", sandboxSchema)
	}
	if schemaExists(t, repo, ctx, previewSandboxSchema) {
		t.Errorf("ARCH-027 B8 VIOLATED: expected preview sandbox %q dropped by archive", previewSandboxSchema)
	}
	if !schemaExists(t, repo, ctx, realSchema) {
		t.Errorf("ARCH-027 B8 VIOLATED: expected reference schema %q to survive archive", realSchema)
	}
	if !schemaExists(t, repo, ctx, previewSchema) {
		t.Errorf("ARCH-027 B8 VIOLATED: expected decoy reference schema %q to survive archive", previewSchema)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Per-category isolation (priority #4 in the brief): two categories with
// same-named tables holding different data grade independently.
// ─────────────────────────────────────────────────────────────────────────────

func TestExamCategoryIsolation_QueryAndDML(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, uid := mkExamWithAuthor(t, repo, ctx, "isolation")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

	catA, err := repo.CreateExamCategory(ctx, examID, "Cat A", &uid)
	if err != nil {
		t.Fatalf("CreateExamCategory(A): %v", err)
	}
	catB, err := repo.CreateExamCategory(ctx, examID, "Cat B", &uid)
	if err != nil {
		t.Fatalf("CreateExamCategory(B): %v", err)
	}
	if err := repo.ImportSchemaIntoExamCategory(ctx, examID, int(catA.ID),
		"CREATE TABLE t (id INTEGER PRIMARY KEY, val INTEGER NOT NULL); INSERT INTO t VALUES (1, 100);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCategory(A): %v", err)
	}
	if err := repo.ImportSchemaIntoExamCategory(ctx, examID, int(catB.ID),
		"CREATE TABLE t (id INTEGER PRIMARY KEY, val INTEGER NOT NULL); INSERT INTO t VALUES (1, 999);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCategory(B): %v", err)
	}

	eid := models.ExamID(examID)
	qAID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &eid, CategoryID: &catA.ID, CorrectSQL: "SELECT * FROM t", Type: "QUERY",
		Points: 1, Requirement: "r", CreatedBy: models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateQuestion(A QUERY): %v", err)
	}
	qBID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &eid, CategoryID: &catB.ID, CorrectSQL: "SELECT * FROM t", Type: "QUERY",
		Points: 1, Requirement: "r", CreatedBy: models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateQuestion(B QUERY): %v", err)
	}
	qA, err := repo.GetQuestion(ctx, qAID)
	if err != nil {
		t.Fatalf("GetQuestion(A): %v", err)
	}
	qB, err := repo.GetQuestion(ctx, qBID)
	if err != nil {
		t.Fatalf("GetQuestion(B): %v", err)
	}

	resA, err := repo.Evaluate(ctx, uid, qA, "SELECT * FROM t", "")
	if err != nil {
		t.Fatalf("Evaluate(A QUERY): %v", err)
	}
	resB, err := repo.Evaluate(ctx, uid, qB, "SELECT * FROM t", "")
	if err != nil {
		t.Fatalf("Evaluate(B QUERY): %v", err)
	}
	if resA.Status != "success" || fmt.Sprintf("%v", rowVal(t, unmarshalRows(t, resA.ValidationDetails), "val")) != "100" {
		t.Fatalf("expected category A's QUERY to read val=100, got status=%q details=%s", resA.Status, resA.ValidationDetails)
	}
	if resB.Status != "success" || fmt.Sprintf("%v", rowVal(t, unmarshalRows(t, resB.ValidationDetails), "val")) != "999" {
		t.Fatalf("expected category B's QUERY to read val=999, got status=%q details=%s", resB.Status, resB.ValidationDetails)
	}

	// DML isolation.
	dmlAID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &eid, CategoryID: &catA.ID, TargetTable: "t",
		CorrectSQL: "UPDATE t SET val = val + 1 WHERE id = 1", Type: "DML",
		Points: 1, Requirement: "r", CreatedBy: models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateQuestion(A DML): %v", err)
	}
	if _, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &eid, CategoryID: &catB.ID, TargetTable: "t",
		CorrectSQL: "UPDATE t SET val = val + 1 WHERE id = 1", Type: "DML",
		Points: 1, Requirement: "r", CreatedBy: models.UserID(uid),
	}); err != nil {
		t.Fatalf("CreateQuestion(B DML): %v", err)
	}
	if err := repo.EnsureExamCategorySchemasReady(ctx, examID); err != nil {
		t.Fatalf("EnsureExamCategorySchemasReady: %v", err)
	}

	sandboxA := ExamCategorySandboxName(int(catA.ID))
	sandboxB := ExamCategorySandboxName(int(catB.ID))
	beforeBVal := readIntColumn(t, repo, ctx, sandboxB, "t", "val", 1)

	dmlA, err := repo.GetQuestion(ctx, dmlAID)
	if err != nil {
		t.Fatalf("GetQuestion(dmlA): %v", err)
	}
	resDMLA, err := repo.Evaluate(ctx, uid, dmlA, "UPDATE t SET val = val + 1 WHERE id = 1", ExamGradingSandboxFor(dmlA))
	if err != nil {
		t.Fatalf("Evaluate(A DML): %v", err)
	}
	if resDMLA.Status != "success" || fmt.Sprintf("%v", rowVal(t, unmarshalRows(t, resDMLA.ValidationDetails), "val")) != "101" {
		t.Fatalf("expected category A's DML post-image val=101, got status=%q details=%s", resDMLA.Status, resDMLA.ValidationDetails)
	}

	afterBVal := readIntColumn(t, repo, ctx, sandboxB, "t", "val", 1)
	if afterBVal != beforeBVal {
		t.Fatalf("LEAK: category B's sandbox row changed (%d -> %d) after grading category A's DML question", beforeBVal, afterBVal)
	}

	// Savepoint rollback still holds: sandbox A's REAL stored value is untouched.
	realAVal := readIntColumn(t, repo, ctx, sandboxA, "t", "val", 1)
	if realAVal != 100 {
		t.Fatalf("expected sandbox A's real (ungraded) value to remain 100 after the rolled-back eval, got %d", realAVal)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ARCH-028 C2: byte-identical grading before/after migration.
// ─────────────────────────────────────────────────────────────────────────────

func TestByteIdenticalGrading_BeforeAfterMigration(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	t.Run("with_per_exam_curriculum_QUERY_and_DML", func(t *testing.T) {
		examID, uid := mkExamWithAuthor(t, repo, ctx, "byteidentical")
		t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })
		if err := repo.ImportSchemaIntoExamCurriculum(ctx, examID,
			"CREATE TABLE t (id INTEGER PRIMARY KEY, val INTEGER NOT NULL); INSERT INTO t VALUES (1, 10);", uid, false); err != nil {
			t.Fatalf("ImportSchemaIntoExamCurriculum: %v", err)
		}

		eid := models.ExamID(examID)
		queryID, err := repo.CreateQuestion(ctx, models.Question{
			ExamID: &eid, CorrectSQL: "SELECT * FROM t", Type: "QUERY", Points: 1,
			Requirement: "r", CreatedBy: models.UserID(uid),
		})
		if err != nil {
			t.Fatalf("CreateQuestion(QUERY): %v", err)
		}
		dmlID, err := repo.CreateQuestion(ctx, models.Question{
			ExamID: &eid, TargetTable: "t", CorrectSQL: "UPDATE t SET val = val + 1 WHERE id = 1", Type: "DML",
			Points: 1, Requirement: "r", CreatedBy: models.UserID(uid),
		})
		if err != nil {
			t.Fatalf("CreateQuestion(DML): %v", err)
		}
		if err := repo.InitializeExamSandbox(ctx, examID); err != nil {
			t.Fatalf("InitializeExamSandbox: %v", err)
		}

		queryQBefore, err := repo.GetQuestion(ctx, queryID)
		if err != nil {
			t.Fatalf("GetQuestion(query, before): %v", err)
		}
		dmlQBefore, err := repo.GetQuestion(ctx, dmlID)
		if err != nil {
			t.Fatalf("GetQuestion(dml, before): %v", err)
		}
		if queryQBefore.CategoryID != nil || dmlQBefore.CategoryID != nil {
			t.Fatalf("test setup: expected no category before migration")
		}

		queryResBefore, err := repo.Evaluate(ctx, uid, queryQBefore, "SELECT * FROM t", ExamSandboxName(examID))
		if err != nil {
			t.Fatalf("Evaluate(query, before): %v", err)
		}
		dmlResBefore, err := repo.Evaluate(ctx, uid, dmlQBefore, "UPDATE t SET val = val + 1 WHERE id = 1", ExamGradingSandboxFor(dmlQBefore))
		if err != nil {
			t.Fatalf("Evaluate(dml, before): %v", err)
		}

		if err := repo.migrateExamCategories(ctx); err != nil {
			t.Fatalf("migrateExamCategories: %v", err)
		}

		queryQAfter, err := repo.GetQuestion(ctx, queryID)
		if err != nil {
			t.Fatalf("GetQuestion(query, after): %v", err)
		}
		dmlQAfter, err := repo.GetQuestion(ctx, dmlID)
		if err != nil {
			t.Fatalf("GetQuestion(dml, after): %v", err)
		}
		if queryQAfter.CategoryID == nil || dmlQAfter.CategoryID == nil {
			t.Fatalf("test setup: expected the migration to have attached both questions to General")
		}
		if ExamCategoryOwnsSchema(int(*queryQAfter.CategoryID), queryQAfter.CategorySchema) {
			t.Fatalf("test setup: expected General to be legacy-adopted (not owning)")
		}

		queryResAfter, err := repo.Evaluate(ctx, uid, queryQAfter, "SELECT * FROM t", ExamSandboxName(examID))
		if err != nil {
			t.Fatalf("Evaluate(query, after): %v", err)
		}
		dmlResAfter, err := repo.Evaluate(ctx, uid, dmlQAfter, "UPDATE t SET val = val + 1 WHERE id = 1", ExamGradingSandboxFor(dmlQAfter))
		if err != nil {
			t.Fatalf("Evaluate(dml, after): %v", err)
		}

		if queryResBefore.Status != queryResAfter.Status || string(queryResBefore.ValidationDetails) != string(queryResAfter.ValidationDetails) || queryResBefore.Feedback != queryResAfter.Feedback {
			t.Fatalf("ARCH-028 C2 VIOLATED (QUERY): grading diverged after migration:\nbefore=%+v\nafter=%+v", queryResBefore, queryResAfter)
		}
		if dmlResBefore.Status != dmlResAfter.Status || string(dmlResBefore.ValidationDetails) != string(dmlResAfter.ValidationDetails) || dmlResBefore.Feedback != dmlResAfter.Feedback {
			t.Fatalf("ARCH-028 C2 VIOLATED (DML): grading diverged after migration:\nbefore=%+v\nafter=%+v", dmlResBefore, dmlResAfter)
		}
	})

	t.Run("with_public_fallback_QUERY_only", func(t *testing.T) {
		examID, uid := mkExamWithAuthor(t, repo, ctx, "byteidentical_public")
		t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })
		tbl := fmt.Sprintf("byteident_pub_%d", examID)
		if _, err := repo.pool.Exec(ctx, fmt.Sprintf(
			"CREATE TABLE public.%s (id INTEGER PRIMARY KEY, val INTEGER NOT NULL); INSERT INTO public.%s VALUES (1, 55)", tbl, tbl)); err != nil {
			t.Fatalf("seed public table: %v", err)
		}
		t.Cleanup(func() { _, _ = repo.pool.Exec(context.Background(), "DROP TABLE IF EXISTS public."+tbl) })

		eid := models.ExamID(examID)
		sql := fmt.Sprintf("SELECT * FROM %s", tbl)
		queryID, err := repo.CreateQuestion(ctx, models.Question{
			ExamID: &eid, CorrectSQL: sql, Type: "QUERY", Points: 1, Requirement: "r", CreatedBy: models.UserID(uid),
		})
		if err != nil {
			t.Fatalf("CreateQuestion: %v", err)
		}
		qBefore, err := repo.GetQuestion(ctx, queryID)
		if err != nil {
			t.Fatalf("GetQuestion(before): %v", err)
		}
		if qBefore.CurriculumSchema != "public" {
			t.Fatalf("test setup: expected curriculum_schema='public', got %q", qBefore.CurriculumSchema)
		}
		resBefore, err := repo.Evaluate(ctx, uid, qBefore, sql, "")
		if err != nil {
			t.Fatalf("Evaluate(before): %v", err)
		}

		if err := repo.migrateExamCategories(ctx); err != nil {
			t.Fatalf("migrateExamCategories: %v", err)
		}
		qAfter, err := repo.GetQuestion(ctx, queryID)
		if err != nil {
			t.Fatalf("GetQuestion(after): %v", err)
		}
		if qAfter.CategoryID == nil {
			t.Fatalf("test setup: expected migration to attach the question to General")
		}
		resAfter, err := repo.Evaluate(ctx, uid, qAfter, sql, "")
		if err != nil {
			t.Fatalf("Evaluate(after): %v", err)
		}

		if resBefore.Status != resAfter.Status || string(resBefore.ValidationDetails) != string(resAfter.ValidationDetails) {
			t.Fatalf("ARCH-028 C2 VIOLATED (public fallback QUERY): grading diverged after migration:\nbefore=%+v\nafter=%+v", resBefore, resAfter)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// ARCH-028 C5: the legacy-sync stamp on both curriculum imports.
// ─────────────────────────────────────────────────────────────────────────────

func TestLegacySync_OnCurriculumAndPreviewImport(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, uid := mkExamWithAuthor(t, repo, ctx, "legacysync")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

	if err := repo.migrateExamCategories(ctx); err != nil {
		t.Fatalf("migrateExamCategories: %v", err)
	}
	cats, err := repo.ListExamCategories(ctx, examID)
	if err != nil || len(cats) != 1 {
		t.Fatalf("expected exactly 1 category, got %+v (err=%v)", cats, err)
	}
	if cats[0].SchemaName != "public" {
		t.Fatalf("test setup: expected General.schema_name='public', got %q", cats[0].SchemaName)
	}
	generalID := cats[0].ID

	// An owning category, created AFTER migration, must be untouched by the syncs below.
	owningCat, err := repo.CreateExamCategory(ctx, examID, "Owning", &uid)
	if err != nil {
		t.Fatalf("CreateExamCategory: %v", err)
	}

	eid := models.ExamID(examID)
	qID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &eid, CategoryID: &generalID, CorrectSQL: "SELECT 1", Type: "QUERY",
		Points: 1, Requirement: "r", CreatedBy: models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}

	if err := repo.ImportSchemaIntoExamCurriculum(ctx, examID, "CREATE TABLE t (id INTEGER PRIMARY KEY);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCurriculum: %v", err)
	}
	wantCurriculumSchema := ExamCurriculumSchemaName(examID)

	generalAfterCurriculum, err := repo.GetExamCategory(ctx, examID, int(generalID))
	if err != nil {
		t.Fatalf("GetExamCategory(after curriculum import): %v", err)
	}
	if generalAfterCurriculum.SchemaName != wantCurriculumSchema {
		t.Fatalf("ARCH-028 C5 VIOLATED: expected General.schema_name synced to %q after curriculum import, got %q", wantCurriculumSchema, generalAfterCurriculum.SchemaName)
	}
	qAfterCurriculum, err := repo.GetQuestion(ctx, qID)
	if err != nil {
		t.Fatalf("GetQuestion(after curriculum import): %v", err)
	}
	if qAfterCurriculum.CategorySchema != wantCurriculumSchema {
		t.Fatalf("ARCH-028 C5 VIOLATED: expected the attached question's resolved CategorySchema to follow the sync, got %q want %q", qAfterCurriculum.CategorySchema, wantCurriculumSchema)
	}

	if _, err := repo.ImportSchemaIntoExamCurriculumPreview(ctx, examID, "CREATE TABLE t (id INTEGER PRIMARY KEY);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCurriculumPreview: %v", err)
	}
	wantPreviewSchema := ExamPreviewCurriculumSchemaName(examID)

	generalAfterPreview, err := repo.GetExamCategory(ctx, examID, int(generalID))
	if err != nil {
		t.Fatalf("GetExamCategory(after preview import): %v", err)
	}
	if generalAfterPreview.PreviewSchemaName != wantPreviewSchema {
		t.Fatalf("ARCH-028 C5 VIOLATED: expected General.preview_schema_name synced to %q, got %q", wantPreviewSchema, generalAfterPreview.PreviewSchemaName)
	}
	qAfterPreview, err := repo.GetQuestion(ctx, qID)
	if err != nil {
		t.Fatalf("GetQuestion(after preview import): %v", err)
	}
	if qAfterPreview.CategoryPreviewSchema != wantPreviewSchema {
		t.Fatalf("ARCH-028 C5 VIOLATED: expected the attached question's CategoryPreviewSchema to follow the preview sync, got %q want %q", qAfterPreview.CategoryPreviewSchema, wantPreviewSchema)
	}

	owningAfter, err := repo.GetExamCategory(ctx, examID, int(owningCat.ID))
	if err != nil {
		t.Fatalf("GetExamCategory(owning, after both syncs): %v", err)
	}
	if owningAfter.SchemaName != owningCat.SchemaName || owningAfter.PreviewSchemaName != owningCat.PreviewSchemaName {
		t.Fatalf("ARCH-028 C5 VIOLATED: expected the owning category untouched by the exam-level curriculum/preview sync, got schema_name=%q preview_schema_name=%q",
			owningAfter.SchemaName, owningAfter.PreviewSchemaName)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ARCH-028 C6: dual-preview-per-category grading and fallback.
// ─────────────────────────────────────────────────────────────────────────────

func TestDualSchemaPerCategory_PreviewDecoyGradeReal(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, uid := mkExamWithAuthor(t, repo, ctx, "dualcat")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

	// (a) Dual QUERY: previews on the category's own decoy, grades on its own real schema.
	cat, err := repo.CreateExamCategory(ctx, examID, "DualQuery", &uid)
	if err != nil {
		t.Fatalf("CreateExamCategory: %v", err)
	}
	if err := repo.ImportSchemaIntoExamCategory(ctx, examID, int(cat.ID),
		"CREATE TABLE secret (id INTEGER PRIMARY KEY, val TEXT NOT NULL); INSERT INTO secret VALUES (1, 'B-real');", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCategory: %v", err)
	}
	if _, err := repo.ImportSchemaIntoExamCategoryPreview(ctx, examID, int(cat.ID),
		"CREATE TABLE secret (id INTEGER PRIMARY KEY, val TEXT NOT NULL); INSERT INTO secret VALUES (1, 'A-decoy');", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCategoryPreview: %v", err)
	}

	eid := models.ExamID(examID)
	qID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &eid, CategoryID: &cat.ID, CorrectSQL: "SELECT * FROM secret", Type: "QUERY",
		DualSchema: true, Points: 1, Requirement: "r", CreatedBy: models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}
	q, err := repo.GetQuestion(ctx, qID)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}
	if q.CategorySchema != ExamCategorySchemaName(int(cat.ID)) || q.CategoryPreviewSchema != ExamCategoryPreviewSchemaName(int(cat.ID)) {
		t.Fatalf("test setup: expected category schema/preview schema to resolve to the owning names, got %+v", q)
	}

	gradeResult, err := repo.Evaluate(ctx, uid, q, "SELECT * FROM secret", ExamGradingSandboxFor(q))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if gradeResult.Status != "success" || rowVal(t, unmarshalRows(t, gradeResult.ValidationDetails), "val") != "B-real" {
		t.Fatalf("expected grading to read the real category schema (B-real), got status=%q details=%s", gradeResult.Status, gradeResult.ValidationDetails)
	}
	pr, err := repo.EvaluatePreview(ctx, uid, q, "SELECT * FROM secret", ExamGradingSandboxFor(q), q.CategoryPreviewSchema)
	if err != nil {
		t.Fatalf("EvaluatePreview: %v", err)
	}
	if pr.Result.Status != "success" || len(pr.Rows) != 1 || pr.Rows[0]["val"] != "A-decoy" {
		t.Fatalf("expected preview to read the category's own decoy (A-decoy), got %+v", pr)
	}

	// (b) Dual DML: previews on the category's own decoy SANDBOX, grades on its own real sandbox.
	catDML, err := repo.CreateExamCategory(ctx, examID, "DualDML", &uid)
	if err != nil {
		t.Fatalf("CreateExamCategory(DML): %v", err)
	}
	if err := repo.ImportSchemaIntoExamCategory(ctx, examID, int(catDML.ID),
		"CREATE TABLE accounts (id INTEGER PRIMARY KEY, balance INTEGER NOT NULL); INSERT INTO accounts VALUES (1, 100);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCategory(DML): %v", err)
	}
	if _, err := repo.ImportSchemaIntoExamCategoryPreview(ctx, examID, int(catDML.ID),
		"CREATE TABLE accounts (id INTEGER PRIMARY KEY, balance INTEGER NOT NULL); INSERT INTO accounts VALUES (1, 999);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCategoryPreview(DML): %v", err)
	}
	dmlID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &eid, CategoryID: &catDML.ID, TargetTable: "accounts",
		CorrectSQL: "UPDATE accounts SET balance = balance + 1 WHERE id = 1", Type: "DML",
		DualSchema: true, Points: 1, Requirement: "r", CreatedBy: models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateQuestion(DML): %v", err)
	}
	dmlQ, err := repo.GetQuestion(ctx, dmlID)
	if err != nil {
		t.Fatalf("GetQuestion(DML): %v", err)
	}
	if err := repo.EnsureExamCategorySchemasReady(ctx, examID); err != nil {
		t.Fatalf("EnsureExamCategorySchemasReady: %v", err)
	}
	if err := repo.EnsureExamCategoryPreviewSandboxReady(ctx, int(catDML.ID)); err != nil {
		t.Fatalf("EnsureExamCategoryPreviewSandboxReady: %v", err)
	}

	dmlGrade, err := repo.Evaluate(ctx, uid, dmlQ, "UPDATE accounts SET balance = balance + 1 WHERE id = 1", ExamGradingSandboxFor(dmlQ))
	if err != nil {
		t.Fatalf("Evaluate(DML): %v", err)
	}
	if dmlGrade.Status != "success" || fmt.Sprintf("%v", rowVal(t, unmarshalRows(t, dmlGrade.ValidationDetails), "balance")) != "101" {
		t.Fatalf("expected DML grading post-image balance=101 on the category's real sandbox, got status=%q details=%s", dmlGrade.Status, dmlGrade.ValidationDetails)
	}
	dmlPreview, err := repo.EvaluatePreview(ctx, uid, dmlQ, "UPDATE accounts SET balance = balance + 1 WHERE id = 1", ExamGradingSandboxFor(dmlQ), ExamPreviewSandboxFor(dmlQ))
	if err != nil {
		t.Fatalf("EvaluatePreview(DML): %v", err)
	}
	if dmlPreview.Result.Status != "success" || fmt.Sprintf("%v", rowVal(t, dmlPreview.Rows, "balance")) != "1000" {
		t.Fatalf("expected DML preview post-image balance=1000 (999+1) on the category's decoy sandbox, got %+v", dmlPreview)
	}
}

// TestDualSchemaPerCategory_NoDecoy_FallbackNeverBlocks proves the ARCH-028 C6
// no-decoy fallback for a categorised question: with no decoy ever imported,
// EvaluatePreview(previewSchema="") lands on the real category schema, never
// blocking the student and matching grading exactly.
func TestDualSchemaPerCategory_NoDecoy_FallbackNeverBlocks(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, uid := mkExamWithAuthor(t, repo, ctx, "dualcat_nodecoy")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

	cat, err := repo.CreateExamCategory(ctx, examID, "NoDecoy", &uid)
	if err != nil {
		t.Fatalf("CreateExamCategory: %v", err)
	}
	if err := repo.ImportSchemaIntoExamCategory(ctx, examID, int(cat.ID),
		"CREATE TABLE secret (id INTEGER PRIMARY KEY, val TEXT NOT NULL); INSERT INTO secret VALUES (1, 'B-only');", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCategory: %v", err)
	}
	// Deliberately NO decoy import.

	eid := models.ExamID(examID)
	qID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &eid, CategoryID: &cat.ID, CorrectSQL: "SELECT * FROM secret", Type: "QUERY",
		DualSchema: true, Points: 1, Requirement: "r", CreatedBy: models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}
	q, err := repo.GetQuestion(ctx, qID)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}
	if q.CategoryPreviewSchema != "" {
		t.Fatalf("test setup: expected no decoy, got CategoryPreviewSchema=%q", q.CategoryPreviewSchema)
	}

	gradeResult, err := repo.Evaluate(ctx, uid, q, "SELECT * FROM secret", ExamGradingSandboxFor(q))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if gradeResult.Status != "success" {
		t.Fatalf("grading evaluation failed: %s", gradeResult.Feedback)
	}

	pr, err := repo.EvaluatePreview(ctx, uid, q, "SELECT * FROM secret", ExamGradingSandboxFor(q), "")
	if err != nil {
		t.Fatalf("EvaluatePreview: %v", err)
	}
	if pr.Result.Status != "success" {
		t.Fatalf("expected the no-decoy fallback preview to succeed (student must never be blocked), got %+v", pr.Result)
	}
	if len(pr.Rows) != 1 || pr.Rows[0]["val"] != "B-only" {
		t.Fatalf("expected the no-decoy fallback preview to surface the real category schema's own row, got %+v", pr.Rows)
	}
	if !reflect.DeepEqual(unmarshalRows(t, gradeResult.ValidationDetails), normalizeRows(t, pr.Rows)) {
		t.Fatalf("expected preview and grade to be identical when no decoy exists: grade=%s preview=%+v", gradeResult.ValidationDetails, pr.Rows)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ARCH-029 CRUD/import, including the repo-layer cross-exam ownership check
// (priority #6 in the brief; the HTTP-layer half lives in
// controllers/exam_category_http_test.go).
// ─────────────────────────────────────────────────────────────────────────────

func TestExamCategoryCRUD_CreateRenameIntroductionListAndCrossExamOwnership(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, uid := mkExamWithAuthor(t, repo, ctx, "crud")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })
	otherExamID, _ := mkExamWithAuthor(t, repo, ctx, "crud_other")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), otherExamID) })

	cat, err := repo.CreateExamCategory(ctx, examID, "Part A", &uid)
	if err != nil {
		t.Fatalf("CreateExamCategory: %v", err)
	}
	if cat.SchemaName != ExamCategorySchemaName(int(cat.ID)) {
		t.Fatalf("expected schema_name = ExamCategorySchemaName(id), got %q", cat.SchemaName)
	}
	if !cat.OwnsSchema {
		t.Fatal("expected a freshly created category to own its schema")
	}
	if cat.QuestionCount != 0 {
		t.Fatalf("expected question_count=0 for a fresh category, got %d", cat.QuestionCount)
	}

	if _, err := repo.CreateExamCategory(ctx, examID, "Part A", &uid); !errors.Is(err, ErrExamCategoryNameTaken) {
		t.Fatalf("expected ErrExamCategoryNameTaken for a duplicate name, got %v", err)
	}

	if err := repo.RenameExamCategory(ctx, examID, int(cat.ID), "Part A Renamed"); err != nil {
		t.Fatalf("RenameExamCategory: %v", err)
	}
	renamed, err := repo.GetExamCategory(ctx, examID, int(cat.ID))
	if err != nil {
		t.Fatalf("GetExamCategory(after rename): %v", err)
	}
	if renamed.Name != "Part A Renamed" || renamed.SchemaName != cat.SchemaName {
		t.Fatalf("expected name updated and schema_name unchanged by rename, got %+v", renamed)
	}

	if err := repo.UpdateExamCategoryIntroduction(ctx, examID, int(cat.ID), "some intro text"); err != nil {
		t.Fatalf("UpdateExamCategoryIntroduction: %v", err)
	}
	withIntro, err := repo.GetExamCategory(ctx, examID, int(cat.ID))
	if err != nil || withIntro.Introduction != "some intro text" {
		t.Fatalf("expected introduction persisted, got %+v (err=%v)", withIntro, err)
	}

	list, err := repo.ListExamCategories(ctx, examID)
	if err != nil || len(list) != 1 || list[0].ID != cat.ID {
		t.Fatalf("expected ListExamCategories to return exactly the one category, got %+v (err=%v)", list, err)
	}

	if err := repo.ImportSchemaIntoExamCategory(ctx, examID, int(cat.ID), "CREATE TABLE t1 (id INTEGER PRIMARY KEY);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCategory (first): %v", err)
	}
	if err := repo.ImportSchemaIntoExamCategory(ctx, examID, int(cat.ID), "CREATE TABLE t2 (id INTEGER PRIMARY KEY);", uid, false); !errors.Is(err, ErrExamCategoryNotEmpty) {
		t.Fatalf("expected ErrExamCategoryNotEmpty on a second import without confirm_replace, got %v", err)
	}
	if err := repo.ImportSchemaIntoExamCategory(ctx, examID, int(cat.ID), "CREATE TABLE t2 (id INTEGER PRIMARY KEY);", uid, true); err != nil {
		t.Fatalf("expected confirm_replace import to succeed, got %v", err)
	}

	// Cross-exam ownership (ARCH-029 (1)/(3), repo layer).
	if _, err := repo.GetExamCategory(ctx, otherExamID, int(cat.ID)); !errors.Is(err, ErrExamCategoryNotFound) {
		t.Fatalf("expected ErrExamCategoryNotFound reaching exam A's category through exam B, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ARCH-036: DeleteExamCategory refuses when non-empty, carries the count, and
// has NO force parameter at all.
// ─────────────────────────────────────────────────────────────────────────────

func TestDeleteExamCategory_RefusesWhenNonEmpty_NoForceParam(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, uid := mkExamWithAuthor(t, repo, ctx, "delcat")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

	cat, err := repo.CreateExamCategory(ctx, examID, "ToDelete", &uid)
	if err != nil {
		t.Fatalf("CreateExamCategory: %v", err)
	}
	if err := repo.ImportSchemaIntoExamCategory(ctx, examID, int(cat.ID), "CREATE TABLE t (id INTEGER PRIMARY KEY);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCategory: %v", err)
	}

	eid := models.ExamID(examID)
	qID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &eid, CategoryID: &cat.ID, CorrectSQL: "SELECT * FROM t", Type: "QUERY",
		Points: 1, Requirement: "r", CreatedBy: models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}

	err = repo.DeleteExamCategory(ctx, examID, int(cat.ID))
	if !errors.Is(err, ErrExamCategoryHasQuestions) {
		t.Fatalf("ARCH-036 VIOLATED: expected ErrExamCategoryHasQuestions for a non-empty category, got %v", err)
	}
	if !strings.Contains(err.Error(), "1 question") {
		t.Fatalf("expected the error to carry the attached-question count, got %q", err.Error())
	}

	if _, err := repo.GetExamCategory(ctx, examID, int(cat.ID)); err != nil {
		t.Fatalf("ARCH-036 VIOLATED: expected the category row to still exist after the refused delete, got %v", err)
	}
	q, err := repo.GetQuestion(ctx, qID)
	if err != nil || q.CategoryID == nil || int(*q.CategoryID) != int(cat.ID) {
		t.Fatalf("ARCH-036 VIOLATED: expected the question's category_id unchanged after the refused delete, got %+v (err=%v)", q, err)
	}

	// No force parameter exists on this function at all: exactly 3 parameters
	// (ctx, examID, categoryID) — a bound method value's reflect.Type does not
	// include the receiver.
	fnType := reflect.TypeOf(repo.DeleteExamCategory)
	if fnType.NumIn() != 3 {
		t.Fatalf("ARCH-036 VIOLATED: expected DeleteExamCategory to take exactly 3 parameters (ctx, examID, categoryID) with no force/detach parameter, got %d (%s)", fnType.NumIn(), fnType)
	}

	if err := repo.DeleteQuestion(ctx, qID); err != nil {
		t.Fatalf("DeleteQuestion: %v", err)
	}
	if err := repo.DeleteExamCategory(ctx, examID, int(cat.ID)); err != nil {
		t.Fatalf("expected delete to succeed once the category is empty, got %v", err)
	}
	if _, err := repo.GetExamCategory(ctx, examID, int(cat.ID)); !errors.Is(err, ErrExamCategoryNotFound) {
		t.Fatalf("expected the category to be gone after a successful delete, got %v", err)
	}
	if schemaExists(t, repo, ctx, ExamCategorySchemaName(int(cat.ID))) {
		t.Fatal("expected the category's owned schema to be dropped after a successful (empty) delete")
	}
}

// TestDeleteExamCategory_ConcurrentAttach_NeverSilentlyDetaches is the FIX-B
// regression test: it proves DeleteExamCategory's ownership check, row lock
// and COUNT are race-free, by deterministically ordering a concurrent
// "attach" (a question INSERT that references the category via
// category_id, held open in its own uncommitted transaction) BEFORE the
// delete even starts, rather than relying on goroutine-scheduler timing to
// land in the racy interleaving.
//
// Session A (this goroutine, via a raw connection acquired straight from
// repo.pool) starts a transaction and INSERTs a question with category_id
// pointing at the test category, but does NOT commit. Enforcing that foreign
// key requires Postgres to hold a FOR KEY SHARE lock on the referenced
// app.exam_categories row for as long as A's transaction is open.
//
// Session B (a goroutine calling the real repo.DeleteExamCategory) is then
// started. Its `SELECT ... FOR UPDATE` on that same row MUST block, because
// FOR KEY SHARE conflicts with FOR UPDATE. The test proves the block is real
// — not merely assumed from timing — by polling pg_locks until it observes a
// waiting (granted = false) lock request from B's backend, with a generous
// timeout; it also asserts B has NOT already returned at that point.
//
// Only once B is provably blocked does the test commit A. B's FOR UPDATE can
// then proceed, its COUNT now observes the committed attach, and
// DeleteExamCategory must return ErrExamCategoryHasQuestions and roll back —
// the ONLY acceptable outcome here per ARCH-036 (the alternative bad
// interleaving, delete-wins-and-then-the-late-committing-attach-gets-
// SET-NULL-by-the-FK, is exactly the defect this fix closes). The test then
// asserts directly on the disallowed outcome: the concurrently-attached
// question's category_id must still equal the category's id — it must never
// have been silently nulled out by a delete that "succeeded" without ever
// seeing it.
func TestDeleteExamCategory_ConcurrentAttach_NeverSilentlyDetaches(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, uid := mkExamWithAuthor(t, repo, ctx, "delcat_race")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

	cat, err := repo.CreateExamCategory(ctx, examID, "RaceCat", &uid)
	if err != nil {
		t.Fatalf("CreateExamCategory: %v", err)
	}

	// Session A: acquire a dedicated connection (genuinely separate backend
	// from any connection DeleteExamCategory will use) and hold an
	// uncommitted INSERT open on it.
	attachConn, err := repo.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire (attach session): %v", err)
	}
	defer attachConn.Release()
	attachPID := attachConn.Conn().PgConn().PID()

	attachTx, err := attachConn.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin (attach session): %v", err)
	}
	var newQID int
	if err := attachTx.QueryRow(ctx,
		`INSERT INTO app.questions (exam_id, category_id, target_table, correct_sql, question_type, points, requirement, created_by, capture_error)
		 VALUES ($1, $2, 't', 'SELECT 1', 'QUERY', 1, 'race attach', $3, '') RETURNING id`,
		examID, int(cat.ID), uid).Scan(&newQID); err != nil {
		t.Fatalf("attach INSERT (uncommitted): %v", err)
	}

	// Session B: the real DeleteExamCategory, in a goroutine, racing against
	// session A's still-open attach.
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- repo.DeleteExamCategory(ctx, examID, int(cat.ID))
	}()

	// Prove B is genuinely blocked on a lock (not just "hasn't run yet") by
	// polling pg_locks for a waiter that isn't A's own backend.
	deadline := time.Now().Add(5 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		var count int
		if err := repo.pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM pg_locks WHERE granted = false AND pid <> $1`,
			int32(attachPID)).Scan(&count); err != nil {
			t.Fatalf("poll pg_locks: %v", err)
		}
		if count > 0 {
			blocked = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("expected DeleteExamCategory's FOR UPDATE to visibly block on the concurrent uncommitted attach (no waiting lock ever observed in pg_locks) — the row lock did not serialise the two sessions")
	}
	select {
	case deleteErrEarly := <-deleteDone:
		t.Fatalf("DeleteExamCategory returned (err=%v) before the concurrent attach committed — it did not actually wait on the row lock", deleteErrEarly)
	default:
	}

	// Release the attach: DeleteExamCategory's FOR UPDATE can now proceed and
	// must observe the committed question.
	if err := attachTx.Commit(ctx); err != nil {
		t.Fatalf("attach commit: %v", err)
	}

	var deleteErr error
	select {
	case deleteErr = <-deleteDone:
	case <-time.After(5 * time.Second):
		t.Fatal("DeleteExamCategory did not return after the concurrent attach committed")
	}

	if !errors.Is(deleteErr, ErrExamCategoryHasQuestions) {
		t.Fatalf("RACE VIOLATED: expected DeleteExamCategory to refuse once its COUNT (inside the same tx as the row lock) observes the concurrently-committed attach, got %v", deleteErr)
	}
	if !strings.Contains(deleteErr.Error(), "1 question") {
		t.Fatalf("expected the refusal to carry the attached-question count, got %q", deleteErr.Error())
	}

	// The one outcome ARCH-036/FIX-B forbids: delete "succeeds" and the
	// concurrently-attached question's category_id gets silently nulled.
	if _, err := repo.GetExamCategory(ctx, examID, int(cat.ID)); err != nil {
		t.Fatalf("expected the category to still exist after the refused delete, got %v", err)
	}
	q, err := repo.GetQuestion(ctx, newQID)
	if err != nil {
		t.Fatalf("GetQuestion(newQID): %v", err)
	}
	if q.CategoryID == nil || int(*q.CategoryID) != int(cat.ID) {
		t.Fatalf("TOCTOU REGRESSION: the concurrently-attached question's category_id is %v after the raced delete — a question was silently detached by a delete that never observed it", q.CategoryID)
	}

	if err := repo.DeleteQuestion(ctx, newQID); err != nil {
		t.Fatalf("cleanup DeleteQuestion: %v", err)
	}
}
