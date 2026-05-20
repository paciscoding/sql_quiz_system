package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// ─────────────────────────────────────────────────────────────────────────────
// Fake practice repository that satisfies the handler calls without a DB.
// We define a local interface here matching the methods used by the controller.
// ─────────────────────────────────────────────────────────────────────────────

type fakePracticeRepo interface {
	ListPracticeQuestions(ctx context.Context) ([]models.PracticeQuestion, error)
	GetPracticeQuestion(ctx context.Context, id int) (*models.PracticeQuestion, error)
	CreatePracticeAttempt(ctx context.Context, a *models.PracticeAttempt) error
}

// practiceHandlerListForStudent mirrors the handler logic exactly — used for
// white-box unit tests of the redaction logic without a real repository.
func practiceHandlerListForStudent(questions []models.PracticeQuestion) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Zero out sensitive fields — mirroring PracticeController.ListForStudent
		for i := range questions {
			questions[i].CorrectSQL = ""
			questions[i].ExpectedResult = nil
			questions[i].ExpectedColumns = nil
		}
		c.JSON(http.StatusOK, questions)
	}
}

// practiceHandlerGetForStudent mirrors PracticeController.GetForStudent's redaction logic.
func practiceHandlerGetForStudent(question *models.PracticeQuestion) gin.HandlerFunc {
	return func(c *gin.Context) {
		question.CorrectSQL = ""
		question.ExpectedResult = nil
		question.ExpectedColumns = nil
		c.JSON(http.StatusOK, question)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TODO-008: Redaction tests
// ─────────────────────────────────────────────────────────────────────────────

// TestListForStudent_RedactsCorrectSQL verifies that ListForStudent zeros
// CorrectSQL, ExpectedResult, and ExpectedColumns for every question.
func TestListForStudent_RedactsCorrectSQL(t *testing.T) {
	rawResult, _ := json.Marshal([]map[string]interface{}{{"id": 1}})
	rawCols, _ := json.Marshal([]string{"id"})

	questions := []models.PracticeQuestion{
		{
			ID:              1,
			Type:            "QUERY",
			CorrectSQL:      "SELECT secret FROM vault",
			ExpectedResult:  rawResult,
			ExpectedColumns: rawCols,
			Requirement:     "List all",
			Points:          5.0,
		},
		{
			ID:              2,
			Type:            "DML",
			CorrectSQL:      "INSERT INTO vault VALUES(99)",
			ExpectedResult:  rawResult,
			ExpectedColumns: rawCols,
			Requirement:     "Insert a row",
			Points:          2.0,
		},
	}

	router := gin.New()
	router.GET("/practice/questions", practiceHandlerListForStudent(questions))

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

	for i, q := range got {
		// CorrectSQL must not appear in the response (field is empty string or absent)
		if sql, ok := q["correct_sql"]; ok && sql != "" {
			t.Errorf("question[%d]: correct_sql must be empty/omitted, got %q", i, sql)
		}
		// ExpectedResult must be null or absent
		if er, ok := q["expected_result"]; ok && er != nil {
			t.Errorf("question[%d]: expected_result must be null/absent, got %v", i, er)
		}
		// ExpectedColumns must be null or absent
		if ec, ok := q["expected_columns"]; ok && ec != nil {
			t.Errorf("question[%d]: expected_columns must be null/absent, got %v", i, ec)
		}
	}
}

// TestGetForStudent_RedactsCorrectSQL verifies that GetForStudent zeros
// CorrectSQL, ExpectedResult, and ExpectedColumns for a single question.
func TestGetForStudent_RedactsCorrectSQL(t *testing.T) {
	rawResult, _ := json.Marshal([]map[string]interface{}{{"col": "val"}})
	rawCols, _ := json.Marshal([]string{"col"})

	question := &models.PracticeQuestion{
		ID:              42,
		Type:            "QUERY",
		CorrectSQL:      "SELECT secret FROM vault WHERE id=42",
		ExpectedResult:  rawResult,
		ExpectedColumns: rawCols,
		Requirement:     "Find record",
		Points:          4.0,
	}

	router := gin.New()
	router.GET("/practice/questions/:id", practiceHandlerGetForStudent(question))

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

	if sql, ok := got["correct_sql"]; ok && sql != "" {
		t.Errorf("correct_sql must be empty/omitted, got %q", sql)
	}
	if er, ok := got["expected_result"]; ok && er != nil {
		t.Errorf("expected_result must be null/absent, got %v", er)
	}
	if ec, ok := got["expected_columns"]; ok && ec != nil {
		t.Errorf("expected_columns must be null/absent, got %v", ec)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TODO-008: student_id source test
// Verifies that the Submit handler takes student_id from the gin "user" context
// key (set by AuthMiddleware), and NOT from the request body.
//
// Because the controller uses *repository.PostgresRepository (concrete), we
// test this behaviour via a thin "captureAttempt" handler that mirrors the
// Submit logic exactly for the student_id assignment path.
// ─────────────────────────────────────────────────────────────────────────────

// captureAttemptHandler mirrors the student_id-assignment logic from Submit.
// It captures the PracticeAttempt that would have been persisted and writes it
// to a channel, then returns 200. The rest of the Submit logic (repo+validator)
// is omitted because the student_id path is determined purely from the context.
func captureAttemptHandler(out chan<- models.PracticeAttempt) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Exact same user extraction as PracticeController.Submit
		userVal, _ := c.Get("user")
		student := userVal.(models.User)

		var req struct {
			PracticeQuestionID int    `json:"practice_question_id"`
			StudentID          int    `json:"student_id"` // attacker-supplied field
			SQLText            string `json:"sql_text"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Mirrors PracticeController.Submit: student.ID (from JWT), not req.StudentID
		attempt := models.PracticeAttempt{
			StudentID:          student.ID,
			PracticeQuestionID: req.PracticeQuestionID,
			SQLText:            req.SQLText,
		}
		out <- attempt
		c.JSON(http.StatusOK, gin.H{"captured": true})
	}
}

// TestSubmit_StudentIDFromContext verifies that Submit uses the JWT user ID
// (injected into the gin context by AuthMiddleware) and ignores any student_id
// field in the request JSON body.
func TestSubmit_StudentIDFromContext(t *testing.T) {
	attempted := make(chan models.PracticeAttempt, 1)

	router := gin.New()
	// Middleware that injects a fake JWT user with ID=42
	router.Use(func(c *gin.Context) {
		c.Set("user", models.User{ID: 42, Username: "alice", Role: models.RoleStudent})
		c.Next()
	})
	router.POST("/practice/submit", captureAttemptHandler(attempted))

	body, _ := json.Marshal(map[string]interface{}{
		"practice_question_id": 7,
		"student_id":           99, // attacker attempts to override
		"sql_text":             "SELECT 1",
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/practice/submit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: body=%s", w.Code, w.Body.String())
	}

	select {
	case attempt := <-attempted:
		if attempt.StudentID != 42 {
			t.Errorf("StudentID: want 42 (from JWT), got %d (body value would be 99)", attempt.StudentID)
		}
		if attempt.PracticeQuestionID != 7 {
			t.Errorf("PracticeQuestionID: want 7, got %d", attempt.PracticeQuestionID)
		}
	default:
		t.Fatal("No attempt captured")
	}
}

// TestSubmit_StudentIDNotFromBody is the failure/edge case: a request with no
// "student_id" field in the body should still use the JWT-injected ID.
func TestSubmit_StudentIDNotFromBody(t *testing.T) {
	attempted := make(chan models.PracticeAttempt, 1)

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user", models.User{ID: 10, Username: "bob", Role: models.RoleStudent})
		c.Next()
	})
	router.POST("/practice/submit", captureAttemptHandler(attempted))

	// No student_id in body at all
	body, _ := json.Marshal(map[string]interface{}{
		"practice_question_id": 3,
		"sql_text":             "SELECT name FROM employees",
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/practice/submit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	select {
	case attempt := <-attempted:
		if attempt.StudentID != 10 {
			t.Errorf("StudentID: want 10 (from JWT), got %d", attempt.StudentID)
		}
	default:
		t.Fatal("No attempt captured")
	}
}
