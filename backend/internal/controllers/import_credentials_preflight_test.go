package controllers

// Tests for the owner-approved "warn, don't block" exam-active safeguard on
// the rotating credential importer:
//   - GET /api/v1/students/import-credentials/preflight (admin-only) so the
//     UI can warn BEFORE any upload happens.
//   - The import response (RotatingImportReport) carries exam_active,
//     recording ground truth at the moment the import ran regardless of
//     whether the UI's preflight check was consulted.
//
// Requires TEST_DATABASE_URL; skips cleanly via openCtrlTestRepo ->
// testsupport.SharedRepo when absent, same convention as
// import_credentials_http_test.go.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
)

// mkActiveExamForGuardTest builds a minimal exam with one captured question
// and activates it, mirroring exam_category_http_test.go's exam_active_409
// fixture: SetExaminationActive's readiness gate requires every question to
// have successfully captured its expected result, which in turn requires the
// exam's curriculum schema to actually contain the table the question's SQL
// reads from. The exam is deactivated and deleted via t.Cleanup so it can
// never leak "exam_active=true" into any other test sharing this package's
// singleton test database.
func mkActiveExamForGuardTest(t *testing.T, repo *repository.PostgresRepository, admin models.User) int {
	t.Helper()
	ctx := context.Background()
	examID, err := repo.CreateExamination(ctx, models.Examination{
		Title: "guard_active_exam", CreatedBy: admin.ID,
	})
	if err != nil {
		t.Fatalf("CreateExamination: %v", err)
	}
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

	if err := repo.ImportSchemaIntoExamCurriculum(ctx, examID,
		"CREATE TABLE guard_t (id INTEGER PRIMARY KEY);", int(admin.ID), false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCurriculum: %v", err)
	}
	eid := models.ExamID(examID)
	if _, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &eid, CorrectSQL: "SELECT * FROM guard_t", Type: "QUERY", Points: 1,
		Requirement: "r", CreatedBy: admin.ID,
	}); err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}
	if err := repo.SetExaminationActive(ctx, examID, true, nil, nil); err != nil {
		t.Fatalf("SetExaminationActive: %v", err)
	}
	t.Cleanup(func() { _ = repo.SetExaminationActive(context.Background(), examID, false, nil, nil) })
	return examID
}

// A lecturer must not reach the preflight check either -- same admin-only
// gate as the import endpoint itself, since the flag it returns is only
// useful to (and only trusted from) the actor who can actually run the import.
func TestImportCredentialsPreflight_LecturerForbidden(t *testing.T) {
	env := newImportCredentialsTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/students/import-credentials/preflight", nil)
	req.Header.Set("Authorization", "Bearer "+env.lecturerToken)
	rec := httptest.NewRecorder()
	env.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// With no exam active, the preflight must report exam_active=false.
func TestImportCredentialsPreflight_NoExamActive_ReportsFalse(t *testing.T) {
	env := newImportCredentialsTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/students/import-credentials/preflight", nil)
	req.Header.Set("Authorization", "Bearer "+env.adminToken)
	rec := httptest.NewRecorder()
	env.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		ExamActive bool `json:"exam_active"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, rec.Body.String())
	}
	if resp.ExamActive {
		t.Error("exam_active = true, want false: no exam has been activated")
	}
}

// With an exam active, the preflight must report exam_active=true.
func TestImportCredentialsPreflight_ExamActive_ReportsTrue(t *testing.T) {
	env := newImportCredentialsTestEnv(t)
	admin := models.User{ID: models.UserID(env.adminID)}
	mkActiveExamForGuardTest(t, env.repo, admin)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/students/import-credentials/preflight", nil)
	req.Header.Set("Authorization", "Bearer "+env.adminToken)
	rec := httptest.NewRecorder()
	env.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		ExamActive bool `json:"exam_active"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, rec.Body.String())
	}
	if !resp.ExamActive {
		t.Error("exam_active = false, want true: an exam was just activated")
	}
}

// The import response itself must carry exam_active matching ground truth at
// the moment the import ran -- this is the detective/audit control that
// applies even when the UI's own preflight check was bypassed. Not blocked:
// the import must still succeed (200) with an exam active, per the owner's
// "warn, don't hard-block" decision.
func TestImportCredentials_ReportCarriesExamActive(t *testing.T) {
	env := newImportCredentialsTestEnv(t)
	admin := models.User{ID: models.UserID(env.adminID)}
	mkActiveExamForGuardTest(t, env.repo, admin)

	csv := "Student Number,Student_id,SecLec,SecLab,First Name,Last Name,Email\n" +
		"N401,cred-guard-1,LEC1,LAB1,Ann,One,\n"
	body, contentType := multipartRoster(t, csv, "ROTATE")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/students/import-credentials", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+env.adminToken)
	rec := httptest.NewRecorder()
	env.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s): the import must not be hard-blocked by an active exam", rec.Code, rec.Body.String())
	}
	var report struct {
		ExamActive bool `json:"exam_active"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, rec.Body.String())
	}
	if !report.ExamActive {
		t.Error("report.exam_active = false, want true: an exam was active when this import ran")
	}
}

// Without any exam active, the import response must carry exam_active=false,
// proving the field reflects live state rather than being hardcoded true.
func TestImportCredentials_ReportExamActiveFalseWhenNoneActive(t *testing.T) {
	env := newImportCredentialsTestEnv(t)

	csv := "Student Number,Student_id,SecLec,SecLab,First Name,Last Name,Email\n" +
		"N402,cred-guard-2,LEC1,LAB1,Ann,One,\n"
	body, contentType := multipartRoster(t, csv, "ROTATE")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/students/import-credentials", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+env.adminToken)
	rec := httptest.NewRecorder()
	env.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var report struct {
		ExamActive bool `json:"exam_active"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, rec.Body.String())
	}
	if report.ExamActive {
		t.Error("report.exam_active = true, want false: no exam is active")
	}
}
