package repository

// Tests for ARCH-031 (3)/TODO-068: the bulk password-reset target-resolution
// and per-student-atomic write primitives in postgres_password_bulk.go.
//
// Uses openTestRepo/uniqueUsername, shared with postgres_password_events_test.go
// and the rest of this package; every test SKIPs cleanly when
// TEST_DATABASE_URL is unset, so `go test ./...` stays green on a machine
// with no database.
//
// Isolation note: app.system_users is a single shared table across every test
// in this package, and this package's tests run sequentially (none of them
// call t.Parallel()). Each test below therefore captures the current max id
// via maxSystemUserID BEFORE creating its own fixture rows and always passes
// that as afterID, so it only ever observes the rows it created itself —
// never leftovers from a test that ran earlier in the same binary.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/... -run BulkReset -v

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// maxSystemUserID returns the current highest id in app.system_users (0 on an
// empty table), used as the isolation high-water mark described above.
func maxSystemUserID(t *testing.T, repo *PostgresRepository, ctx context.Context) int {
	t.Helper()
	var max int
	if err := repo.pool.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM app.system_users`).Scan(&max); err != nil {
		t.Fatalf("maxSystemUserID: %v", err)
	}
	return max
}

// mkBulkResetStudent creates a STUDENT row with every profile column
// populated (student_number/first_name/last_name/sec_lec/sec_lab), since
// ResolveBulkResetTargets projects all of them.
func mkBulkResetStudent(t *testing.T, repo *PostgresRepository, ctx context.Context, prefix, studentNumber string) int {
	t.Helper()
	username := uniqueUsername(prefix)
	uid, err := repo.CreateUser(ctx, models.User{
		Username: username,
		Password: "hash",
		Role:     models.RoleStudent,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	firstName := prefix + "_first"
	lastName := prefix + "_last"
	secLec := "LEC1"
	secLab := "LAB1"
	u := models.User{
		ID:            models.UserID(uid),
		Username:      username,
		Role:          models.RoleStudent,
		StudentNumber: &studentNumber,
		FirstName:     &firstName,
		LastName:      &lastName,
		SecLec:        &secLec,
		SecLab:        &secLab,
	}
	if err := repo.UpdateUser(ctx, u); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	return uid
}

// assertTargetIDs fails the test unless targets' UserIDs are EXACTLY want,
// order-independent.
func assertTargetIDs(t *testing.T, targets []models.BulkResetTarget, want []int) {
	t.Helper()
	got := make([]int, len(targets))
	for i, tg := range targets {
		got[i] = tg.UserID
	}
	sort.Ints(got)
	wantSorted := append([]int{}, want...)
	sort.Ints(wantSorted)
	if len(got) != len(wantSorted) {
		t.Fatalf("expected %d targets %v, got %d %v", len(wantSorted), wantSorted, len(got), got)
	}
	for i := range got {
		if got[i] != wantSorted[i] {
			t.Fatalf("expected targets %v, got %v", wantSorted, got)
		}
	}
}

// TestResolveBulkResetTargets_ThreeModes is F1's repository-level proof: with
// 5 students, 2 of them enrolled in an exam, each of the three modes resolves
// EXACTLY its target set, plus the profile columns are actually carried
// through (not just the ids).
func TestResolveBulkResetTargets_ThreeModes(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	baseID := maxSystemUserID(t, repo, ctx)
	examID := mkExam(t, repo, ctx, "bulkmodes")

	students := make([]int, 5)
	for i := range students {
		students[i] = mkBulkResetStudent(t, repo, ctx, fmt.Sprintf("bulkmodes_s%d", i), fmt.Sprintf("BM-SN-%d", i))
	}
	onExam := students[:2]
	if err := repo.AddStudentsToExam(ctx, examID, onExam); err != nil {
		t.Fatalf("AddStudentsToExam: %v", err)
	}

	// all_students: every STUDENT row created above, nothing else.
	allTargets, allRemaining, err := repo.ResolveBulkResetTargets(ctx, "all_students", nil, 0, baseID, 100)
	if err != nil {
		t.Fatalf("ResolveBulkResetTargets(all_students): %v", err)
	}
	assertTargetIDs(t, allTargets, students)
	if allRemaining != len(students) {
		t.Fatalf("all_students: expected remaining=%d, got %d", len(students), allRemaining)
	}
	// Profile columns actually round-trip, not just ids.
	for _, tg := range allTargets {
		if tg.UserID == students[0] {
			if tg.StudentNumber != "BM-SN-0" {
				t.Fatalf("expected StudentNumber round-tripped, got %q", tg.StudentNumber)
			}
			if tg.FirstName == "" || tg.LastName == "" || tg.SecLec == "" || tg.SecLab == "" {
				t.Fatalf("expected all profile columns populated, got %+v", tg)
			}
		}
	}

	// exam_roster: exactly the 2 enrolled students.
	examTargets, examRemaining, err := repo.ResolveBulkResetTargets(ctx, "exam_roster", nil, examID, baseID, 100)
	if err != nil {
		t.Fatalf("ResolveBulkResetTargets(exam_roster): %v", err)
	}
	assertTargetIDs(t, examTargets, onExam)
	if examRemaining != len(onExam) {
		t.Fatalf("exam_roster: expected remaining=%d, got %d", len(onExam), examRemaining)
	}

	// ids: an explicit 3-of-5 subset (including one NOT on the exam).
	idSubset := []int{students[0], students[2], students[4]}
	idTargets, idRemaining, err := repo.ResolveBulkResetTargets(ctx, "ids", idSubset, 0, baseID, 100)
	if err != nil {
		t.Fatalf("ResolveBulkResetTargets(ids): %v", err)
	}
	assertTargetIDs(t, idTargets, idSubset)
	if idRemaining != len(idSubset) {
		t.Fatalf("ids: expected remaining=%d, got %d", len(idSubset), idRemaining)
	}
}

// TestResolveBulkResetTargets_CursorPaging_NoDuplicates is F8's repository-
// level proof: 250 STUDENT rows, paged with limit=100, produce pages of
// 100/100/50 with a stable id-ASC cursor and no duplicate/missing ids across
// the whole loop.
func TestResolveBulkResetTargets_CursorPaging_NoDuplicates(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	baseID := maxSystemUserID(t, repo, ctx)

	const total = 250
	created := make([]int, 0, total)
	for i := 0; i < total; i++ {
		created = append(created, mkBulkResetStudent(t, repo, ctx,
			fmt.Sprintf("bulkpage_%03d", i), fmt.Sprintf("BP-SN-%03d", i)))
	}

	var pageSizes []int
	seen := map[int]bool{}
	afterID := baseID
	for page := 0; page < 10; page++ { // hard cap so a bug can't infinite-loop the test
		targets, repoRemaining, err := repo.ResolveBulkResetTargets(ctx, "all_students", nil, 0, afterID, 100)
		if err != nil {
			t.Fatalf("ResolveBulkResetTargets page %d: %v", page, err)
		}
		if len(targets) == 0 {
			t.Fatalf("page %d returned zero targets unexpectedly", page)
		}
		for _, tg := range targets {
			if seen[tg.UserID] {
				t.Fatalf("duplicate id %d returned across pages", tg.UserID)
			}
			seen[tg.UserID] = true
		}
		pageSizes = append(pageSizes, len(targets))

		apiRemaining := repoRemaining - len(targets)
		afterID = targets[len(targets)-1].UserID
		if apiRemaining <= 0 {
			break
		}
	}

	wantPages := []int{100, 100, 50}
	if len(pageSizes) != len(wantPages) {
		t.Fatalf("expected %d pages %v, got %d pages %v", len(wantPages), wantPages, len(pageSizes), pageSizes)
	}
	for i := range wantPages {
		if pageSizes[i] != wantPages[i] {
			t.Fatalf("expected page sizes %v, got %v", wantPages, pageSizes)
		}
	}
	if len(seen) != total {
		t.Fatalf("expected %d unique ids across the union of pages, got %d", total, len(seen))
	}
	for _, id := range created {
		if !seen[id] {
			t.Fatalf("created id %d missing from the paged union", id)
		}
	}
}

// TestNonStudentUserIDs_FlagsWrongRoleAndMissing is F6's repository-level
// proof: a LECTURER id, an ADMIN id, and a nonexistent id are all flagged; a
// genuine STUDENT id in the same list is NOT flagged.
func TestNonStudentUserIDs_FlagsWrongRoleAndMissing(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	student := mkBulkResetStudent(t, repo, ctx, "nonstud_student", "NS-1")
	lecturerID, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("nonstud_lecturer"), Password: "hash", Role: models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser (lecturer): %v", err)
	}
	adminID, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("nonstud_admin"), Password: "hash", Role: models.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("CreateUser (admin): %v", err)
	}
	missingID := maxSystemUserID(t, repo, ctx) + 1_000_000 // never assigned

	bad, err := repo.NonStudentUserIDs(ctx, []int{student, lecturerID, adminID, missingID})
	if err != nil {
		t.Fatalf("NonStudentUserIDs: %v", err)
	}

	wantBad := map[int]bool{lecturerID: true, adminID: true, missingID: true}
	if len(bad) != len(wantBad) {
		t.Fatalf("expected %d flagged ids %v, got %d: %v", len(wantBad), wantBad, len(bad), bad)
	}
	for _, id := range bad {
		if !wantBad[id] {
			t.Fatalf("unexpected id %d flagged as non-student", id)
		}
		if id == student {
			t.Fatalf("SECURITY REGRESSION: a genuine STUDENT id was flagged as non-student")
		}
	}
}

// TestApplyPasswordReset_AtomicPerStudent is F5's repository-level proof:
// when the audit-event insert fails (here, via a source value that violates
// app.password_events' CHECK constraint), the password hash and
// token_version updates that already ran earlier in the SAME transaction
// must roll back too — never a changed password with a stale token_version,
// or vice versa. The trailing non-vacuity check proves the harness isn't
// trivially always-failing: the identical call with a valid source commits
// all three effects together.
func TestApplyPasswordReset_AtomicPerStudent(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	uid := mkBulkResetStudent(t, repo, ctx, "atomicity", "AT-1")

	before, err := repo.GetUserByID(ctx, uid)
	if err != nil {
		t.Fatalf("GetUserByID (before): %v", err)
	}
	beforeVersion, err := repo.GetTokenVersion(ctx, uid)
	if err != nil {
		t.Fatalf("GetTokenVersion (before): %v", err)
	}

	// "not_a_real_source" violates password_events_source_check, so the
	// INSERT fails and WithTransaction must roll back the two UPDATEs that
	// already ran ahead of it in this same call.
	err = repo.ApplyPasswordReset(ctx, uid, "forged-hash-should-not-persist", uid, "not_a_real_source", []byte("enc"), []byte("nonce"))
	if err == nil {
		t.Fatal("expected ApplyPasswordReset to fail for an invalid source value")
	}
	t.Logf("ApplyPasswordReset forced failure (expected): %v", err)

	after, err := repo.GetUserByID(ctx, uid)
	if err != nil {
		t.Fatalf("GetUserByID (after): %v", err)
	}
	if after.Password != before.Password {
		t.Fatalf("SECURITY REGRESSION: password hash changed despite the forced failure (before=%q after=%q)", before.Password, after.Password)
	}
	afterVersion, err := repo.GetTokenVersion(ctx, uid)
	if err != nil {
		t.Fatalf("GetTokenVersion (after): %v", err)
	}
	if afterVersion != beforeVersion {
		t.Fatalf("token_version changed despite the forced failure (before=%d after=%d)", beforeVersion, afterVersion)
	}
	events, err := repo.GetPasswordEvents(ctx, uid)
	if err != nil {
		t.Fatalf("GetPasswordEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected no password_events row after the forced failure, got %d", len(events))
	}

	// Non-vacuity: the same call with a valid source commits all three
	// effects together.
	if err := repo.ApplyPasswordReset(ctx, uid, "real-hash", uid, models.PasswordEventAdminReset, []byte("enc2"), []byte("nonce2")); err != nil {
		t.Fatalf("ApplyPasswordReset (valid source): %v", err)
	}
	after2, err := repo.GetUserByID(ctx, uid)
	if err != nil {
		t.Fatalf("GetUserByID (after valid): %v", err)
	}
	if after2.Password != "real-hash" {
		t.Fatalf("expected password hash committed, got %q", after2.Password)
	}
	afterVersion2, err := repo.GetTokenVersion(ctx, uid)
	if err != nil {
		t.Fatalf("GetTokenVersion (after valid): %v", err)
	}
	if afterVersion2 != beforeVersion+1 {
		t.Fatalf("expected token_version=%d after the valid call, got %d", beforeVersion+1, afterVersion2)
	}
	events2, err := repo.GetPasswordEvents(ctx, uid)
	if err != nil {
		t.Fatalf("GetPasswordEvents (after valid): %v", err)
	}
	if len(events2) != 1 || events2[0].Source != models.PasswordEventAdminReset {
		t.Fatalf("expected exactly 1 admin_reset event, got %+v", events2)
	}
}

// TestApplyPasswordReset_NoAuditRowWhenEncNil proves the enc==nil path (no
// PASSWORD_LOG_KEY configured) still commits the hash+token_version effects
// but skips the audit insert entirely, matching AuthService.BulkResetPasswords'
// "one warning for the whole batch, never one per student" behaviour.
func TestApplyPasswordReset_NoAuditRowWhenEncNil(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	uid := mkBulkResetStudent(t, repo, ctx, "noauditrow", "NA-1")
	beforeVersion, err := repo.GetTokenVersion(ctx, uid)
	if err != nil {
		t.Fatalf("GetTokenVersion (before): %v", err)
	}

	if err := repo.ApplyPasswordReset(ctx, uid, "hash-no-audit", uid, models.PasswordEventAdminReset, nil, nil); err != nil {
		t.Fatalf("ApplyPasswordReset (enc=nil): %v", err)
	}

	after, err := repo.GetUserByID(ctx, uid)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if after.Password != "hash-no-audit" {
		t.Fatalf("expected password hash committed even with enc=nil, got %q", after.Password)
	}
	afterVersion, err := repo.GetTokenVersion(ctx, uid)
	if err != nil {
		t.Fatalf("GetTokenVersion (after): %v", err)
	}
	if afterVersion != beforeVersion+1 {
		t.Fatalf("expected token_version bumped even with enc=nil, before=%d after=%d", beforeVersion, afterVersion)
	}
	events, err := repo.GetPasswordEvents(ctx, uid)
	if err != nil {
		t.Fatalf("GetPasswordEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected zero password_events rows when enc=nil, got %d", len(events))
	}
}
