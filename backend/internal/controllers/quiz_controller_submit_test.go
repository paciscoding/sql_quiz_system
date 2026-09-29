package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

// This file is the DB-free unit test for QuizController.SubmitAnswer (ARCH-105).
// It needs no TEST_DATABASE_URL: the repository, the submission queue and the
// validator are all fakes injected through the quizRepo / quizQueue /
// quizValidator seams. It pins the AGENTS.md mandate 3/4 invariant — exam DML
// goes through the queue, SELECT bypasses it — plus SubmitAnswer's error
// mapping (503 / 503 schema_busy / 504).

// fakeSubmitRepo embeds a nil quizRepo, so any repo method the test does not
// override panics loudly instead of silently returning zero values.
type fakeSubmitRepo struct {
	quizRepo
	question    models.Question
	submissions []models.Submission
}

func (r *fakeSubmitRepo) GetQuestion(ctx context.Context, id int) (models.Question, error) {
	if id != r.question.ID {
		return models.Question{}, fmt.Errorf("fakeSubmitRepo: no question %d", id)
	}
	return r.question, nil
}

func (r *fakeSubmitRepo) GetExamByID(ctx context.Context, id int) (models.Examination, error) {
	// Active with no schedule, so ExaminationEffectiveState reports ActiveNow.
	return models.Examination{IsActive: true}, nil
}

func (r *fakeSubmitRepo) IsStudentEnrolledInExam(ctx context.Context, studentID, examID int) (bool, error) {
	return true, nil
}

func (r *fakeSubmitRepo) CreateSubmission(ctx context.Context, s models.Submission) error {
	r.submissions = append(r.submissions, s)
	return nil
}

// fakeSubmitQueue records how often Run is called. With err == nil it runs fn
// inline (standing in for the single worker); otherwise it returns err
// without running fn, as the real queue does on full/shutdown.
type fakeSubmitQueue struct {
	calls int
	err   error
}

func (q *fakeSubmitQueue) Run(ctx context.Context, fn func(context.Context) error) error {
	q.calls++
	if q.err != nil {
		return q.err
	}
	return fn(ctx)
}

// fakeSubmitValidator records Validate calls and returns a configured result.
// ValidatePreview is never reached from SubmitAnswer and panics if it is.
type fakeSubmitValidator struct {
	calls  int
	result models.ValidationResult
	err    error
}

func (v *fakeSubmitValidator) Validate(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string) (models.ValidationResult, error) {
	v.calls++
	return v.result, v.err
}

func (v *fakeSubmitValidator) ValidatePreview(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string, previewSchema string) (models.ValidationResult, []map[string]interface{}, []string, int, bool, error) {
	panic("fakeSubmitValidator: ValidatePreview must not be called by SubmitAnswer")
}

func TestSubmitAnswer(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const testSubmitQuestionID = 42
	testSubmitExamID := models.ExamID(9)
	testSubmitOK := models.ValidationResult{Status: "success", Score: 1, IsCorrect: true}

	cases := []struct {
		name         string
		qType        string
		queueErr     error
		validatorErr error

		wantStatus      int
		wantQueueCalls  int
		wantValidations int
		wantSaved       int
		wantErrorField  string // expected "error" field of the JSON body, "" = not checked
	}{
		{name: "dml_goes_through_queue", qType: "DML",
			wantStatus: http.StatusOK, wantQueueCalls: 1, wantValidations: 1, wantSaved: 1},
		{name: "dml_queue_full_503", qType: "DML", queueErr: services.ErrQueueFull,
			wantStatus: http.StatusServiceUnavailable, wantQueueCalls: 1, wantValidations: 0, wantSaved: 0,
			wantErrorField: services.ErrQueueFull.Error()},
		{name: "select_bypasses_queue", qType: "QUERY",
			wantStatus: http.StatusOK, wantQueueCalls: 0, wantValidations: 1, wantSaved: 1},
		{name: "dml_queue_shutdown_503", qType: "DML", queueErr: services.ErrQueueShutdown,
			wantStatus: http.StatusServiceUnavailable, wantQueueCalls: 1, wantValidations: 0, wantSaved: 0,
			wantErrorField: services.ErrQueueShutdown.Error()},
		{name: "schema_busy_503", qType: "DML", validatorErr: repository.ErrSchemaBusy,
			wantStatus: http.StatusServiceUnavailable, wantQueueCalls: 1, wantValidations: 1, wantSaved: 0,
			wantErrorField: "schema_busy"},
		{name: "deadline_exceeded_504", qType: "DML", validatorErr: context.DeadlineExceeded,
			wantStatus: http.StatusGatewayTimeout, wantQueueCalls: 1, wantValidations: 1, wantSaved: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeSubmitRepo{question: models.Question{
				ID:     testSubmitQuestionID,
				ExamID: &testSubmitExamID,
				Type:   tc.qType,
			}}
			queue := &fakeSubmitQueue{err: tc.queueErr}
			validator := &fakeSubmitValidator{result: testSubmitOK, err: tc.validatorErr}
			ctrl := NewQuizController(validator, queue, repo, nil, nil)

			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Set("user", models.User{ID: 7, Role: models.RoleStudent})
				c.Next()
			})
			r.POST("/submit", ctrl.SubmitAnswer)

			body, _ := json.Marshal(map[string]interface{}{
				"question_number": testSubmitQuestionID,
				"sql_text":        "UPDATE t SET x = 1",
			})
			req := httptest.NewRequest(http.MethodPost, "/submit", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.wantStatus, w.Body.String())
			}
			if queue.calls != tc.wantQueueCalls {
				t.Errorf("queue.Run calls = %d, want %d", queue.calls, tc.wantQueueCalls)
			}
			if validator.calls != tc.wantValidations {
				t.Errorf("Validate calls = %d, want %d", validator.calls, tc.wantValidations)
			}
			if len(repo.submissions) != tc.wantSaved {
				t.Errorf("CreateSubmission calls = %d, want %d", len(repo.submissions), tc.wantSaved)
			}
			if tc.wantSaved > 0 {
				s := repo.submissions[0]
				if s.ExamID != testSubmitExamID || s.StudentID != 7 || s.QuestionID != testSubmitQuestionID || !s.IsCorrect {
					t.Errorf("saved submission = %+v, want exam %d student 7 question %d correct", s, testSubmitExamID, testSubmitQuestionID)
				}
			}
			if tc.wantErrorField != "" {
				var got map[string]interface{}
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatalf("decode body %q: %v", w.Body.String(), err)
				}
				if got["error"] != tc.wantErrorField {
					t.Errorf("error field = %v, want %q", got["error"], tc.wantErrorField)
				}
			}
		})
	}
}
