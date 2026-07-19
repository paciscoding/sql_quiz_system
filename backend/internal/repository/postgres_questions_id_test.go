package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// mkExamAndTable seeds a lecturer, an examination, and a real capture table in
// the public schema (the default Phase-0 capture schema) so CreateQuestion's
// synchronous capture succeeds. Returns the exam id, the table name, and the
// author user id.
func mkExamAndTable(t *testing.T, repo *PostgresRepository, ctx context.Context) (int, string, int) {
	t.Helper()
	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("q_author"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	var examID int
	if err := repo.pool.QueryRow(ctx,
		`INSERT INTO app.examinations (title, created_by) VALUES ($1, $2) RETURNING id`,
		"TDD Exam", uid).Scan(&examID); err != nil {
		t.Fatalf("insert exam: %v", err)
	}
	tbl := fmt.Sprintf("tdd_q_%d", examID)
	if _, err := repo.pool.Exec(ctx,
		fmt.Sprintf("CREATE TABLE public.%s (id int); INSERT INTO public.%s VALUES (1),(2)", tbl, tbl)); err != nil {
		t.Fatalf("create capture table: %v", err)
	}
	t.Cleanup(func() {
		repo.pool.Exec(context.Background(), "DROP TABLE IF EXISTS public."+tbl)
	})
	return examID, tbl, uid
}

// TestCreateQuestion_AssignsDistinctIds is the regression test for the
// questions.id collision bug: id was a plain INTEGER PRIMARY KEY with no
// sequence, so a question created without a client id inserted id=0 and every
// subsequent no-id insert collided (ON CONFLICT DO UPDATE), silently
// overwriting the prior question. Two no-id creates must yield two distinct,
// positive ids and two persisted rows.
func TestCreateQuestion_AssignsDistinctIds(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, tbl, uid := mkExamAndTable(t, repo, ctx)

	mk := func(req string) models.Question {
		eid := models.ExamID(examID)
		return models.Question{
			ExamID:      &eid,
			TargetTable: tbl,
			CorrectSQL:  "SELECT id FROM " + tbl + " ORDER BY id",
			Type:        "QUERY",
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

	if id1 <= 0 || id2 <= 0 {
		t.Fatalf("ids must be positive & auto-assigned, got id1=%d id2=%d", id1, id2)
	}
	if id1 == id2 {
		t.Fatalf("two no-id questions collided on the same id %d", id1)
	}

	var cnt int
	if err := repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM app.questions WHERE exam_id=$1`, examID).Scan(&cnt); err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 2 {
		t.Fatalf("expected 2 persisted questions, got %d (prior question was clobbered)", cnt)
	}
}

// TestCreateQuestion_UpdateByIdKeepsRow verifies the update path still works:
// calling CreateQuestion with a real (>0) id updates that row in place rather
// than inserting a new one.
func TestCreateQuestion_UpdateByIdKeepsRow(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, tbl, uid := mkExamAndTable(t, repo, ctx)

	eid := models.ExamID(examID)
	base := models.Question{
		ExamID:      &eid,
		TargetTable: tbl,
		CorrectSQL:  "SELECT id FROM " + tbl + " ORDER BY id",
		Type:        "QUERY",
		Points:      5,
		Requirement: "original",
		CreatedBy:   models.UserID(uid),
	}
	id, err := repo.CreateQuestion(ctx, base)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	upd := base
	upd.ID = id
	upd.Requirement = "edited"
	upd.Points = 9
	gotID, err := repo.CreateQuestion(ctx, upd)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if gotID != id {
		t.Fatalf("update should keep id %d, got %d", id, gotID)
	}

	var cnt int
	var req string
	var pts float64
	if err := repo.pool.QueryRow(ctx,
		`SELECT count(*) OVER (), requirement, points FROM app.questions WHERE id=$1`, id).
		Scan(&cnt, &req, &pts); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if req != "edited" || pts != 9 {
		t.Fatalf("update didn't apply: requirement=%q points=%v", req, pts)
	}
	// Total rows for this exam must still be exactly 1 (updated in place).
	var total int
	if err := repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM app.questions WHERE exam_id=$1`, examID).Scan(&total); err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 1 {
		t.Fatalf("update must not insert a new row; got %d rows", total)
	}
}
