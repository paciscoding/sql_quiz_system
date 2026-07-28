package repository

// Pure-unit tests for TODO-063 (ARCH-028 C1 / ARCH-027 (1) / ARCH-036).
// Nothing here touches a database: ExamGradingSandboxFor, ExamPreviewSandboxFor,
// querySchemaFor and ExamCategoryOwnsSchema are all plain, DB-free functions of
// a models.Question value, and schemaQualifiedNameRe/validateNoSchemaQualifiedNames
// are compiled regex evaluated against a string. This file must pass with
// TEST_DATABASE_URL UNSET — that is the whole point of TODO-063, so it never
// calls openTestRepo or touches r.pool anywhere.
//
// Run: go test ./internal/repository/... -run 'ExamGradingSandboxFor|ExamPreviewSandboxFor|QuerySchemaFor|ExamCategoryOwnsSchema|SchemaQualifiedNameRe' -v

import (
	"errors"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// ─────────────────────────────────────────────────────────────────────────────
// (i)-(iv): ExamGradingSandboxFor / ExamPreviewSandboxFor / querySchemaFor,
// table-driven over the four ARCH-028 C1 cases. Cases (i)-(iii) must resolve
// to EXACTLY the values the same Question (with CategoryID/CategorySchema
// zeroed out) would have resolved to before exam categories existed; only
// case (iv) — an owning category — may diverge.
// ─────────────────────────────────────────────────────────────────────────────

func TestExamGradingAndPreviewSandboxFor_FourCases(t *testing.T) {
	examID := models.ExamID(5)
	catLegacyPublic := models.ExamCategoryID(3)      // legacy-adopted, schema copies 'public'
	catLegacyCurriculum := models.ExamCategoryID(11) // legacy-adopted, schema copies 'curriculum_exam_5'
	catOwning := models.ExamCategoryID(9)            // owns exam_cat_9

	cases := []struct {
		name           string
		q              models.Question
		sandboxSchema  string // only consulted for practice (ExamID==nil) questions
		wantQuery      string
		wantGradingSbx string
		wantPreviewSbx string
	}{
		{
			name:           "(i) ExamID nil (practice question)",
			q:              models.Question{ExamID: nil},
			sandboxSchema:  "practice_cat_42",
			wantQuery:      "practice_cat_42",
			wantGradingSbx: "",
			wantPreviewSbx: "",
		},
		{
			name: "(ii) category nil",
			q: models.Question{
				ExamID:           &examID,
				CurriculumSchema: "curriculum_exam_5",
			},
			wantQuery:      "curriculum_exam_5",
			wantGradingSbx: ExamSandboxName(5),
			wantPreviewSbx: ExamPreviewSandboxName(5),
		},
		{
			name: "(iii-a) legacy-adopted category, CategorySchema='public'",
			q: models.Question{
				ExamID:           &examID,
				CategoryID:       &catLegacyPublic,
				CategorySchema:   "public",
				CurriculumSchema: "public",
			},
			wantQuery:      "public",
			wantGradingSbx: ExamSandboxName(5),
			wantPreviewSbx: ExamPreviewSandboxName(5),
		},
		{
			name: "(iii-b) legacy-adopted category, CategorySchema='curriculum_exam_3'",
			q: models.Question{
				ExamID:           &examID,
				CategoryID:       &catLegacyCurriculum,
				CategorySchema:   "curriculum_exam_3",
				CurriculumSchema: "curriculum_exam_3",
			},
			wantQuery:      "curriculum_exam_3",
			wantGradingSbx: ExamSandboxName(5),
			wantPreviewSbx: ExamPreviewSandboxName(5),
		},
		{
			name: "(iv) owning category",
			q: models.Question{
				ExamID:                &examID,
				CategoryID:            &catOwning,
				CategorySchema:        "exam_cat_9",
				CategoryPreviewSchema: "exam_cat_9_preview",
				CurriculumSchema:      "public", // must be ignored: the category wins
			},
			wantQuery:      "exam_cat_9",
			wantGradingSbx: "sandbox_exam_cat_9",
			wantPreviewSbx: "sandbox_exam_cat_9_preview",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := querySchemaFor(c.q, c.sandboxSchema); got != c.wantQuery {
				t.Errorf("querySchemaFor = %q, want %q", got, c.wantQuery)
			}
			if got := ExamGradingSandboxFor(c.q); got != c.wantGradingSbx {
				t.Errorf("ExamGradingSandboxFor = %q, want %q", got, c.wantGradingSbx)
			}
			if got := ExamPreviewSandboxFor(c.q); got != c.wantPreviewSbx {
				t.Errorf("ExamPreviewSandboxFor = %q, want %q", got, c.wantPreviewSbx)
			}
		})
	}

	// Strongest form of the "(i)-(iii) match the pre-feature values" claim:
	// zero out CategoryID/CategorySchema/CategoryPreviewSchema on each of
	// cases[1:4] (ii, iii-a, iii-b) and prove the three resolvers give
	// IDENTICAL results with or without the category attached — i.e. a
	// migrated/legacy/no-category question is byte-identical to a question
	// that never had a category at all. Only case (iv) (owning) may legally
	// diverge, and is asserted separately above.
	for _, c := range cases[1:4] {
		t.Run(c.name+" == pre-feature (no category) value", func(t *testing.T) {
			preFeature := c.q
			preFeature.CategoryID = nil
			preFeature.CategorySchema = ""
			preFeature.CategoryPreviewSchema = ""

			if got, want := querySchemaFor(c.q, c.sandboxSchema), querySchemaFor(preFeature, c.sandboxSchema); got != want {
				t.Errorf("querySchemaFor with category = %q, pre-feature (no category) = %q — must be identical", got, want)
			}
			if got, want := ExamGradingSandboxFor(c.q), ExamGradingSandboxFor(preFeature); got != want {
				t.Errorf("ExamGradingSandboxFor with category = %q, pre-feature (no category) = %q — must be identical", got, want)
			}
			if got, want := ExamPreviewSandboxFor(c.q), ExamPreviewSandboxFor(preFeature); got != want {
				t.Errorf("ExamPreviewSandboxFor with category = %q, pre-feature (no category) = %q — must be identical", got, want)
			}
		})
	}
}

// TestQuerySchemaFor_PracticeEmptySandboxFallsBackToPublic is a light
// additional check (not one of the four mandated cases) that the practice
// (ExamID==nil) "" -> "public" default noted in querySchemaFor's own doc
// comment still holds untouched by this feature.
func TestQuerySchemaFor_PracticeEmptySandboxFallsBackToPublic(t *testing.T) {
	got := querySchemaFor(models.Question{ExamID: nil}, "")
	if got != "public" {
		t.Fatalf("querySchemaFor(practice, sandboxSchema=\"\") = %q, want \"public\"", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ExamCategoryOwnsSchema: the single discriminator ARCH-028's resolver keys
// off. True only when schemaName is EXACTLY the canonical name for that id.
// ─────────────────────────────────────────────────────────────────────────────

func TestExamCategoryOwnsSchema_Table(t *testing.T) {
	cases := []struct {
		name       string
		categoryID int
		schemaName string
		want       bool
	}{
		{"owns its own canonical schema", 5, "exam_cat_5", true},
		{"mismatched id in the schema name", 5, "exam_cat_6", false},
		{"legacy-adopted, still 'public'", 5, "public", false},
		{"legacy-adopted, still an exam curriculum schema", 5, "curriculum_exam_5", false},
		{"empty schema name (never provisioned)", 5, "", false},
		{"id 0 edge case, owns", 0, "exam_cat_0", true},
		{"prefix lookalike, does not own", 5, "exam_cat_59", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExamCategoryOwnsSchema(c.categoryID, c.schemaName); got != c.want {
				t.Errorf("ExamCategoryOwnsSchema(%d, %q) = %v, want %v", c.categoryID, c.schemaName, got, c.want)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// schemaQualifiedNameRe / validateNoSchemaQualifiedNames: the ARCH-036
// (OQ-1, user-approved) widened denylist. Exactly the six reject cases and
// two accept cases from ARCH-036's acceptance list.
// ─────────────────────────────────────────────────────────────────────────────

func TestSchemaQualifiedNameRe_ARCH036Cases(t *testing.T) {
	rejectCases := []string{
		"practice_cat_3.t",
		"practice_cat_3_preview.t",
		"curriculum_exam_7.t",
		"curriculum_exam_7_preview.t",
		"exam_cat_9.t",
		"exam_cat_9_preview.t",
	}
	for _, name := range rejectCases {
		t.Run("reject "+name, func(t *testing.T) {
			sql := "SELECT * FROM " + name
			err := validateNoSchemaQualifiedNames(sql)
			if err == nil {
				t.Fatalf("validateNoSchemaQualifiedNames(%q) = nil error, want ErrSchemaQualifiedName (ARCH-036)", sql)
			}
			if !errors.Is(err, ErrSchemaQualifiedName) {
				t.Fatalf("validateNoSchemaQualifiedNames(%q) error = %v, want it to wrap ErrSchemaQualifiedName", sql, err)
			}
		})
	}

	acceptCases := []string{
		"SELECT * FROM t",                    // unqualified name
		"SELECT * FROM my_practice_cat_3x.t", // family-prefix lookalike, not a real match
	}
	for _, sql := range acceptCases {
		t.Run("accept "+sql, func(t *testing.T) {
			if err := validateNoSchemaQualifiedNames(sql); err != nil {
				t.Fatalf("validateNoSchemaQualifiedNames(%q) = %v, want nil (must still be accepted)", sql, err)
			}
		})
	}
}
