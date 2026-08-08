package repository

// Regression-tester (step [7]) coverage for the lecturer-authored seed rows
// feature (Task 2, ARCH-043/049): the additive dml_kind/seed_sql columns on
// app.questions and app.practice_questions, and the Go-side backfill that
// derives dml_kind for DML rows written before the column existed.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/... -run 'TestSeedSQLColumnsExist|TestMigrateDMLKind' -p 1 -v

import (
	"context"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// TestSeedSQLColumnsExist asserts both additive columns landed on both
// tables.
func TestSeedSQLColumnsExist(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	for _, table := range []string{"questions", "practice_questions"} {
		for _, col := range []string{"dml_kind", "seed_sql"} {
			var n int
			err := repo.pool.QueryRow(ctx, `
				SELECT count(*) FROM information_schema.columns
				WHERE table_schema = 'app' AND table_name = $1 AND column_name = $2`,
				table, col).Scan(&n)
			if err != nil {
				t.Fatalf("query information_schema: %v", err)
			}
			if n != 1 {
				t.Errorf("app.%s is missing column %s", table, col)
			}
		}
	}
}

// TestMigrateDMLKindIsIdempotentAndCorrect inserts DML rows with a NULL
// dml_kind, runs the backfill twice, and asserts the derived kind — including
// the case a SQL regex would get wrong, a statement hidden behind a comment.
func TestMigrateDMLKindIsIdempotentAndCorrect(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("seedsql_migration_author"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cat, err := repo.CreatePracticeCategory(ctx, uniqueUsername("seedsql_migration_cat"), uid)
	if err != nil {
		t.Fatalf("CreatePracticeCategory: %v", err)
	}
	defer func() { _ = repo.DeletePracticeCategory(ctx, int(cat.ID), true) }()

	cases := []struct {
		sql  string
		want string
	}{
		{"DELETE FROM employee WHERE ssn = '1'", "DELETE"},
		{"UPDATE employee SET fname = 'x' WHERE ssn = '1'", "UPDATE"},
		{"INSERT INTO employee (ssn) VALUES ('1')", "INSERT"},
		{"-- remove the row\nDELETE FROM employee WHERE ssn = '1'", "DELETE"},
		{"/* c */ update employee set fname='x' where ssn='1'", "UPDATE"},
	}

	var ids []int
	for _, tc := range cases {
		var id int
		err := repo.pool.QueryRow(ctx, `
			INSERT INTO app.practice_questions
				(created_by, category_id, target_table, correct_sql, question_type, requirement, dml_kind)
			VALUES ($1, $2, 'employee', $3, 'DML', 'seed backfill fixture', NULL)
			RETURNING id`, uid, int(cat.ID), tc.sql).Scan(&id)
		if err != nil {
			t.Fatalf("insert fixture: %v", err)
		}
		ids = append(ids, id)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = repo.pool.Exec(ctx, `DELETE FROM app.practice_questions WHERE id = $1`, id)
		}
	})

	for run := 0; run < 2; run++ {
		if err := repo.migrateDMLKind(ctx); err != nil {
			t.Fatalf("migrateDMLKind run %d: %v", run, err)
		}
		for i, tc := range cases {
			var got *string
			if err := repo.pool.QueryRow(ctx,
				`SELECT dml_kind FROM app.practice_questions WHERE id = $1`, ids[i]).Scan(&got); err != nil {
				t.Fatalf("read back: %v", err)
			}
			if got == nil || *got != tc.want {
				t.Errorf("run %d, sql %q: dml_kind = %v, want %q", run, tc.sql, got, tc.want)
			}
		}
	}
}
