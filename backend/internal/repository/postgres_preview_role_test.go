package repository

// Integration tests for ARCH-020 (GAP 3): the dual-schema preview must be
// engine-enforced, not merely cosmetic. Before this fix, a dual preview ran
// under exam_student / exam_student_readonly, both of which hold USAGE+SELECT
// (exam_student also DML) on EVERY real curriculum/category schema, so a
// student could run a schema-qualified query against grading schema B from
// inside a decoy preview and read it in full. These tests prove:
//   - the two new preview-only roles exist, are NOLOGIN, and are idempotent
//     to (re)create;
//   - neither new role ever holds USAGE on a real schema (the hard invariant);
//   - a decoy schema grants the new roles and has exam_student /
//     exam_student_readonly REVOKEd (the asymmetry that closes the leak);
//   - the actual exploit — a schema-qualified read (and, for DML, write) of B
//     from inside a dual preview — now fails with permission denied;
//   - the positive paths (a dual preview reading/writing its own decoy, and
//     the no-decoy fallback reading B directly) still work exactly as before;
//   - a decoy schema left over from the pre-ARCH-020 grant posture is
//     self-healed by CreateRestrictedUser (the boot-time reconciler) with no
//     migration and no manual step.
//
// Uses openTestRepo/uniqueUsername/mkPracticeCategory/mkExam (shared with the
// other integration tests in this package) and skips cleanly when
// TEST_DATABASE_URL is unset, so `go test ./...` stays green on a machine
// with no database.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/... -run PreviewRole
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/... -run EvaluatePreview_Dual

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// hasSchemaUsage reports has_schema_privilege(role, schema, 'USAGE').
func hasSchemaUsage(t *testing.T, repo *PostgresRepository, ctx context.Context, role, schema string) bool {
	t.Helper()
	var ok bool
	if err := repo.pool.QueryRow(ctx,
		"SELECT has_schema_privilege($1, $2, 'USAGE')", role, schema).Scan(&ok); err != nil {
		t.Fatalf("has_schema_privilege(%s, %s, USAGE): %v", role, schema, err)
	}
	return ok
}

// hasTablePriv reports has_table_privilege(role, schema.table, priv).
func hasTablePriv(t *testing.T, repo *PostgresRepository, ctx context.Context, role, schema, table, priv string) bool {
	t.Helper()
	var ok bool
	qualified := fmt.Sprintf("%s.%s", schema, table)
	if err := repo.pool.QueryRow(ctx,
		"SELECT has_table_privilege($1, $2, $3)", role, qualified, priv).Scan(&ok); err != nil {
		t.Fatalf("has_table_privilege(%s, %s, %s): %v", role, qualified, priv, err)
	}
	return ok
}

// TestPreviewRoles_ExistAndIdempotent proves CreateRestrictedUser creates
// exam_student_preview and exam_student_preview_readonly, both NOLOGIN, and
// that running it twice against the same database is safe (the DO-block
// pg_catalog.pg_roles guard, same pattern as the pre-existing exam_student /
// exam_student_readonly roles).
func TestPreviewRoles_ExistAndIdempotent(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	repo := openTestRepo(t)
	ctx := context.Background()

	if err := repo.CreateRestrictedUser(ctx); err != nil {
		t.Fatalf("CreateRestrictedUser (1st call): %v", err)
	}
	if err := repo.CreateRestrictedUser(ctx); err != nil {
		t.Fatalf("CreateRestrictedUser (2nd call): %v", err)
	}

	rows, err := repo.pool.Query(ctx,
		`SELECT rolname, rolcanlogin FROM pg_catalog.pg_roles WHERE rolname IN ($1, $2)`,
		rolePreviewStudent, rolePreviewStudentReadonly)
	if err != nil {
		t.Fatalf("query pg_roles: %v", err)
	}
	defer rows.Close()

	found := map[string]bool{}
	for rows.Next() {
		var name string
		var canLogin bool
		if err := rows.Scan(&name, &canLogin); err != nil {
			t.Fatalf("scan pg_roles row: %v", err)
		}
		if canLogin {
			t.Errorf("role %q must be NOLOGIN, got rolcanlogin = true", name)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pg_roles: %v", err)
	}
	if !found[rolePreviewStudent] || !found[rolePreviewStudentReadonly] {
		t.Fatalf("expected both preview roles to exist after CreateRestrictedUser, found=%v", found)
	}
}

// TestPreviewRoles_HardInvariant_NoUsageOnRealSchemas proves neither new role
// ever holds USAGE on any real schema: public/app-adjacent shared schemas,
// the practice sandbox, a real practice category schema, a real exam
// curriculum schema, and a real exam DML sandbox. This is the hard invariant
// ARCH-020 requires — the new roles' only privileges must come from decoy
// grants.
func TestPreviewRoles_HardInvariant_NoUsageOnRealSchemas(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	if err := repo.CreateRestrictedUser(ctx); err != nil {
		t.Fatalf("CreateRestrictedUser: %v", err)
	}

	catID, uid := mkPracticeCategory(t, repo, ctx, "role_inv_cat")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()
	realCatSchema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	examID := mkExam(t, repo, ctx, "role_inv_exam")
	defer func() { _ = repo.DeleteExamination(ctx, examID) }()
	if err := repo.ImportSchemaIntoExamCurriculum(ctx, examID, "CREATE TABLE t (id INTEGER PRIMARY KEY);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCurriculum: %v", err)
	}
	realExamCurriculum := ExamCurriculumSchemaName(examID)
	if err := repo.InitializeExamSandbox(ctx, examID); err != nil {
		t.Fatalf("InitializeExamSandbox: %v", err)
	}
	realExamSandbox := ExamSandboxName(examID)

	if err := repo.InitializePracticeSandbox(ctx); err != nil {
		t.Fatalf("InitializePracticeSandbox: %v", err)
	}

	realSchemas := []string{"app", practiceSandboxSchema, realCatSchema, realExamCurriculum, realExamSandbox}
	for _, role := range []string{rolePreviewStudent, rolePreviewStudentReadonly} {
		for _, schema := range realSchemas {
			if hasSchemaUsage(t, repo, ctx, role, schema) {
				t.Errorf("HARD INVARIANT VIOLATED: role %q has USAGE on real schema %q", role, schema)
			}
		}
	}
}

// TestPreviewRoles_DecoyGrantTopology proves the positive grant topology on a
// decoy schema — exam_student_preview_readonly gets USAGE+SELECT, the DML
// decoy additionally gives exam_student_preview INSERT/UPDATE/DELETE — and
// the asymmetry that makes ARCH-020 work: exam_student / exam_student_readonly
// hold NO privileges at all on the decoy, even though ensureCategorySchema
// grants them everywhere else.
func TestPreviewRoles_DecoyGrantTopology(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "role_grant_cat")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID, "CREATE TABLE t (id INTEGER PRIMARY KEY, val INTEGER NOT NULL);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	if _, err := repo.ImportSchemaIntoCategoryPreview(ctx, catID, "CREATE TABLE t (id INTEGER PRIMARY KEY, val INTEGER NOT NULL);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategoryPreview: %v", err)
	}
	decoy := PracticeCategoryPreviewSchemaName(catID)

	if !hasSchemaUsage(t, repo, ctx, rolePreviewStudentReadonly, decoy) {
		t.Errorf("expected %s to have USAGE on decoy schema %s", rolePreviewStudentReadonly, decoy)
	}
	if !hasTablePriv(t, repo, ctx, rolePreviewStudentReadonly, decoy, "t", "SELECT") {
		t.Errorf("expected %s to have SELECT on %s.t", rolePreviewStudentReadonly, decoy)
	}
	if !hasSchemaUsage(t, repo, ctx, rolePreviewStudent, decoy) {
		t.Errorf("expected %s to have USAGE on decoy schema %s (allowDML=true)", rolePreviewStudent, decoy)
	}
	for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
		if !hasTablePriv(t, repo, ctx, rolePreviewStudent, decoy, "t", priv) {
			t.Errorf("expected %s to have %s on %s.t", rolePreviewStudent, priv, decoy)
		}
	}

	// The asymmetry: exam_student / exam_student_readonly must hold nothing on
	// the decoy.
	for _, role := range []string{"exam_student", "exam_student_readonly"} {
		if hasSchemaUsage(t, repo, ctx, role, decoy) {
			t.Errorf("expected %s to have NO USAGE on decoy schema %s (ARCH-020 asymmetry)", role, decoy)
		}
		if hasTablePriv(t, repo, ctx, role, decoy, "t", "SELECT") {
			t.Errorf("expected %s to have NO SELECT on %s.t (ARCH-020 asymmetry)", role, decoy)
		}
	}
}

// TestReconcilePreviewSchemaGrants_SelfHealsLegacyDecoy simulates a decoy
// created by the pre-ARCH-020 code — granted to exam_student /
// exam_student_readonly, with no grants at all for the new preview roles —
// and proves a single CreateRestrictedUser call (the boot path) brings it
// onto the ARCH-020 posture: the new roles gain access and the legacy roles
// lose it. No migration, no manual SQL.
func TestReconcilePreviewSchemaGrants_SelfHealsLegacyDecoy(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	// Synthetic name matching the practice_cat_*_preview canonical pattern so
	// reconcilePreviewSchemaGrants' enumeration picks it up.
	legacyDecoy := fmt.Sprintf("practice_cat_legacyreconcile%d_preview", time.Now().UnixNano())
	if err := repo.EnsureSchema(ctx, legacyDecoy); err != nil {
		t.Fatalf("EnsureSchema(%q): %v", legacyDecoy, err)
	}
	defer func() {
		_, _ = repo.pool.Exec(ctx, fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, legacyDecoy))
	}()
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE "%s".t (id INTEGER PRIMARY KEY)`, legacyDecoy)); err != nil {
		t.Fatalf("create legacy decoy table: %v", err)
	}
	// Reproduce the OLD (pre-ARCH-020) ensureCategoryPreviewSchema grant
	// posture directly, bypassing the now-fixed helper.
	if err := repo.GrantSchemaPermissions(ctx, legacyDecoy); err != nil {
		t.Fatalf("simulate legacy exam_student grant: %v", err)
	}
	for _, q := range []string{
		fmt.Sprintf(`GRANT USAGE ON SCHEMA "%s" TO exam_student_readonly`, legacyDecoy),
		fmt.Sprintf(`GRANT SELECT ON ALL TABLES IN SCHEMA "%s" TO exam_student_readonly`, legacyDecoy),
	} {
		if _, err := repo.pool.Exec(ctx, q); err != nil {
			t.Fatalf("simulate legacy exam_student_readonly grant: %v", err)
		}
	}

	// Before reconciliation: legacy roles have access, new roles do not.
	if !hasSchemaUsage(t, repo, ctx, "exam_student_readonly", legacyDecoy) {
		t.Fatal("test setup broken: legacy exam_student_readonly grant did not take")
	}
	if hasSchemaUsage(t, repo, ctx, rolePreviewStudentReadonly, legacyDecoy) {
		t.Fatal("test setup broken: preview role already has USAGE before reconciliation")
	}

	if err := repo.CreateRestrictedUser(ctx); err != nil {
		t.Fatalf("CreateRestrictedUser (self-heal pass): %v", err)
	}

	if !hasSchemaUsage(t, repo, ctx, rolePreviewStudentReadonly, legacyDecoy) {
		t.Errorf("expected %s to gain USAGE on legacy decoy %s after reconciliation", rolePreviewStudentReadonly, legacyDecoy)
	}
	if !hasTablePriv(t, repo, ctx, rolePreviewStudentReadonly, legacyDecoy, "t", "SELECT") {
		t.Errorf("expected %s to gain SELECT on %s.t after reconciliation", rolePreviewStudentReadonly, legacyDecoy)
	}
	if hasSchemaUsage(t, repo, ctx, "exam_student", legacyDecoy) {
		t.Errorf("expected exam_student to LOSE USAGE on legacy decoy %s after reconciliation", legacyDecoy)
	}
	if hasSchemaUsage(t, repo, ctx, "exam_student_readonly", legacyDecoy) {
		t.Errorf("expected exam_student_readonly to LOSE USAGE on legacy decoy %s after reconciliation", legacyDecoy)
	}
	// practice_cat_*_preview => allowDML=true, so the writable role must also
	// have been granted.
	if !hasTablePriv(t, repo, ctx, rolePreviewStudent, legacyDecoy, "t", "INSERT") {
		t.Errorf("expected %s to gain INSERT on %s.t after reconciliation (allowDML for practice_cat_*_preview)", rolePreviewStudent, legacyDecoy)
	}
}

// mkDualQueryCategory creates a fresh practice category with a real schema
// table "secret" holding a distinguishing row, plus a same-structure decoy
// preview schema, and returns (catID, uid, realSchema, decoySchema).
func mkDualQueryCategory(t *testing.T, repo *PostgresRepository, ctx context.Context, namePrefix string) (catID, uid int, realSchema, decoySchema string) {
	t.Helper()
	catID, uid = mkPracticeCategory(t, repo, ctx, namePrefix)

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE secret (id INTEGER PRIMARY KEY, val TEXT NOT NULL); INSERT INTO secret VALUES (1, 'real-B-data');",
		uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	if _, err := repo.ImportSchemaIntoCategoryPreview(ctx, catID,
		"CREATE TABLE secret (id INTEGER PRIMARY KEY, val TEXT NOT NULL); INSERT INTO secret VALUES (1, 'decoy-A-data');",
		uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategoryPreview: %v", err)
	}

	realSchema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}
	decoySchema, err = repo.GetPracticeCategoryPreviewSchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategoryPreviewSchema: %v", err)
	}
	return catID, uid, realSchema, decoySchema
}

// TestEvaluatePreview_DualQuery_SchemaQualifiedReadOfB_Denied is the exploit
// this TODO closes: from inside a dual QUERY preview (previewSchema = the
// decoy), a schema-qualified SELECT against grading schema B must fail with
// insufficient privilege instead of returning B's rows.
func TestEvaluatePreview_DualQuery_SchemaQualifiedReadOfB_Denied(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid, realSchema, decoySchema := mkDualQueryCategory(t, repo, ctx, "dual_query_exploit")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	q := models.Question{Type: "QUERY", DualSchema: true}
	exploitSQL := fmt.Sprintf("SELECT * FROM %s.secret", realSchema)

	result, err := repo.EvaluatePreview(ctx, uid, q, exploitSQL, realSchema, decoySchema)
	if err != nil {
		t.Fatalf("EvaluatePreview returned an error (expected a graceful PreviewResult): %v", err)
	}
	if result.Result.Status != "error" {
		t.Fatalf("expected Status=error for a schema-qualified read of B from a dual preview, got %q (feedback=%q)", result.Result.Status, result.Result.Feedback)
	}
	if !strings.Contains(strings.ToLower(result.Result.Feedback), "permission denied") {
		t.Errorf("expected feedback to contain 'permission denied', got %q", result.Result.Feedback)
	}
	if len(result.Rows) != 0 {
		t.Errorf("expected zero rows disclosed from B, got %d", len(result.Rows))
	}
}

// TestEvaluatePreview_DualQuery_AnotherOwnersRealSchema_Denied proves the
// denial is not schema-specific: a dual preview also cannot read a
// completely different owner's real schema, not just its own paired B.
func TestEvaluatePreview_DualQuery_AnotherOwnersRealSchema_Denied(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid, _, decoySchema := mkDualQueryCategory(t, repo, ctx, "dual_query_own")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	otherCatID, otherUID := mkPracticeCategory(t, repo, ctx, "dual_query_other")
	defer func() { _ = repo.DeletePracticeCategory(ctx, otherCatID, true) }()
	if err := repo.ImportSchemaIntoCategory(ctx, otherCatID,
		"CREATE TABLE secret (id INTEGER PRIMARY KEY, val TEXT NOT NULL); INSERT INTO secret VALUES (1, 'other-owner-data');",
		otherUID, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory (other owner): %v", err)
	}
	otherRealSchema, err := repo.GetPracticeCategorySchema(ctx, otherCatID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema (other owner): %v", err)
	}

	q := models.Question{Type: "QUERY", DualSchema: true}
	exploitSQL := fmt.Sprintf("SELECT * FROM %s.secret", otherRealSchema)

	result, err := repo.EvaluatePreview(ctx, uid, q, exploitSQL, otherRealSchema, decoySchema)
	if err != nil {
		t.Fatalf("EvaluatePreview returned an error: %v", err)
	}
	if result.Result.Status != "error" || !strings.Contains(strings.ToLower(result.Result.Feedback), "permission denied") {
		t.Fatalf("expected permission-denied error reading another owner's real schema from a dual preview, got status=%q feedback=%q", result.Result.Status, result.Result.Feedback)
	}
}

// TestEvaluatePreview_DualDML_ReadAndWriteOfB_Denied proves a dual DML
// preview cannot SELECT from or INSERT into grading schema B, and that B's
// row count is unchanged afterward.
func TestEvaluatePreview_DualDML_ReadAndWriteOfB_Denied(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "dual_dml_exploit")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE secret (id INTEGER PRIMARY KEY, val TEXT NOT NULL); INSERT INTO secret VALUES (1, 'real-B-data');",
		uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	if _, err := repo.ImportSchemaIntoCategoryPreview(ctx, catID,
		"CREATE TABLE secret (id INTEGER PRIMARY KEY, val TEXT NOT NULL); INSERT INTO secret VALUES (1, 'decoy-A-data');",
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

	var beforeCount int
	if err := repo.pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s.secret", realSchema)).Scan(&beforeCount); err != nil {
		t.Fatalf("count B rows before: %v", err)
	}

	q := models.Question{Type: "DML", DualSchema: true, TargetTable: "secret"}

	readSQL := fmt.Sprintf("SELECT * FROM %s.secret", realSchema)
	readResult, err := repo.EvaluatePreview(ctx, uid, q, readSQL, realSchema, decoySchema)
	if err != nil {
		t.Fatalf("EvaluatePreview (read): %v", err)
	}
	if readResult.Result.Status != "error" || !strings.Contains(strings.ToLower(readResult.Result.Feedback), "permission denied") {
		t.Errorf("expected permission-denied reading B from a dual DML preview, got status=%q feedback=%q", readResult.Result.Status, readResult.Result.Feedback)
	}

	writeSQL := fmt.Sprintf("INSERT INTO %s.secret (id, val) VALUES (999, 'leaked-write')", realSchema)
	writeResult, err := repo.EvaluatePreview(ctx, uid, q, writeSQL, realSchema, decoySchema)
	if err != nil {
		t.Fatalf("EvaluatePreview (write): %v", err)
	}
	if writeResult.Result.Status != "error" || !strings.Contains(strings.ToLower(writeResult.Result.Feedback), "permission denied") {
		t.Errorf("expected permission-denied writing to B from a dual DML preview, got status=%q feedback=%q", writeResult.Result.Status, writeResult.Result.Feedback)
	}

	var afterCount int
	if err := repo.pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s.secret", realSchema)).Scan(&afterCount); err != nil {
		t.Fatalf("count B rows after: %v", err)
	}
	if afterCount != beforeCount {
		t.Errorf("expected B's row count to be unchanged (%d), got %d", beforeCount, afterCount)
	}
}

// TestEvaluatePreview_DualQuery_PositivePathUnbroken proves the fix does not
// break the legitimate dual preview: unqualified SQL against the decoy still
// returns the decoy's own rows successfully.
func TestEvaluatePreview_DualQuery_PositivePathUnbroken(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid, realSchema, decoySchema := mkDualQueryCategory(t, repo, ctx, "dual_query_positive")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	q := models.Question{Type: "QUERY", DualSchema: true}
	result, err := repo.EvaluatePreview(ctx, uid, q, "SELECT * FROM secret", realSchema, decoySchema)
	if err != nil {
		t.Fatalf("EvaluatePreview: %v", err)
	}
	if result.Result.Status != "success" {
		t.Fatalf("expected a legitimate dual preview to succeed, got status=%q feedback=%q", result.Result.Status, result.Result.Feedback)
	}
	if len(result.Rows) != 1 || result.Rows[0]["val"] != "decoy-A-data" {
		t.Errorf("expected the decoy's own row ('decoy-A-data'), got %+v", result.Rows)
	}
}

// TestEvaluatePreview_NoDecoyFallback_RunsOnRealSchema proves spec §6.1's
// fallback still works: dual_schema=true with previewSchema == "" (no decoy
// resolved by the controller) runs the preview on B under today's role, never
// blocking the student.
func TestEvaluatePreview_NoDecoyFallback_RunsOnRealSchema(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "dual_fallback")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE secret (id INTEGER PRIMARY KEY, val TEXT NOT NULL); INSERT INTO secret VALUES (1, 'real-B-data');",
		uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	realSchema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	q := models.Question{Type: "QUERY", DualSchema: true}
	result, err := repo.EvaluatePreview(ctx, uid, q, "SELECT * FROM secret", realSchema, "" /* no decoy */)
	if err != nil {
		t.Fatalf("EvaluatePreview: %v", err)
	}
	if result.Result.Status != "success" {
		t.Fatalf("expected the no-decoy fallback to succeed against B, got status=%q feedback=%q", result.Result.Status, result.Result.Feedback)
	}
	if len(result.Rows) != 1 || result.Rows[0]["val"] != "real-B-data" {
		t.Errorf("expected B's own row ('real-B-data'), got %+v", result.Rows)
	}
}
