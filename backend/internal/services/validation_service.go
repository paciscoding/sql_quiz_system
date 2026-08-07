package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
)

// dualExecErrorFeedback replaces syntax_error Feedback for dual-schema questions
// before it reaches the student (ARCH-018). The original executor Feedback is
// produced by running the student's SQL against the hidden grading schema B
// (pgconn.PgError.Error() text), and its Message is a data-dependent oracle over
// B, so it must never be shown to the student; the original is still logged
// server-side so lecturers/ops can diagnose A/B structure drift.
const dualExecErrorFeedback = "Your SQL could not be executed against the grading dataset. Use Test Run to check your statement."

type DBExecutor interface {
	Query(ctx context.Context, schema, sql string) ([]map[string]interface{}, error)
	Exec(ctx context.Context, schema, sql string) error
	Evaluate(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string) (models.ValidationResult, error)
	EvaluatePreview(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string, previewSchema string) (repository.PreviewResult, error)
}

type ValidationService struct {
	executor DBExecutor
}

func NewValidationService(executor DBExecutor) *ValidationService {
	return &ValidationService{executor: executor}
}

func (s *ValidationService) Validate(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string) (result models.ValidationResult, err error) {
	// ARCH-018: dual-schema questions grade against the hidden schema B, and the
	// rows/columns captured for grading must never reach the student. A deferred
	// func runs AFTER the return expression below has already been evaluated, so
	// decideStatus (further down) has already consumed ValidationDetails/Columns
	// for grading before this scrub runs — the verdict (Status/IsCorrect/Score)
	// is therefore provably unchanged. This is total over EVERY return path of
	// Validate, including the empty-SQL and executor-error early returns, so no
	// future return path can forget to scrub.
	defer func() {
		if !question.DualSchema {
			return
		}
		if result.Status == "syntax_error" {
			// ARCH-021: the multi-statement rejection message is our own fixed
			// constant, not the executor's raw SQL error text - it carries no
			// data from any schema and is produced before the student's
			// statement ever reaches the planner, so unlike every other
			// syntax_error Feedback it is not a data-dependent oracle over the
			// hidden grading schema B. Exact-equality only (never substring or
			// pattern matching on student-controlled text), so it cannot be
			// spoofed by crafting a student SQL error that merely contains this
			// text.
			if result.Feedback == repository.MultiStatementFeedback {
				log.Printf("dual-schema multi-statement rejection for student %d, question %d", studentID, question.ID)
			} else if result.Feedback == repository.AutoSeedFailureFeedback {
				// ARCH-041: like MultiStatementFeedback above, this is our own
				// fixed constant, not executor output derived from the hidden
				// grading schema B. It names no schema, table, column, role or
				// SQLSTATE and its content never varies with any property of
				// schema B, so it is not a data-dependent oracle and is exactly
				// as safe to pass through unscrubbed. Exact-equality only, so it
				// cannot be spoofed by student-controlled error text that merely
				// contains this wording. Overwriting it with dualExecErrorFeedback
				// would blame the student's submission for what is actually a
				// question-configuration failure, contradicting ARCH-041.
				log.Printf("dual-schema auto-seed failure surfaced to student %d for question %d", studentID, question.ID)
			} else {
				log.Printf("dual-schema syntax error suppressed from student %d for question %d: %s", studentID, question.ID, result.Feedback)
				result.Feedback = dualExecErrorFeedback
			}
		}
		result.ValidationDetails = nil
		result.Columns = nil
	}()

	if studentSQL == "" {
		return models.ValidationResult{
			Status:    "syntax_error",
			Score:     0,
			Feedback:  "SQL text cannot be empty",
			IsCorrect: false,
		}, nil
	}

	result, err = s.executor.Evaluate(ctx, studentID, question, studentSQL, sandboxSchema)
	if err != nil {
		return models.ValidationResult{}, err
	}

	if result.Status == "error" {
		result.Status = "syntax_error"
		result.IsCorrect = false
		result.Score = 0
		return result, nil
	}

	var studentData []map[string]interface{}
	if err := json.Unmarshal(result.ValidationDetails, &studentData); err != nil {
		return models.ValidationResult{}, fmt.Errorf("failed to parse captured student data: %w", err)
	}

	return s.decideStatus(result, studentData, result.Columns, question), nil
}

// ValidatePreview runs the student SQL in a read-only sandbox and returns the
// three-state status together with the result rows and metadata. Unlike Validate
// it never persists a submission.
func (s *ValidationService) ValidatePreview(ctx context.Context, studentID int, q models.Question, studentSQL string, sandboxSchema string, previewSchema string) (models.ValidationResult, []map[string]interface{}, []string, int, bool, error) {
	if studentSQL == "" {
		return models.ValidationResult{
			Status:   "syntax_error",
			Feedback: "SQL text cannot be empty",
		}, []map[string]interface{}{}, []string{}, 0, false, nil
	}

	pr, err := s.executor.EvaluatePreview(ctx, studentID, q, studentSQL, sandboxSchema, previewSchema)
	if err != nil {
		return models.ValidationResult{}, nil, nil, 0, false, err
	}

	if pr.Result.Status == "error" {
		return models.ValidationResult{
			Status:   "syntax_error",
			Feedback: pr.Result.Feedback,
		}, pr.Rows, pr.Columns, pr.RowCount, pr.Truncated, nil
	}

	finalResult := s.decideStatus(pr.Result, pr.Rows, pr.Columns, q)
	return finalResult, pr.Rows, pr.Columns, pr.RowCount, pr.Truncated, nil
}

func (s *ValidationService) decideStatus(execResult models.ValidationResult, studentData []map[string]interface{}, studentColumns []string, q models.Question) models.ValidationResult {
	var teacherData []map[string]interface{}
	if err := json.Unmarshal(q.ExpectedResult, &teacherData); err != nil {
		// Should not happen for well-formed questions; return incorrect
		execResult.Status = "incorrect"
		execResult.IsCorrect = false
		execResult.Score = 0
		execResult.Feedback = "The result is wrong."
		return execResult
	}

	// DML grading (ARCH-005): the two snapshots are unordered full-table
	// captures, so ordering is never graded, the ColumnOrder gate never
	// applies, and attribute comparison is always key-aware regardless of the
	// stored strict_attrs flag. The QUERY path below (compareResults /
	// compareRows / valuesEqual) is completely untouched.
	if q.Type != "QUERY" {
		return s.decideDMLStatus(execResult, teacherData, studentData, q)
	}

	if q.StrictMode.ColumnOrder {
		var teacherCols []string
		// q.ExpectedColumns is a JSONB array of column-name strings. If it is empty
		// or fails to parse (e.g. legacy rows captured before ordered-capture existed),
		// skip the gate rather than hard-failing.
		if len(q.ExpectedColumns) > 0 && json.Unmarshal(q.ExpectedColumns, &teacherCols) == nil && len(teacherCols) > 0 {
			// ARCH-043: order is graded, casing is not.
			if !equalStringSlicesFold(teacherCols, studentColumns) {
				execResult.Status = "incorrect"
				execResult.IsCorrect = false
				execResult.Score = 0
				execResult.Feedback = "The column order does not match the expected output."
				return execResult
			}
		}
	}

	if s.compareResults(teacherData, studentData, q.StrictMode) {
		execResult.Status = "correct"
		execResult.IsCorrect = true
		execResult.Score = q.Points
		execResult.Feedback = "Correct! Full points awarded."
	} else {
		execResult.Status = "incorrect"
		execResult.IsCorrect = false
		execResult.Score = 0
		execResult.Feedback = "The result is wrong."
	}
	return execResult
}

// decideDMLStatus grades a DML question by comparing the teacher's and
// student's post-image table snapshots as a multiset of rows under key-aware
// equality (ARCH-005): no ColumnOrder gate, no ordering, no non-strict
// attribute mode — DML forces all three regardless of the stored flags.
func (s *ValidationService) decideDMLStatus(execResult models.ValidationResult, teacherData, studentData []map[string]interface{}, q models.Question) models.ValidationResult {
	missing, extra := diffRowMultiset(teacherData, studentData)
	if len(missing) == 0 && len(extra) == 0 {
		execResult.Status = "correct"
		execResult.IsCorrect = true
		execResult.Score = q.Points
		execResult.Feedback = "Correct! Full points awarded."
		return execResult
	}

	execResult.Status = "incorrect"
	execResult.IsCorrect = false
	execResult.Score = 0

	if q.DualSchema {
		// Leak-proofing (ARCH-007): the teacher's post-image snapshot lives on the
		// hidden grading schema B. A dual question's student never sees B's data,
		// so the row-level diff (which would dump B's expected rows via
		// formatDMLDiff) must never reach Feedback here.
		execResult.Feedback = "Your result does not match the expected outcome."
		return execResult
	}

	var teacherCols []string
	if len(q.ExpectedColumns) > 0 {
		_ = json.Unmarshal(q.ExpectedColumns, &teacherCols)
	}
	execResult.Feedback = formatDMLDiff(q.TargetTable, len(teacherData), len(studentData), missing, extra, teacherCols)
	return execResult
}

func (s *ValidationService) compareResults(teacher, student []map[string]interface{}, mode models.StrictMode) bool {
	if len(teacher) != len(student) {
		return false
	}

	if mode.Ordering {
		// Strict order: compare index by index
		for i := range teacher {
			if !s.compareRows(teacher[i], student[i], mode) {
				return false
			}
		}
		return true
	}

	// Non-strict order: every row in teacher must exist in student
	// This is O(N^2) but fine for small exam result sets (~90-150 rows max)
	sCopy := make([]map[string]interface{}, len(student))
	copy(sCopy, student)

	for _, tRow := range teacher {
		found := false
		for i, sRow := range sCopy {
			if s.compareRows(tRow, sRow, mode) {
				found = true
				// Remove matched row to handle duplicates correctly
				sCopy = append(sCopy[:i], sCopy[i+1:]...)
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// foldColumnName is the single definition of how a COLUMN NAME is normalised
// before grading compares it (ARCH-043). Postgres folds every unquoted
// identifier to lower case, so `SELECT fname` captures the key "fname" while a
// student who writes `SELECT fname AS "Fname"` captures "Fname" — the exact
// same column, spelled differently only because one of the two happened to
// quote the alias. Grading a pure casing difference as a wrong answer punishes
// the student for a Postgres quoting rule rather than for their SQL, so column
// names match case-insensitively everywhere below. CELL VALUES are untouched:
// valuesEqual stays case-SENSITIVE, so 'Smith' is still not 'smith'.
//
// strings.ToLower (ASCII/Unicode simple fold) is the right normaliser here
// because these are SQL identifiers, and it matches what Postgres itself does
// to unquoted names closely enough for the identifiers a quiz can produce.
func foldColumnName(name string) string {
	return strings.ToLower(name)
}

// foldRowKeys re-keys a captured row by folded column name. It reports ok=false
// when two of the row's OWN column names collide under folding — e.g. a student
// who wrote `SELECT a AS "x", b AS "X"`, which Postgres happily returns as two
// distinct columns (ARCH-043).
//
// A collision is NOT resolved by picking a winner: with two candidate columns
// folding to the same name there is no non-arbitrary way to decide which one
// the teacher's column should be compared against, and map iteration order in
// Go is randomised, so "pick one" would make the same submission grade correct
// or incorrect run to run.
//
// Instead, the comparison callers fall back to rowsEqualExactKeys for that row
// pair (ARCH-044). The original ARCH-043 implementation failed closed here, and
// that was a REGRESSION: ok=false fires on the TEACHER's own captured row too,
// which happens whenever the model answer projects case-distinct quoted names
// (`SELECT e.dno AS "Dno", d.dno AS "DNO"`) or the DML target table was created
// with quoted "Total"/"total" columns. Every student on such a question then
// scored 0 — including one submitting SQL byte-identical to the model answer —
// and the ColumnOrder gate could not catch it because equalStringSlicesFold
// compares element-wise, so ["Dno","DNO"] still equals itself.
func foldRowKeys(row map[string]interface{}) (map[string]interface{}, bool) {
	folded := make(map[string]interface{}, len(row))
	for k, v := range row {
		fk := foldColumnName(k)
		if _, dup := folded[fk]; dup {
			return nil, false
		}
		folded[fk] = v
	}
	return folded, true
}

// rowsEqualExactKeys is the PRE-ARCH-043 key-aware row comparison, preserved
// verbatim as the fallback used whenever either row's own column names collide
// under folding (ARCH-044). Keys are looked up exactly, byte for byte, with no
// normalisation at all; values still go through the case-SENSITIVE valuesEqual.
//
// Restoring exactly the shipped-before-folding semantics is what makes the
// fallback safe in both directions: it can never grade anything MORE correct
// than the deployed pre-ARCH-043 behaviour (a colliding student row still has
// to carry the teacher's exact key spellings to match, so aliasing two columns
// to "x" and "X" against a teacher {"x","y"} still misses on "y"), and it can
// never regress a question that graded correctly before folding existed.
// Folding stays a strict relaxation, applied only where it is unambiguous.
// Deterministic: no dependence on Go's randomised map iteration order.
//
// Callers must have already established that the two rows have equal arity.
func rowsEqualExactKeys(teacher, student map[string]interface{}) bool {
	for k, v := range teacher {
		sv, ok := student[k]
		if !ok || !valuesEqual(v, sv) {
			return false
		}
	}
	return true
}

// equalStringSlicesFold compares two column-name slices element by element,
// order-sensitively, folding each name (ARCH-043). It backs the ColumnOrder
// gate: the ORDER of the columns is still graded strictly, only the SPELLING of
// each name is case-insensitive.
func equalStringSlicesFold(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if foldColumnName(a[i]) != foldColumnName(b[i]) {
			return false
		}
	}
	return true
}

func (s *ValidationService) compareRows(teacher, student map[string]interface{}, mode models.StrictMode) bool {
	if len(teacher) != len(student) {
		return false
	}

	if mode.Attributes {
		// Strict attributes: keys must match, but only up to case (ARCH-043).
		// Both rows are re-keyed by folded column name first. If EITHER row's own
		// column names collide under folding, folding has no unambiguous answer
		// for this pair, so we fall back to the pre-ARCH-043 exact-key comparison
		// (ARCH-044) rather than failing closed — failing closed zeroed every
		// student on any question whose TEACHER row carried case-distinct quoted
		// names. Folding cannot change either row's key count without colliding,
		// so the arity check above stays meaningful on the folded maps.
		tFolded, tOK := foldRowKeys(teacher)
		sFolded, sOK := foldRowKeys(student)
		if !tOK || !sOK {
			return rowsEqualExactKeys(teacher, student)
		}
		for k, v := range tFolded {
			sv, ok := sFolded[k]
			if !ok || !valuesEqual(v, sv) {
				return false
			}
		}
		return true
	}

	// Non-strict attributes: only values must match, regardless of key names
	// Collect all values from both rows
	var tVals, sVals []any
	for _, v := range teacher {
		tVals = append(tVals, v)
	}
	for _, v := range student {
		sVals = append(sVals, v)
	}

	// Compare values sets
	for _, tv := range tVals {
		found := false
		for i, sv := range sVals {
			if valuesEqual(tv, sv) {
				found = true
				sVals = append(sVals[:i], sVals[i+1:]...)
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// numericEpsilon is the tolerance used when comparing two numeric cell values.
// It absorbs float-formatting drift (e.g. 1.0000000001 vs 1.0) without merging
// genuinely different values.
const numericEpsilon = 1e-9

// valuesEqual compares two SQL result cell values with type awareness. It is a
// strict superset of the previous fmt.Sprintf("%v", ...) equality for the common
// fixed-schema case:
//   - both nil            -> equal; exactly one nil -> not equal
//   - both numeric        -> compare magnitudes within numericEpsilon, so
//     int64(1) equals float64(1.0)
//   - both string/[]byte  -> compare as strings
//   - both bool           -> compare as bools
//   - otherwise/mismatched-> fall back to fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
func valuesEqual(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	if af, aok := toFloat(a); aok {
		if bf, bok := toFloat(b); bok {
			diff := af - bf
			if diff < 0 {
				diff = -diff
			}
			return diff <= numericEpsilon
		}
	}

	if as, aok := toStringValue(a); aok {
		if bs, bok := toStringValue(b); bok {
			return as == bs
		}
	}

	if ab, aok := a.(bool); aok {
		if bb, bok := b.(bool); bok {
			return ab == bb
		}
	}

	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// toFloat reports whether v is a supported numeric type and, if so, its float64
// value. json.Number is included because encoding/json can decode numbers into it.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// toStringValue reports whether v is a string or []byte and, if so, its string form.
func toStringValue(v any) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	case []byte:
		return string(s), true
	default:
		return "", false
	}
}
