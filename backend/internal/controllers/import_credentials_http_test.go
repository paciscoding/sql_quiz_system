package controllers

// HTTP-integration tests for Task 5 (admin rotating credential import): the
// two endpoints LecturerController.ImportStudentsWithCredentials and
// LecturerController.ImportCredentialsJobStatus. Requires TEST_DATABASE_URL;
// skips cleanly via openCtrlTestRepo -> testsupport.SharedRepo when absent
// (repo convention, same as bulk_reset_http_test.go).
//
// Fixture reuse: this file leans on real helpers from
// password_http_integration_test.go (openCtrlTestRepo, mkCtrlUser, login).
// Its email spy, mutexRecordingEmailSender below, is a standalone type (not a
// wrapper around bulk_reset_http_test.go's bulkRecordingEmailSender): it needs
// its own mutex because the import-credentials email worker writes its
// counters from a different goroutine than the test reads them from --
// -race would otherwise correctly flag it.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
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
// every scenario below needs. repo is kept too, for scenarios that need to
// read back persisted state directly (e.g. password_events rows).
type importCredentialsTestEnv struct {
	engine          *gin.Engine
	registry        *services.EmailJobRegistry
	email           *mutexRecordingEmailSender
	repo            *repository.PostgresRepository
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
		repo:            repo,
		adminID:         int(admin.ID),
		adminToken:      login(t, engine, admin.Username, "adminpw"),
		otherAdminToken: login(t, engine, otherAdmin.Username, "adminpw2"),
		lecturerToken:   login(t, engine, lecturer.Username, "lecpw"),
	}
}

// panicEmailSender panics on every SendCredential call. It exists to prove
// IMPORTANT 1's fix: without a recover() in the email worker goroutine, this
// panic would crash the entire test binary instead of merely failing a job.
type panicEmailSender struct{}

func (panicEmailSender) SendCredential(toEmail, username, password string) error {
	panic("simulated SMTP client panic")
}
func (panicEmailSender) SendResetCode(toEmail, username, code string) error { return nil }
func (panicEmailSender) SendChangeNotice(adminEmail string, ev models.PasswordEvent) error {
	return nil
}

// failingEmailSender always returns an error (never panics) from
// SendCredential, so the worker's per-row failure bookkeeping (MarkFailed) can
// be exercised deterministically.
type failingEmailSender struct{}

func (failingEmailSender) SendCredential(toEmail, username, password string) error {
	return fmt.Errorf("simulated send failure")
}
func (failingEmailSender) SendResetCode(toEmail, username, code string) error { return nil }
func (failingEmailSender) SendChangeNotice(adminEmail string, ev models.PasswordEvent) error {
	return nil
}

// buildImportCredentialsEnvWithSender mirrors newImportCredentialsTestEnv but
// lets the caller supply an arbitrary services.EmailSender (panicEmailSender,
// failingEmailSender, ...) instead of the counting mutexRecordingEmailSender
// spy. The returned env's `email` field is left nil -- callers that need this
// builder don't use the counting spy.
func buildImportCredentialsEnvWithSender(t *testing.T, sender services.EmailSender) *importCredentialsTestEnv {
	t.Helper()
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	t.Setenv("ADMIN_NOTIFY_EMAIL", "admin-notify@example.com")

	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	auth.SetEmailSender(sender)

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
		repo:            repo,
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
	// Assert the setup call itself succeeded: a 400 here (e.g. a broken
	// fixture) would leave report.JobID == "", degrading every case below to
	// a request against ".../import-credentials//status" and failing with a
	// baffling "status = 404, want 200" that never names the real problem.
	if rec.Code != http.StatusOK {
		t.Fatalf("setup import status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var report struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode setup report: %v (body: %s)", err, rec.Body.String())
	}
	if report.JobID == "" {
		t.Fatal("job_id is empty: the roster has a row with an email address, so a job should have been registered")
	}

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
				t.Fatalf("status = %d, want %d (body: %s)", w.Code, tc.want, w.Body.String())
			}
			if tc.name != "owner reads own job" {
				return
			}
			// Inspect the body: asserting only the status code would let a
			// handler that serializes an empty/wrong job through unnoticed.
			var job services.EmailJob
			if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
				t.Fatalf("decode job: %v (body: %s)", err, w.Body.String())
			}
			if job.Total != 1 {
				t.Errorf("job.Total = %d, want 1 (only one roster row carries an email address)", job.Total)
			}
			if job.Sent+job.Failed > job.Total {
				t.Errorf("job.Sent(%d)+job.Failed(%d) exceeds job.Total(%d)", job.Sent, job.Failed, job.Total)
			}
		})
	}
	// Also wait for the background job so the test doesn't leak a goroutine
	// still sending after the test function returns.
	env.waitForJobDone(t, report.JobID)
}

// If no row in the roster carries an email address, the endpoint must not
// hand out a job id (there is nothing to poll for) and must not register a
// job or attempt any send.
func TestImportCredentials_NoEmailAddresses_NoJob(t *testing.T) {
	env := newImportCredentialsTestEnv(t)
	csv := "Student Number,Student_id,SecLec,SecLab,First Name,Last Name,Email\n" +
		"N201,cred-noemail-1,LEC1,LAB1,Ann,One,\n" +
		"N202,cred-noemail-2,LEC1,LAB1,Bob,Two,\n"
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
		Total int    `json:"total"`
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v (body: %s)", err, rec.Body.String())
	}
	if report.Total != 2 {
		t.Fatalf("report.Total = %d, want 2", report.Total)
	}
	if report.JobID != "" {
		t.Fatalf("job_id = %q, want \"\" (no roster row had an email address)", report.JobID)
	}
	if got := env.email.CredentialCount(); got != 0 {
		t.Errorf("SendCredential called %d times, want 0 (no row had an email address)", got)
	}
}

// The password-event source mapping: a `created` row must log
// PasswordEventCreate and an `updated` row must log PasswordEventAdminReset.
// Importing the same roster twice makes every row of the second import an
// update, exercising both branches of the a.Outcome == services.OutcomeCreated
// check in ImportStudentsWithCredentials.
func TestImportCredentials_PasswordEventSourceMapping(t *testing.T) {
	env := newImportCredentialsTestEnv(t)

	// testRosterCSV's cred-s1/cred-s2 rows are reused (and thus already
	// "updated") by other tests sharing testsupport.SharedRepo's singleton DB
	// connection, so this test needs its own never-before-seen student id AND
	// email (system_users.email is unique too, and the DB persists across
	// repeated runs against a long-lived test container) to guarantee the
	// first import here really is a create.
	unique := time.Now().UnixNano()
	studentID := fmt.Sprintf("cred-map-%d", unique)
	email := fmt.Sprintf("ann-map-%d@example.com", unique)
	roster := "Student Number,Student_id,SecLec,SecLab,First Name,Last Name,Email\n" +
		"N301," + studentID + ",LEC1,LAB1,Ann,One," + email + "\n"

	runImport := func() {
		t.Helper()
		body, contentType := multipartRoster(t, roster, "ROTATE")
		req := httptest.NewRequest(http.MethodPost, "/api/v1/students/import-credentials", body)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Authorization", "Bearer "+env.adminToken)
		rec := httptest.NewRecorder()
		env.engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("import status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		var report struct {
			JobID  string           `json:"job_id"`
			Errors []map[string]any `json:"errors"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
			t.Fatalf("decode report: %v (body: %s)", err, rec.Body.String())
		}
		if len(report.Errors) != 0 {
			t.Fatalf("import reported row errors, want none: %+v (body: %s)", report.Errors, rec.Body.String())
		}
		if report.JobID == "" {
			t.Fatalf("job_id is empty: the roster row has an email address, so a job should have been registered (body: %s)", rec.Body.String())
		}
		env.waitForJobDone(t, report.JobID)
	}

	runImport() // creates studentID
	runImport() // updates studentID

	user, err := env.repo.GetUserByUsername(context.Background(), studentID)
	if err != nil {
		t.Fatalf("GetUserByUsername(%s): %v", studentID, err)
	}
	events, err := env.repo.GetPasswordEvents(context.Background(), int(user.ID))
	if err != nil {
		t.Fatalf("GetPasswordEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 password_events rows after two imports, got %d: %+v", len(events), events)
	}
	// Newest first (repository.GetPasswordEvents orders created_at DESC):
	// index 0 is the second import's event (updated -> admin_reset), index 1
	// is the first import's event (created -> create).
	if events[0].Source != models.PasswordEventAdminReset {
		t.Errorf("second import's event source = %q, want %q (an `updated` row must log PasswordEventAdminReset)",
			events[0].Source, models.PasswordEventAdminReset)
	}
	if events[1].Source != models.PasswordEventCreate {
		t.Errorf("first import's event source = %q, want %q (a `created` row must log PasswordEventCreate)",
			events[1].Source, models.PasswordEventCreate)
	}
}

// A send failure must increment the job's Failed count and record the
// student's login id (StudentIDExt) in Failures.
func TestImportCredentials_SendFailureRecordedInJob(t *testing.T) {
	env := buildImportCredentialsEnvWithSender(t, failingEmailSender{})
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
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v (body: %s)", err, rec.Body.String())
	}
	if report.JobID == "" {
		t.Fatal("job_id is empty: the roster has a row with an email address")
	}
	env.waitForJobDone(t, report.JobID)

	job, ok := env.registry.Get(report.JobID, env.adminID)
	if !ok {
		t.Fatalf("job %q not found after completion", report.JobID)
	}
	if job.Failed != 1 {
		t.Errorf("job.Failed = %d, want 1", job.Failed)
	}
	if job.Sent != 0 {
		t.Errorf("job.Sent = %d, want 0", job.Sent)
	}
	if len(job.Failures) != 1 || job.Failures[0] != "cred-s1" {
		t.Errorf("job.Failures = %+v, want [\"cred-s1\"] (the login id of the only row with an email address)", job.Failures)
	}
}

// If the worker's send loop panics (a real SMTP client driven by
// admin-uploaded addresses -- exactly the input class not under our
// control), the worker goroutine must recover and the process must survive.
// Without that recover, an unrecovered panic in a detached goroutine crashes
// the entire process/test binary immediately, regardless of gin's Recovery
// middleware (which only protects the request goroutine) -- so this test
// simply completing at all, with the job correctly marked Done, is the
// evidence the recover() fired.
func TestImportCredentials_EmailWorkerPanicRecovered(t *testing.T) {
	env := buildImportCredentialsEnvWithSender(t, panicEmailSender{})
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
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v (body: %s)", err, rec.Body.String())
	}
	if report.JobID == "" {
		t.Fatal("job_id is empty: the roster has a row with an email address")
	}

	// If the panic is not recovered inside the worker goroutine, the test
	// process crashes right here (or the job simply never reaches Done and
	// waitForJobDone times out after 5s) instead of returning normally.
	env.waitForJobDone(t, report.JobID)

	job, ok := env.registry.Get(report.JobID, env.adminID)
	if !ok {
		t.Fatalf("job %q not found after completion", report.JobID)
	}
	if !job.Done {
		t.Fatalf("job.Done = false even though waitForJobDone reported completion")
	}
	// Reaching this line means the test binary is still executing after the
	// panicking SendCredential call ran -- the real proof recover() caught it.
	t.Log("process survived the worker's panic: recover() in the email worker goroutine caught it, and Finish still ran")
}
