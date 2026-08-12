package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// ─────────────────────────────────────────────────────────────────────────────
// ARCH-018 (e5): capture_error and expected_rows_affected are B-derived
// (the lecturer's correct_sql executed against the grading schema; the
// latter is a direct numeric hint about B's data) and must be stripped from
// every student-facing question payload for dual_schema questions, exactly
// like correct_sql/expected_result/expected_columns already are. Non-dual
// questions must keep shipping both fields, byte-identical to before this
// change (back-compat).
//
// These tests mirror the redaction logic actually present in
// PracticeController.ListForStudent, PracticeController.GetForStudent and
// QuizController.GetExamQuestions — matching the white-box pattern already
// used in practice_controller_test.go — since those controllers hold a
// concrete *repository.PostgresRepository and cannot be exercised without a
// live database.
// ─────────────────────────────────────────────────────────────────────────────

func intPtrForTest(v int) *int { return &v }

// dualLeakListForStudentHandler mirrors PracticeController.ListForStudent's
// redaction loop exactly, including the ARCH-018 dual-only clearing of
// CaptureError / ExpectedRowsAffected.
func dualLeakListForStudentHandler(questions []models.PracticeQuestion) gin.HandlerFunc {
	return func(c *gin.Context) {
		for i := range questions {
			questions[i].CorrectSQL = ""
			questions[i].ExpectedResult = nil
			questions[i].ExpectedColumns = nil
			if questions[i].DualSchema {
				questions[i].CaptureError = ""
				questions[i].ExpectedRowsAffected = nil
			}
		}
		c.JSON(http.StatusOK, questions)
	}
}

// dualLeakGetForStudentHandler mirrors PracticeController.GetForStudent's
// redaction logic exactly, including the ARCH-018 dual-only clearing.
func dualLeakGetForStudentHandler(question *models.PracticeQuestion) gin.HandlerFunc {
	return func(c *gin.Context) {
		question.CorrectSQL = ""
		question.ExpectedResult = nil
		question.ExpectedColumns = nil
		if question.DualSchema {
			question.CaptureError = ""
			question.ExpectedRowsAffected = nil
		}
		c.JSON(http.StatusOK, question)
	}
}

// dualLeakExamQuestionsHandler mirrors QuizController.GetExamQuestions' final
// redaction loop exactly, including the ARCH-018 dual-only clearing.
func dualLeakExamQuestionsHandler(questions []models.Question) gin.HandlerFunc {
	return func(c *gin.Context) {
		for i := range questions {
			questions[i].CorrectSQL = ""
			// Mirrors quiz_controller.go: dual QUERY questions now KEEP
			// expected_result/expected_columns (the exam view shows the
			// Expected Result table like practice does); only non-QUERY
			// questions are stripped.
			if questions[i].Type != "QUERY" {
				questions[i].ExpectedResult = nil
				questions[i].ExpectedColumns = nil
			}
			if questions[i].DualSchema {
				questions[i].CaptureError = ""
				questions[i].ExpectedRowsAffected = nil
			}
		}
		c.JSON(http.StatusOK, questions)
	}
}

// TestListForStudent_DualOmitsCaptureErrorAndRowsAffected covers acceptance
// A9: a dual practice question's list payload contains neither
// "capture_error" nor "expected_rows_affected", while a non-dual question in
// the same list still contains both, unchanged.
func TestListForStudent_DualOmitsCaptureErrorAndRowsAffected(t *testing.T) {
	questions := []models.PracticeQuestion{
		{
			ID:                   1,
			Type:                 "QUERY",
			DualSchema:           true,
			CorrectSQL:           "SELECT secret FROM vault",
			CaptureError:         "ERROR: division by zero (SQLSTATE 22012)",
			ExpectedRowsAffected: intPtrForTest(7),
			Requirement:          "List all",
			Points:               5.0,
		},
		{
			ID:                   2,
			Type:                 "DML",
			DualSchema:           false,
			CorrectSQL:           "INSERT INTO vault VALUES(99)",
			CaptureError:         "some capture diagnostic",
			ExpectedRowsAffected: intPtrForTest(3),
			Requirement:          "Insert a row",
			Points:               2.0,
		},
	}

	router := gin.New()
	router.GET("/practice/questions", dualLeakListForStudentHandler(questions))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/practice/questions", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	var got []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Expected 2 questions, got %d", len(got))
	}

	// Dual question: neither field present at all (omitempty drops them).
	dual := got[0]
	if v, ok := dual["capture_error"]; ok {
		t.Errorf("dual question: capture_error must be absent, got %v", v)
	}
	if v, ok := dual["expected_rows_affected"]; ok {
		t.Errorf("dual question: expected_rows_affected must be absent, got %v", v)
	}

	// Non-dual question: both fields present, unchanged (back-compat).
	nonDual := got[1]
	if v, ok := nonDual["capture_error"]; !ok || v != "some capture diagnostic" {
		t.Errorf("non-dual question: capture_error must be unchanged, got %v (present=%v)", v, ok)
	}
	if v, ok := nonDual["expected_rows_affected"]; !ok || v != float64(3) {
		t.Errorf("non-dual question: expected_rows_affected must be unchanged, got %v (present=%v)", v, ok)
	}
}

// TestGetForStudent_DualOmitsCaptureErrorAndRowsAffected covers the single-
// question read path (GET /practice/questions/:id).
func TestGetForStudent_DualOmitsCaptureErrorAndRowsAffected(t *testing.T) {
	t.Run("dual question strips both fields", func(t *testing.T) {
		question := &models.PracticeQuestion{
			ID:                   42,
			Type:                 "QUERY",
			DualSchema:           true,
			CorrectSQL:           "SELECT secret FROM vault WHERE id=42",
			CaptureError:         "ERROR: relation \"vault\" does not exist",
			ExpectedRowsAffected: intPtrForTest(11),
			Requirement:          "Find record",
			Points:               4.0,
		}

		router := gin.New()
		router.GET("/practice/questions/:id", dualLeakGetForStudentHandler(question))

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/practice/questions/42", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d", w.Code)
		}

		var got map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}
		if v, ok := got["capture_error"]; ok {
			t.Errorf("capture_error must be absent, got %v", v)
		}
		if v, ok := got["expected_rows_affected"]; ok {
			t.Errorf("expected_rows_affected must be absent, got %v", v)
		}
	})

	t.Run("non-dual question keeps both fields unchanged", func(t *testing.T) {
		question := &models.PracticeQuestion{
			ID:                   43,
			Type:                 "DML",
			DualSchema:           false,
			CorrectSQL:           "UPDATE vault SET x = 1 WHERE id = 43",
			CaptureError:         "capture diagnostic text",
			ExpectedRowsAffected: intPtrForTest(1),
			Requirement:          "Update a record",
			Points:               4.0,
		}

		router := gin.New()
		router.GET("/practice/questions/:id", dualLeakGetForStudentHandler(question))

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/practice/questions/43", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d", w.Code)
		}

		var got map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("Failed to parse response: %v", err)
		}
		if v, ok := got["capture_error"]; !ok || v != "capture diagnostic text" {
			t.Errorf("capture_error must be unchanged, got %v (present=%v)", v, ok)
		}
		if v, ok := got["expected_rows_affected"]; !ok || v != float64(1) {
			t.Errorf("expected_rows_affected must be unchanged, got %v (present=%v)", v, ok)
		}
	})
}

// TestGetExamQuestions_DualOmitsCaptureErrorAndRowsAffected covers acceptance
// A10: the student exam-questions payload strips capture_error and
// expected_rows_affected for a dual question while leaving a non-dual
// question in the same exam untouched, and the pre-existing dual strip of
// expected_result/expected_columns (quiz_controller.go:147-150) still holds.
func TestGetExamQuestions_DualOmitsCaptureErrorAndRowsAffected(t *testing.T) {
	rawResult, _ := json.Marshal([]map[string]interface{}{{"id": 1}})
	rawCols, _ := json.Marshal([]string{"id"})

	questions := []models.Question{
		{
			// Non-dual QUERY: expected_result/expected_columns are the
			// answer key and stay (pre-existing behaviour); capture_error /
			// expected_rows_affected must ALSO stay (back-compat, ARCH-018
			// deliberately leaves this exposure alone for non-dual).
			ID:                   1,
			Type:                 "QUERY",
			DualSchema:           false,
			CorrectSQL:           "SELECT * FROM employees",
			ExpectedResult:       rawResult,
			ExpectedColumns:      rawCols,
			CaptureError:         "diagnostic for q1",
			ExpectedRowsAffected: intPtrForTest(2),
			Requirement:          "List employees",
			Points:               5.0,
		},
		{
			// Dual DML: everything B-derived must be gone.
			ID:                   2,
			Type:                 "DML",
			DualSchema:           true,
			CorrectSQL:           "DELETE FROM employees WHERE id = 1",
			ExpectedResult:       rawResult,
			ExpectedColumns:      rawCols,
			CaptureError:         "ERROR: division by zero (SQLSTATE 22012)",
			ExpectedRowsAffected: intPtrForTest(9),
			Requirement:          "Delete an employee",
			Points:               5.0,
		},
		{
			// Non-dual DML: expected_result/expected_columns already stripped
			// today (Type != QUERY); capture_error/expected_rows_affected
			// must remain present and unchanged (back-compat).
			ID:                   3,
			Type:                 "DML",
			DualSchema:           false,
			CorrectSQL:           "UPDATE employees SET salary = 1 WHERE id = 1",
			ExpectedResult:       rawResult,
			ExpectedColumns:      rawCols,
			CaptureError:         "diagnostic for q3",
			ExpectedRowsAffected: intPtrForTest(1),
			Requirement:          "Update an employee",
			Points:               5.0,
		},
	}

	router := gin.New()
	router.GET("/exam/questions", dualLeakExamQuestionsHandler(questions))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/exam/questions", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	var got []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Expected 3 questions, got %d", len(got))
	}

	nonDualQuery := got[0]
	if v, ok := nonDualQuery["expected_result"]; !ok || v == nil {
		t.Errorf("q1 (non-dual QUERY): expected_result must remain present, got %v (present=%v)", v, ok)
	}
	if v, ok := nonDualQuery["expected_columns"]; !ok || v == nil {
		t.Errorf("q1 (non-dual QUERY): expected_columns must remain present, got %v (present=%v)", v, ok)
	}
	if v, ok := nonDualQuery["capture_error"]; !ok || v != "diagnostic for q1" {
		t.Errorf("q1 (non-dual): capture_error must be unchanged, got %v (present=%v)", v, ok)
	}
	if v, ok := nonDualQuery["expected_rows_affected"]; !ok || v != float64(2) {
		t.Errorf("q1 (non-dual): expected_rows_affected must be unchanged, got %v (present=%v)", v, ok)
	}

	dualDML := got[1]
	if v, ok := dualDML["expected_result"]; ok && v != nil {
		t.Errorf("q2 (dual): expected_result must be null/absent, got %v", v)
	}
	if v, ok := dualDML["expected_columns"]; ok && v != nil {
		t.Errorf("q2 (dual): expected_columns must be null/absent, got %v", v)
	}
	if v, ok := dualDML["capture_error"]; ok {
		t.Errorf("q2 (dual): capture_error must be absent, got %v", v)
	}
	if v, ok := dualDML["expected_rows_affected"]; ok {
		t.Errorf("q2 (dual): expected_rows_affected must be absent, got %v", v)
	}

	nonDualDML := got[2]
	if v, ok := nonDualDML["expected_result"]; ok && v != nil {
		t.Errorf("q3 (non-dual DML): expected_result must be null/absent (pre-existing DML behaviour), got %v", v)
	}
	if v, ok := nonDualDML["capture_error"]; !ok || v != "diagnostic for q3" {
		t.Errorf("q3 (non-dual): capture_error must be unchanged, got %v (present=%v)", v, ok)
	}
	if v, ok := nonDualDML["expected_rows_affected"]; !ok || v != float64(1) {
		t.Errorf("q3 (non-dual): expected_rows_affected must be unchanged, got %v (present=%v)", v, ok)
	}
}
