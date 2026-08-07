package repository

// Integration tests for preview column ordering.
//
// The three preview entry points used to return only []map[string]interface{}.
// Both preview controllers rebuilt the header by ranging rows[0], and Go
// randomises map iteration, so the lecturer's preview table shuffled its
// columns on every request — a SELECT written as (name, score) could render as
// (score, name). The preview funcs now also return the pgx FieldDescriptions
// order, which is the SELECT projection order (for DML, the post-image
// snapshot's column order).
//
// These are DB integration tests: column order comes from the engine's field
// descriptions, so there is nothing to assert without a real query. They use
// openTestRepo/mkPracticeCategory (shared with the other integration tests in
// this package) and skip cleanly when TEST_DATABASE_URL is unset, so
// `go test ./...` stays green on a machine with no database.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/... -run PreviewColumnOrder

import (
	"context"
	"fmt"
	"testing"
)

// previewOrderReps is how many times each preview is repeated. One pass can
// pass by luck under the old map-range code; a handful cannot (with 3 columns
// a random order matches the projection 1 time in 6).
const previewOrderReps = 12

func assertCols(t *testing.T, got, want []string, ctx string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got columns %v, want %v", ctx, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: got columns %v, want %v", ctx, got, want)
		}
	}
}

// TestDedupePreviewColumns is a DB-free unit test for the ARCH-044 helper.
// pgx FieldDescriptions is not deduplicated but the row maps built from it are
// (rowMap[fd.Name] overwrites), so an ordered header taken raw from the field
// descriptions could list more columns than the rows carry — a phantom column
// plus duplicate v-for keys in ResultTable.vue / ExamQuestionsPanel.vue.
func TestDedupePreviewColumns(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"no duplicates", []string{"zeta", "alpha", "mid"}, []string{"zeta", "alpha", "mid"}},
		{"trailing duplicate keeps first occurrence", []string{"a", "b", "a"}, []string{"a", "b"}},
		{"adjacent duplicate (SELECT * FROM employee, department)",
			[]string{"ssn", "dno", "dname", "dno"}, []string{"ssn", "dno", "dname"}},
		{"all duplicates", []string{"v", "v", "v"}, []string{"v"}},
		{"empty slice", []string{}, []string{}},
		{"nil slice", nil, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dedupePreviewColumns(tc.in)
			if got == nil {
				t.Fatalf("dedupePreviewColumns(%v) = nil, want non-nil (controllers rely on emitting [] not null)", tc.in)
			}
			assertCols(t, got, tc.want, tc.name)
		})
	}
}

// TestPreviewColumnOrder_DuplicateColumnNames is the DB-backed end-to-end case
// for ARCH-044: the engine reports two fields named "v", the row map holds one
// key, so the preview must report exactly one column.
func TestPreviewColumnOrder_DuplicateColumnNames(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	rows, cols, err := repo.PreviewCorrectSQL(ctx, "QUERY", "", "SELECT 1 AS v, 2 AS v")
	if err != nil {
		t.Fatalf("PreviewCorrectSQL: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	assertCols(t, cols, []string{"v"}, "duplicate column names")
	if len(rows[0]) != len(cols) {
		t.Fatalf("columns %v do not match row keys %v", cols, rows[0])
	}
}

// TestPreviewColumnOrder_QueryProjection pins a QUERY preview's columns to the
// SELECT projection order. The aliases are deliberately not in alphabetical or
// reverse-alphabetical order so no accidental sort can pass.
func TestPreviewColumnOrder_QueryProjection(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	want := []string{"zeta", "alpha", "mid"}
	for i := 0; i < previewOrderReps; i++ {
		rows, cols, err := repo.PreviewCorrectSQL(ctx, "QUERY", "", "SELECT 1 AS zeta, 2 AS alpha, 3 AS mid")
		if err != nil {
			t.Fatalf("PreviewCorrectSQL: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("expected 1 row, got %d", len(rows))
		}
		assertCols(t, cols, want, fmt.Sprintf("rep %d", i))
	}
}

// TestPreviewColumnOrder_EmptyResultSetStillOrdered proves the columns survive
// a zero-row result. The old code derived the header from rows[0] and so
// produced no columns at all when the query matched nothing — the lecturer saw
// an empty table with no header instead of the projection they wrote.
func TestPreviewColumnOrder_EmptyResultSetStillOrdered(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	rows, cols, err := repo.PreviewCorrectSQL(ctx, "QUERY",
		"", "SELECT 1 AS zeta, 2 AS alpha, 3 AS mid WHERE false")
	if err != nil {
		t.Fatalf("PreviewCorrectSQL: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows, got %d", len(rows))
	}
	assertCols(t, cols, []string{"zeta", "alpha", "mid"}, "empty result set")
}

// TestPreviewColumnOrder_PracticeQueryAndDML covers the practice preview in
// both modes: a QUERY keeps its projection order, and a DML post-image keeps
// the target table's physical column order.
func TestPreviewColumnOrder_PracticeQueryAndDML(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, _ := mkPracticeCategory(t, repo, ctx, "prev_colorder")

	schema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE %s.pcol_target (zeta text, alpha int, mid text)`, schema)); err != nil {
		t.Fatalf("create fixture table: %v", err)
	}
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf(
		`INSERT INTO %s.pcol_target VALUES ('z', 1, 'm')`, schema)); err != nil {
		t.Fatalf("seed fixture table: %v", err)
	}

	t.Run("query", func(t *testing.T) {
		want := []string{"mid", "zeta", "alpha"}
		for i := 0; i < previewOrderReps; i++ {
			_, cols, err := repo.PreviewPracticeCorrectSQL(ctx, catID, "QUERY", "",
				"SELECT mid, zeta, alpha FROM pcol_target", false)
			if err != nil {
				t.Fatalf("PreviewPracticeCorrectSQL: %v", err)
			}
			assertCols(t, cols, want, fmt.Sprintf("rep %d", i))
		}
	})

	t.Run("dml_post_image", func(t *testing.T) {
		// For DML the rows are the post-image snapshot (SELECT * FROM target),
		// so the expected order is the table's physical column order.
		want := []string{"zeta", "alpha", "mid"}
		for i := 0; i < previewOrderReps; i++ {
			rows, cols, err := repo.PreviewPracticeCorrectSQL(ctx, catID, "DML", "pcol_target",
				"UPDATE pcol_target SET alpha = 42", false)
			if err != nil {
				t.Fatalf("PreviewPracticeCorrectSQL (DML): %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("expected 1 post-image row, got %d", len(rows))
			}
			assertCols(t, cols, want, fmt.Sprintf("rep %d", i))
		}
	})

	// The preview transaction is always rolled back, so the fixture row must
	// still hold its original value after the DML previews above.
	var alpha int
	if err := repo.pool.QueryRow(ctx, fmt.Sprintf(
		`SELECT alpha FROM %s.pcol_target`, schema)).Scan(&alpha); err != nil {
		t.Fatalf("read back fixture: %v", err)
	}
	if alpha != 1 {
		t.Fatalf("preview leaked its DML: alpha = %d, want 1", alpha)
	}
}
