package repository

// R1 (ARCH-014/ARCH-013 top item): pgx v5's transparent scanning of named-int
// Go types (models.UserID, models.ExamID, ...) — including the NULLABLE
// pointer case Question.ExamID (*models.ExamID) — is a runtime assumption,
// not compile-checked. These tests INSERT then SCAN a row through the real
// repository methods and assert: (a) the scanned typed id equals the
// original int input, (b) the nullable pointer case round-trips both when
// present and when NULL, and (c) the scanned value JSON-encodes to a valid
// publicid token (proving pgx handed back a real Go int under the hood, not
// some other representation MarshalJSON can't cope with).
//
// Requires TEST_DATABASE_URL; skips cleanly (via openTestRepo/uniqueUsername,
// shared with postgres_questions_id_test.go in this package) when absent.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
)

// TestPgxScan_QuestionExamID_NonNull_RoundTrips is the highest-risk R1 case
// flagged by the validator: Question.ExamID is *models.ExamID (a pointer to
// a named-int type). CreateQuestion writes it, GetQuestion scans it back via
// &q.ExamID (**ExamID). If pgx's reflection can't resolve the underlying
// int32/int64 kind through the named-type-and-pointer wrapping, this either
// errors or silently corrupts the value — this test would catch either.
func TestPgxScan_QuestionExamID_NonNull_RoundTrips(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, tbl, uid := mkExamAndTable(t, repo, ctx)

	eid := models.ExamID(examID)
	created := models.Question{
		ExamID:      &eid,
		TargetTable: tbl,
		CorrectSQL:  "SELECT id FROM " + tbl + " ORDER BY id",
		Type:        "QUERY",
		Points:      3,
		Requirement: "R1 scan check",
		CreatedBy:   models.UserID(uid),
	}

	newID, err := repo.CreateQuestion(ctx, created)
	if err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}

	got, err := repo.GetQuestion(ctx, newID)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}

	// Nullable *ExamID must have been scanned back non-nil and equal to input.
	if got.ExamID == nil {
		t.Fatalf("GetQuestion: ExamID scanned as nil, want %d (pgx reflection gap on *models.ExamID)", examID)
	}
	if int(*got.ExamID) != examID {
		t.Fatalf("GetQuestion: ExamID = %d, want %d", int(*got.ExamID), examID)
	}

	// Non-pointer named-int CreatedBy must also have round-tripped.
	if int(got.CreatedBy) != uid {
		t.Fatalf("GetQuestion: CreatedBy = %d, want %d", int(got.CreatedBy), uid)
	}

	// The scanned typed ids must JSON-encode to valid, decodable tokens (not
	// panic, not silently emit a zero/garbage value).
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal scanned Question: %v", err)
	}
	var decoded struct {
		ExamID    string `json:"exam_id"`
		CreatedBy string `json:"created_by"`
		ID        int    `json:"id"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal scanned Question JSON: %v", err)
	}
	if gotExamID, err := publicid.Decode(publicid.TypeExam, decoded.ExamID); err != nil || gotExamID != examID {
		t.Fatalf("scanned exam_id token %q did not decode to %d (err=%v, got=%d)", decoded.ExamID, examID, err, gotExamID)
	}
	if gotUID, err := publicid.Decode(publicid.TypeUser, decoded.CreatedBy); err != nil || gotUID != uid {
		t.Fatalf("scanned created_by token %q did not decode to %d (err=%v, got=%d)", decoded.CreatedBy, uid, err, gotUID)
	}
	// Question.ID (the exam-question ordinal) stays a bare JSON number per
	// ARCH-015 — sanity-check it round-tripped as the real scanned id, not 0.
	if decoded.ID != newID {
		t.Fatalf("scanned id = %d, want %d", decoded.ID, newID)
	}
}

// TestPgxScan_QuestionExamID_Null_RoundTrips is the complementary edge case:
// a practice question has ExamID == nil (no exam FK at all). GetQuestion must
// scan the NULL column back as a nil *models.ExamID, not a non-nil pointer to
// zero (which would be indistinguishable from "exam id 0").
func TestPgxScan_QuestionExamID_Null_RoundTrips(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("q_practice_author"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	practiceQ := models.Question{
		ExamID:      nil, // practice question: no exam FK
		TargetTable: "",
		CorrectSQL:  "SELECT 1",
		Type:        "QUERY",
		Points:      1,
		Requirement: "practice, no exam",
		CreatedBy:   models.UserID(uid),
	}

	newID, err := repo.CreateQuestion(ctx, practiceQ)
	if err != nil {
		t.Fatalf("CreateQuestion (nil ExamID): %v", err)
	}

	got, err := repo.GetQuestion(ctx, newID)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}

	if got.ExamID != nil {
		t.Fatalf("GetQuestion: ExamID scanned as %v, want nil (NULL exam_id must not become a spurious pointer-to-zero)", *got.ExamID)
	}

	// A nil *ExamID must marshal as JSON null, not "\"E...\"" (0) or omitted
	// silently turning into a real-looking-but-wrong token.
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, ok := m["exam_id"]; ok && v != nil {
		t.Fatalf("exam_id must be null (or omitted) for a nil ExamID, got %v", v)
	}
}

// TestPgxScan_ExamAndUser_NamedIntFields_RoundTrip is bonus R1 coverage for
// the two other named-int scan sites the validator flagged:
// GetExamByID(&e.ID ExamID, &e.CreatedBy UserID) and
// GetUserByID(&u.ID UserID, &u.CreatedBy *UserID).
func TestPgxScan_ExamAndUser_NamedIntFields_RoundTrip(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("r1_creator"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	var examID int
	if err := repo.pool.QueryRow(ctx,
		`INSERT INTO app.examinations (title, created_by) VALUES ($1, $2) RETURNING id`,
		"R1 Scan Exam", uid).Scan(&examID); err != nil {
		t.Fatalf("insert exam: %v", err)
	}

	exam, err := repo.GetExamByID(ctx, examID)
	if err != nil {
		t.Fatalf("GetExamByID: %v", err)
	}
	if int(exam.ID) != examID {
		t.Fatalf("Examination.ID = %d, want %d", int(exam.ID), examID)
	}
	if int(exam.CreatedBy) != uid {
		t.Fatalf("Examination.CreatedBy = %d, want %d", int(exam.CreatedBy), uid)
	}

	user, err := repo.GetUserByID(ctx, uid)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if int(user.ID) != uid {
		t.Fatalf("User.ID = %d, want %d", int(user.ID), uid)
	}
	// This user was created with no CreatedBy (self-registered/seed path) —
	// exercise the nullable *UserID branch on User too.
	if user.CreatedBy != nil {
		t.Fatalf("User.CreatedBy: want nil (no creator set), got %v", *user.CreatedBy)
	}
}
