package repository

// Regression tests for the per-exam curriculum schema feature (TODO-PXC-BE).
//
// categories_logic_test.go already covers the base cases for
// validateNoSchemaQualifiedNames and querySchemaFor. This file adds the NEW
// coverage gaps introduced by the feature:
//
//  1. validateNoSchemaQualifiedNames: curriculum_exam_[0-9]+ is now in the
//     denylist — the existing test file does not assert this.
//  2. ExamCurriculumSchemaName naming helper (new helper, not tested anywhere).
//  3. All three schema-name helpers are mutually distinct (no collision guard).
//  4. querySchemaFor: CurriculumSchema field on models.Question is now used for
//     exam questions (the existing test expects "public" always, which was the
//     pre-feature behavior; new test covers the CurriculumSchema-populated path).

import (
	"errors"
	"strings"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// ─────────────────────────────────────────────────────────────────────────────
// 1. curriculum_exam_[0-9]+ denylist extension
// ─────────────────────────────────────────────────────────────────────────────

func TestValidateNoSchemaQualifiedNames_CurriculumExamDenylist(t *testing.T) {
	cases := []struct {
		name        string
		sql         string
		wantErr     bool
		errContains string
	}{
		// ── new denylist entries introduced by TODO-PXC-BE ───────────────────
		{
			name:        "curriculum_exam_5 is rejected",
			sql:         "SELECT * FROM curriculum_exam_5.foo",
			wantErr:     true,
			errContains: "curriculum_exam_",
		},
		{
			name:        "curriculum_exam_42 is rejected",
			sql:         "INSERT INTO curriculum_exam_42.bar VALUES (1)",
			wantErr:     true,
			errContains: "curriculum_exam_",
		},
		{
			name:        "curriculum_exam_1 in uppercase is rejected",
			sql:         "SELECT * FROM CURRICULUM_EXAM_1.tbl",
			wantErr:     true,
			errContains: "curriculum_exam_",
		},
		{
			name:        "sandbox_exam_3 is still rejected (no regression)",
			sql:         "UPDATE sandbox_exam_3.z SET col=1",
			wantErr:     true,
			errContains: "sandbox_exam_3",
		},
		{
			name:        "practice_cat_2 is still rejected (no regression)",
			sql:         "DROP TABLE practice_cat_2.w",
			wantErr:     true,
			errContains: "practice_cat_2",
		},

		// ── plain DDL/DML must still be accepted ─────────────────────────────
		{
			name:    "unqualified CREATE TABLE is accepted",
			sql:     "CREATE TABLE foo (id SERIAL PRIMARY KEY, name TEXT NOT NULL)",
			wantErr: false,
		},
		{
			name:    "unqualified INSERT is accepted",
			sql:     "INSERT INTO foo (name) VALUES ('alice')",
			wantErr: false,
		},
		{
			name:    "curriculum_exam in a single-quoted literal is not a reference",
			sql:     "INSERT INTO notes VALUES ('curriculum_exam_5 is a schema name')",
			wantErr: false,
		},
		{
			name:    "curriculum_exam in a line comment is not a reference",
			sql:     "-- curriculum_exam_5.trick\nCREATE TABLE bar (x INT)",
			wantErr: false,
		},
		{
			name:    "curriculum_exam in a block comment is not a reference",
			sql:     "/* curriculum_exam_99.trick */ CREATE TABLE baz (x INT)",
			wantErr: false,
		},
		{
			name:    "curriculum_exam in a dollar-quoted string is not a reference",
			sql:     "DO $$ BEGIN RAISE NOTICE 'curriculum_exam_5.trick'; END $$;",
			wantErr: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := validateNoSchemaQualifiedNames(tc.sql)
			if tc.wantErr {
				if err == nil {
					t.Errorf("want error containing %q, got nil", tc.errContains)
					return
				}
				if !errors.Is(err, ErrSchemaQualifiedName) {
					t.Errorf("want errors.Is(err, ErrSchemaQualifiedName), got: %v", err)
				}
				if tc.errContains != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.errContains)) {
					t.Errorf("error %q does not mention %q", err.Error(), tc.errContains)
				}
			} else {
				if err != nil {
					t.Errorf("want nil error, got: %v", err)
				}
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. ExamCurriculumSchemaName naming helper
// ─────────────────────────────────────────────────────────────────────────────

func TestExamCurriculumSchemaName(t *testing.T) {
	cases := []struct {
		id   int
		want string
	}{
		{id: 1, want: "curriculum_exam_1"},
		{id: 42, want: "curriculum_exam_42"},
		{id: 100, want: "curriculum_exam_100"},
		{id: 0, want: "curriculum_exam_0"},
		{id: 9999, want: "curriculum_exam_9999"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			got := ExamCurriculumSchemaName(tc.id)
			if got != tc.want {
				t.Errorf("ExamCurriculumSchemaName(%d) = %q, want %q", tc.id, got, tc.want)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. All three schema helpers are mutually distinct for the same ID
// ─────────────────────────────────────────────────────────────────────────────

func TestSchemaNameHelpers_MutuallyDistinct(t *testing.T) {
	for _, id := range []int{1, 5, 99} {
		curriculum := ExamCurriculumSchemaName(id)
		sandbox := ExamSandboxName(id)
		category := PracticeCategorySchemaName(id)

		if curriculum == sandbox {
			t.Errorf("id=%d: curriculum %q == sandbox %q (naming collision)", id, curriculum, sandbox)
		}
		if curriculum == category {
			t.Errorf("id=%d: curriculum %q == category %q (naming collision)", id, curriculum, category)
		}
		if sandbox == category {
			t.Errorf("id=%d: sandbox %q == category %q (naming collision)", id, sandbox, category)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. querySchemaFor: CurriculumSchema field — new coverage introduced by feature
//
// The existing TestQuerySchemaFor in categories_logic_test.go covers only the
// pre-feature behavior (CurriculumSchema always empty → always returns "public"
// for exam questions). These cases cover the post-feature behavior where
// CurriculumSchema is populated by GetQuestion.
// ─────────────────────────────────────────────────────────────────────────────

func intPtr(n int) *models.ExamID { id := models.ExamID(n); return &id }

func TestQuerySchemaFor_WithCurriculumSchema(t *testing.T) {
	cases := []struct {
		name          string
		question      models.Question
		sandboxSchema string
		want          string
		desc          string
	}{
		// ── Post-feature: exam question with CurriculumSchema set ─────────────
		{
			name: "exam question with per-exam CurriculumSchema returns that schema",
			question: models.Question{
				ExamID:           intPtr(7),
				CurriculumSchema: "curriculum_exam_7",
			},
			sandboxSchema: "sandbox_exam_7",
			want:          "curriculum_exam_7",
			desc:          "per-exam curriculum schema is used for eval when set",
		},
		{
			name: "exam question with CurriculumSchema set to public returns public",
			question: models.Question{
				ExamID:           intPtr(1),
				CurriculumSchema: "public",
			},
			sandboxSchema: "sandbox_exam_1",
			want:          "public",
			desc:          "explicit 'public' in curriculum_schema column is respected (default)",
		},

		// ── Backward-compat: exam question with empty CurriculumSchema ────────
		{
			name: "exam question with empty CurriculumSchema falls back to public",
			question: models.Question{
				ExamID:           intPtr(3),
				CurriculumSchema: "",
			},
			sandboxSchema: "sandbox_exam_3",
			want:          "public",
			desc:          "legacy exams without a per-exam import keep reading public",
		},
		{
			name: "exam question ignores sandboxSchema when CurriculumSchema is set",
			question: models.Question{
				ExamID:           intPtr(10),
				CurriculumSchema: "curriculum_exam_10",
			},
			sandboxSchema: "should_be_ignored",
			want:          "curriculum_exam_10",
			desc:          "sandbox arg is irrelevant for exam QUERY questions",
		},

		// ── Practice questions: CurriculumSchema field must be ignored ─────────
		{
			name: "practice question returns sandboxSchema even if CurriculumSchema is set",
			question: models.Question{
				ExamID:           nil,
				CurriculumSchema: "curriculum_exam_9",
			},
			sandboxSchema: "practice_cat_2",
			want:          "practice_cat_2",
			desc:          "ExamID == nil always routes to sandboxSchema, never CurriculumSchema",
		},
		{
			name: "practice question with empty sandboxSchema falls back to public",
			question: models.Question{
				ExamID:           nil,
				CurriculumSchema: "curriculum_exam_9",
			},
			sandboxSchema: "",
			want:          "public",
			desc:          "no category schema → public fallback, even if CurriculumSchema was set",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := querySchemaFor(tc.question, tc.sandboxSchema)
			if got != tc.want {
				t.Errorf("querySchemaFor() = %q, want %q — %s", got, tc.want, tc.desc)
			}
		})
	}
}
