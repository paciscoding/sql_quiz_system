package controllers

// HTTP-integration tests for Task 5 (admin rotating credential import): the
// two endpoints LecturerController.ImportStudentsWithCredentials and
// LecturerController.ImportCredentialsJobStatus. Requires TEST_DATABASE_URL;
// skips cleanly via openCtrlTestRepo -> testsupport.SharedRepo when absent
// (repo convention, same as bulk_reset_http_test.go).
//
// Fixture reuse: this file leans entirely on real helpers from
// password_http_integration_test.go (openCtrlTestRepo, mkCtrlUser, login, do)
// and bulk_reset_http_test.go (bulkRecordingEmailSender, whose SendCredential/
// SendChangeNotice spy counters this file wraps with a mutex below because the
// email worker goroutine writes them from a different goroutine than the test
// reads them from -- -race would otherwise correctly flag it).

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

// multipartRoster builds a multipart body with a CSV roster and a confirm field.
func multipartRoster(t *testing.T, csv, confirm string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	part, err := w.CreateFormFile("file", "roster.csv")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte(csv)); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	if confirm != "" {
		if err := w.WriteField("confirm", confirm); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return body, w.FormDataContentType()
}

// testRosterCSV includes SecLec/SecLab columns -- the brief's original
// fixture omitted them, but services.ParseRoster (roster_parser.go) requires
// both as part of requiredHeaders, so a roster missing them fails to parse
// with a 400 before ever reaching ImportRotating. Fixed here rather than
// weakening any assertion below (see task-5-report.md for the deviation note).
const testRosterCSV = "Student Number,Student_id,SecLec,SecLab,First Name,Last Name,Email\n" +
	"N001,cred-s1,LEC1,LAB1,Ann,One,ann@example.com\n" +
	"N002,cred-s2,LEC1,LAB1,Bob,Two,\n"

// mutexRecordingEmailSender wraps bulkRecordingEmailSender's counting logic
// but under a mutex: the import-credentials email worker runs in its own
// goroutine and increments these counters concurrently with the test
// goroutine reading them via CredentialCount()/NoticeCount(), so the plain
// int fields in bulkRecordingEmailSender are not safe to reuse directly here.
type mutexRecordingEmailSender struct {
	mu                sync.Mutex
	credentialCalls   int
	changeNoticeCalls int
}

func (s *mutexRecordingEmailSender) SendCredential(toEmail, username, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credentialCalls++
	return nil
}
func (s *mutexRecordingEmailSender) SendResetCode(toEmail, username, code string) error { return nil }
func (s *mutexRecordingEmailSender) SendChangeNotice(adminEmail string, ev models.PasswordEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changeNoticeCalls++
	return nil
}
func (s *mutexRecordingEmailSender) CredentialCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.credentialCalls
}
func (s *mutexRecordingEmailSender) NoticeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changeNoticeCalls
}

// importCredentialsTestEnv bundles the gin engine, tokens and email spy that
// every scenario below needs.
type importCredentialsTestEnv struct {
	engine          *gin.Engine
	registry        *services.EmailJobRegistry
	email           *mutexRecordingEmailSender
	adminID         int
	adminToken      string
	otherAdminToken string
	lecturerToken   string
}

// buildImportCredentialsRouter mirrors the real registration: /login plus the
// two import-credentials routes inside the admin-only staff group, behind
// AuthMiddleware + RoleMiddleware(RoleAdmin) (cmd/main.go's staff group uses
// RoleMiddleware(RoleAdmin, RoleLecturer) for most routes, but the rotating
// importer is intentionally Admin-only -- see the brief).
func buildImportCredentialsRouter(auth *services.AuthService, userCtrl *UserController, lecturerCtrl *LecturerController) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.POST("/login", userCtrl.Login)
	staff := v1.Group("/")
	staff.Use(AuthMiddleware(auth))
	{
		staff.POST("/students/import-credentials", RoleMiddleware(models.RoleAdmin), lecturerCtrl.ImportStudentsWithCredentials)
		staff.GET("/students/import-credentials/:job_id/status", RoleMiddleware(models.RoleAdmin), lecturerCtrl.ImportCredentialsJobStatus)
	}
	return r
}

func newImportCredentialsTestEnv(t *testing.T) *importCredentialsTestEnv {
	t.Helper()
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	// Arm SendChangeNotice's gate BEFORE the AuthService is constructed --
	// s.adminEmail is read once at construction time from ADMIN_NOTIFY_EMAIL.
	t.Setenv("ADMIN_NOTIFY_EMAIL", "admin-notify@example.com")

	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	spy := &mutexRecordingEmailSender{}
	auth.SetEmailSender(spy)

	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	lecturerCtrl := NewLecturerController(repo, nil)
	lecturerCtrl.SetStudentImportService(services.NewStudentImportService(repo))
	lecturerCtrl.SetAuthService(auth)
	registry := services.NewEmailJobRegistry()
	lecturerCtrl.SetEmailJobRegistry(registry)

	engine := buildImportCredentialsRouter(auth, userCtrl, lecturerCtrl)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	otherAdmin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw2")
	lecturer := mkCtrlUser(t, repo, models.RoleLecturer, "lecpw")

	return &importCredentialsTestEnv{
		engine:          engine,
		registry:        registry,
		email:           spy,
		adminID:         int(admin.ID),
		adminToken:      login(t, engine, admin.Username, "adminpw"),
		otherAdminToken: login(t, engine, otherAdmin.Username, "adminpw2"),
		lecturerToken:   login(t, engine, lecturer.Username, "lecpw"),
	}
}

// waitForJobDone polls the registry directly (not over HTTP) every 10ms up to
// 5s until the job reports Done, so the test never depends on how long the
// (fake, in-memory) email send takes. Every job created by these tests is
// owned by env.adminID (the account that made the import-credentials call).
func (env *importCredentialsTestEnv) waitForJobDone(t *testing.T, jobID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if job, ok := env.registry.Get(jobID, env.adminID); ok && job.Done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %q did not finish within 5s", jobID)
}

// A lecturer must not reach the rotating importer: it mass-rotates credentials.
func TestImportCredentials_LecturerForbidden(t *testing.T) {
	env := newImportCredentialsTestEnv(t)
	body, contentType := multipartRoster(t, testRosterCSV, "ROTATE")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/students/import-credentials", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+env.lecturerToken)
	rec := httptest.NewRecorder()
	env.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestImportCredentials_RequiresConfirmPhrase(t *testing.T) {
	for _, confirm := range []string{"", "rotate", "RESET"} {
		env := newImportCredentialsTestEnv(t)
		body, contentType := multipartRoster(t, testRosterCSV, confirm)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/students/import-credentials", body)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Authorization", "Bearer "+env.adminToken)
		rec := httptest.NewRecorder()
		env.engine.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("confirm=%q: status = %d, want 400", confirm, rec.Code)
		}
		var resp map[string]string
		json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp["error"] != "confirmation_required" {
			t.Errorf("confirm=%q: error = %q, want \"confirmation_required\"", confirm, resp["error"])
		}
	}
}

// One SendCredential per row WITH an email, zero SendChangeNotice regardless of
// ADMIN_NOTIFY_EMAIL, and a job id the admin can poll.
func TestImportCredentials_EmailsOnlyRowsWithAddresses(t *testing.T) {
	env := newImportCredentialsTestEnv(t)
	body, contentType := multipartRoster(t, testRosterCSV, "ROTATE")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/students/import-credentials", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+env.adminToken)
	rec := httptest.NewRecorder()
	env.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var report struct {
		Total    int    `json:"total"`
		JobID    string `json:"job_id"`
		Accounts []struct {
			StudentIDExt string `json:"student_id_ext"`
			Password     string `json:"password"`
			EmailStatus  string `json:"email_status"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.Total != 2 || len(report.Accounts) != 2 {
		t.Fatalf("report total=%d accounts=%d, want 2/2", report.Total, len(report.Accounts))
	}
	if report.JobID == "" {
		t.Fatal("job_id is empty: the UI has nothing to poll")
	}
	if report.Accounts[0].EmailStatus != "queued" || report.Accounts[1].EmailStatus != "no_email" {
		t.Errorf("email_status = %q/%q, want queued/no_email",
			report.Accounts[0].EmailStatus, report.Accounts[1].EmailStatus)
	}
	for _, a := range report.Accounts {
		if a.Password == "" {
			t.Errorf("%s: empty password", a.StudentIDExt)
		}
	}

	env.waitForJobDone(t, report.JobID)

	if got := env.email.CredentialCount(); got != 1 {
		t.Errorf("SendCredential called %d times, want 1 (only the row with an address)", got)
	}
	if got := env.email.NoticeCount(); got != 0 {
		t.Errorf("SendChangeNotice called %d times, want 0", got)
	}
}

func TestImportCredentialsJobStatus_OwnershipAndUnknownIDs(t *testing.T) {
	env := newImportCredentialsTestEnv(t)
	body, contentType := multipartRoster(t, testRosterCSV, "ROTATE")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/students/import-credentials", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+env.adminToken)
	rec := httptest.NewRecorder()
	env.engine.ServeHTTP(rec, req)
	var report struct {
		JobID string `json:"job_id"`
	}
	json.Unmarshal(rec.Body.Bytes(), &report)

	cases := []struct {
		name  string
		id    string
		token string
		want  int
	}{
		{"owner reads own job", report.JobID, env.adminToken, http.StatusOK},
		{"other admin blocked", report.JobID, env.otherAdminToken, http.StatusNotFound},
		{"lecturer blocked", report.JobID, env.lecturerToken, http.StatusForbidden},
		{"unknown id", "deadbeef", env.adminToken, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet,
				"/api/v1/students/import-credentials/"+tc.id+"/status", nil)
			r.Header.Set("Authorization", "Bearer "+tc.token)
			w := httptest.NewRecorder()
			env.engine.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Errorf("status = %d, want %d (body: %s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
	// Also wait for the background job so the test doesn't leak a goroutine
	// still sending after the test function returns.
	if report.JobID != "" {
		env.waitForJobDone(t, report.JobID)
	}
}
