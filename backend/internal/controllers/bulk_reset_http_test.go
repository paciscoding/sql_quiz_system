package controllers

// HTTP-integration tests for ARCH-031/ARCH-032/TODO-070: the admin bulk
// password-reset handler (UserController.BulkResetPassword). Requires
// TEST_DATABASE_URL; skips cleanly via openCtrlTestRepo -> testsupport.SharedRepo
// when absent, so `go test ./internal/controllers/...` stays green without a
// database (repo convention). The route itself is NOT registered in
// cmd/main.go yet (that wiring is a later wave's TODO by design) — this file
// builds its own gin.Engine (buildBulkResetRouter) mirroring the real
// admin-only group AuthMiddleware+RoleMiddleware(RoleAdmin) stack, the same
// pattern password_http_integration_test.go uses for the existing
// single-user reset route.
//
// Two techniques used below are worth calling out because they are NOT
// production code paths:
//
//  1. Isolation baseline (maxUserID): app.system_users is one shared table
//     across this whole test binary, and this package's tests run
//     sequentially (nothing here calls t.Parallel()). Every scenario below
//     captures the current max user id BEFORE creating its own fixture rows
//     and passes it as an explicit after_user_id cursor on its FIRST
//     request — a legitimate client choice ARCH-031 (2) already allows
//     ("after_user_id: optional continuation cursor; absent = start"), not a
//     hidden test hook — so each scenario only ever observes rows it created
//     itself.
//  2. poisonPasswordUpdate: to force a REAL per-student failure inside
//     AuthService.BulkResetPasswords/repository.ApplyPasswordReset (rather
//     than asserting a specific error string), this installs a throwaway
//     BEFORE UPDATE trigger on app.system_users that raises an exception
//     only when the row being updated is the one poisoned id, via
//     PostgresRepository.Exec (already exported for exactly this kind of
//     schema-scoped DDL). The trigger + function are dropped again before
//     the test returns.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/controllers/... -run BulkReset -v

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
	"golang.org/x/crypto/bcrypt"
)

// buildBulkResetRouter wires exactly /login + the admin-only bulk-reset route
// (plus /users/all, reused to prove token invalidation the same way
// password_http_integration_test.go's TestTokenInvalidation_HTTP does),
// mirroring ARCH-031 (1)'s real registration: POST
// /api/v1/users/bulk-reset-password inside the admin-only group, behind
// AuthMiddleware + RoleMiddleware(RoleAdmin). NEVER registered for
// staff/Lecturer (S1).
func buildBulkResetRouter(auth *services.AuthService, userCtrl *UserController) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.POST("/login", userCtrl.Login)
	user := v1.Group("/users")
	user.Use(AuthMiddleware(auth))
	{
		admin := user.Group("/")
		admin.Use(RoleMiddleware(models.RoleAdmin))
		{
			admin.GET("/all", userCtrl.GetAllUsers)
			admin.POST("/bulk-reset-password", userCtrl.BulkResetPassword)
		}
	}
	return r
}

// maxUserID returns the current highest internal user id across the whole
// app.system_users table (0 on an empty table) — the isolation baseline
// described in the file header.
func maxUserID(t *testing.T, repo *repository.PostgresRepository) int {
	t.Helper()
	users, err := repo.GetAllUsers(context.Background())
	if err != nil {
		t.Fatalf("GetAllUsers: %v", err)
	}
	max := 0
	for _, u := range users {
		if int(u.ID) > max {
			max = int(u.ID)
		}
	}
	return max
}

// bulkStudent is a created STUDENT fixture plus the plaintext password it was
// seeded with, for tests that need to prove login/JWT-invalidation behaviour.
type bulkStudent struct {
	models.User
	OrigPassword string
}

// mkBulkStudent creates a STUDENT row with a REAL, individually-hashed known
// password and every profile column populated, for scenarios that verify
// login/JWT-invalidation behaviour around the reset.
func mkBulkStudent(t *testing.T, repo *repository.PostgresRepository, prefix, studentNumber, plaintext string) bulkStudent {
	t.Helper()
	username := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	id, err := repo.CreateUser(context.Background(), models.User{
		Username: username, Password: string(hash), Role: models.RoleStudent,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	firstName, lastName, secLec, secLab := prefix+"_first", prefix+"_last", "LEC1", "LAB1"
	u := models.User{
		ID: models.UserID(id), Username: username, Role: models.RoleStudent,
		StudentNumber: &studentNumber, FirstName: &firstName, LastName: &lastName,
		SecLec: &secLec, SecLab: &secLab,
	}
	if err := repo.UpdateUser(context.Background(), u); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	return bulkStudent{User: u, OrigPassword: plaintext}
}

// mkBulkStudentFast creates a STUDENT row with a shared, already-hashed
// (cheap-cost) password. Used for large fixtures (hundreds of rows) where the
// scenario never actually logs in as the original account, so per-row
// hashing cost would only slow the test down for no assertion benefit.
func mkBulkStudentFast(t *testing.T, repo *repository.PostgresRepository, prefix, studentNumber, cheapHash string) int {
	t.Helper()
	username := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	id, err := repo.CreateUser(context.Background(), models.User{
		Username: username, Password: cheapHash, Role: models.RoleStudent,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	firstName, lastName, secLec, secLab := prefix+"_first", prefix+"_last", "LEC1", "LAB1"
	u := models.User{
		ID: models.UserID(id), Username: username, Role: models.RoleStudent,
		StudentNumber: &studentNumber, FirstName: &firstName, LastName: &lastName,
		SecLec: &secLec, SecLab: &secLab,
	}
	if err := repo.UpdateUser(context.Background(), u); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	return id
}

// bulkResetResponseBody decodes the ARCH-031 (7) response shape. Reusing the
// real models.BulkResetResult/BulkResetFailure types means UserID round-trips
// through publicid transparently (its UnmarshalJSON decodes the opaque
// token), so assertions below can compare directly against the internal ids
// the fixtures were created with.
type bulkResetResponseBody struct {
	Mode            string                    `json:"mode"`
	Processed       int                       `json:"processed"`
	Succeeded       int                       `json:"succeeded"`
	Failed          int                       `json:"failed"`
	Remaining       int                       `json:"remaining"`
	NextAfterUserID *string                   `json:"next_after_user_id"`
	KeyConfigured   bool                      `json:"key_configured"`
	Results         []models.BulkResetResult  `json:"results"`
	Failures        []models.BulkResetFailure `json:"failures"`
	JobID           string                    `json:"job_id"`
}

func postBulkReset(r *gin.Engine, token string, body map[string]interface{}) (*httptest.ResponseRecorder, bulkResetResponseBody) {
	w := postJSON(r, "/api/v1/users/bulk-reset-password", token, body)
	var parsed bulkResetResponseBody
	_ = json.Unmarshal(w.Body.Bytes(), &parsed)
	return w, parsed
}

// tryLoginStatus is a non-fataling variant of the shared login() helper, for
// scenarios that need to observe a FAILED login (e.g. the old password no
// longer authenticating) without aborting the test.
func tryLoginStatus(r *gin.Engine, username, password string) int {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// assertGeneratedPassword checks the ARCH-031 F1 password-policy invariants:
// exactly services.PasswordLength characters, every one drawn from
// services.PasswordAlphabet, and never equal to the student's own
// student_number.
func assertGeneratedPassword(t *testing.T, studentNumber, password string) {
	t.Helper()
	if len(password) != services.PasswordLength {
		t.Fatalf("expected a %d-character generated password, got %d (%q)", services.PasswordLength, len(password), password)
	}
	for _, ch := range password {
		if !strings.ContainsRune(services.PasswordAlphabet, ch) {
			t.Fatalf("generated password %q contains a character outside services.PasswordAlphabet", password)
		}
	}
	if password == studentNumber {
		t.Fatalf("SECURITY REGRESSION: generated password equals the student_number %q", studentNumber)
	}
}

// captureLog redirects the standard logger to a buffer for the duration of
// fn and returns everything written, so tests can assert on the ARCH-031 (8)
// / ARCH-032 S6 audit line (and its absence of any password) without
// depending on stderr.
func captureLog(fn func()) string {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)
	fn()
	return buf.String()
}

// poisonPasswordUpdate installs a throwaway trigger that makes the password
// hash update for exactly one user id fail with a real database error,
// forcing repository.ApplyPasswordReset's transaction for that ONE student to
// roll back — see the file header for why this (rather than an injected Go
// error) is the right tool here. The returned cleanup MUST be deferred.
func poisonPasswordUpdate(t *testing.T, repo *repository.PostgresRepository, poisonUserID int) (cleanup func()) {
	t.Helper()
	ctx := context.Background()
	fnName := fmt.Sprintf("poison_bulkreset_%d", poisonUserID)
	trgName := fmt.Sprintf("poison_bulkreset_%d_trg", poisonUserID)

	fnSQL := fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION app.%s() RETURNS trigger AS $$
		BEGIN
			IF NEW.id = %d THEN
				RAISE EXCEPTION 'poisoned for test (bulk reset forced per-student failure)';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`, fnName, poisonUserID)
	if err := repo.Exec(ctx, "app", fnSQL); err != nil {
		t.Fatalf("create poison function: %v", err)
	}
	trgSQL := fmt.Sprintf(`
		CREATE TRIGGER %s
		BEFORE UPDATE OF password_hash ON app.system_users
		FOR EACH ROW EXECUTE FUNCTION app.%s()`, trgName, fnName)
	if err := repo.Exec(ctx, "app", trgSQL); err != nil {
		t.Fatalf("create poison trigger: %v", err)
	}
	return func() {
		_ = repo.Exec(ctx, "app", fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON app.system_users", trgName))
		_ = repo.Exec(ctx, "app", fmt.Sprintf("DROP FUNCTION IF EXISTS app.%s()", fnName))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// F1/F2: the three selection modes, each resetting exactly its own target
// set, with the full login/JWT-invalidation effect proven per student.
// ─────────────────────────────────────────────────────────────────────────────

func TestBulkResetPassword_ThreeModes_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	r := buildBulkResetRouter(auth, userCtrl)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	t.Run("ids", func(t *testing.T) {
		a := mkBulkStudent(t, repo, "modeids_a", "MI-A", "origpw-a-123")
		b := mkBulkStudent(t, repo, "modeids_b", "MI-B", "origpw-b-123")
		c := mkBulkStudent(t, repo, "modeids_c", "MI-C", "origpw-c-123")

		oldTokenA := login(t, r, a.Username, a.OrigPassword)

		w, resp := postBulkReset(r, adminToken, map[string]interface{}{
			"mode": "ids",
			"user_ids": []string{
				publicid.Encode(publicid.TypeUser, int(a.ID)),
				publicid.Encode(publicid.TypeUser, int(b.ID)),
			},
			"confirm": "RESET",
		})
		if w.Code != http.StatusOK {
			t.Fatalf("mode=ids: want 200, got %d (%s)", w.Code, w.Body.String())
		}
		if resp.Succeeded != 2 || resp.Failed != 0 || len(resp.Results) != 2 {
			t.Fatalf("mode=ids: expected 2 succeeded/0 failed, got %+v", resp)
		}

		got := map[int]models.BulkResetResult{}
		for _, res := range resp.Results {
			got[int(res.UserID)] = res
		}
		resA, okA := got[int(a.ID)]
		resB, okB := got[int(b.ID)]
		if !okA || !okB {
			t.Fatalf("mode=ids: expected results for both a and b, got %+v", resp.Results)
		}
		if resA.Password == resB.Password {
			t.Fatalf("SECURITY REGRESSION: two students received the identical generated password")
		}
		assertGeneratedPassword(t, "MI-A", resA.Password)
		assertGeneratedPassword(t, "MI-B", resB.Password)

		// New password authenticates; old no longer does.
		_ = login(t, r, a.Username, resA.Password)
		if code := tryLoginStatus(r, a.Username, a.OrigPassword); code == http.StatusOK {
			t.Fatalf("SECURITY REGRESSION: old password still authenticates for %q", a.Username)
		}
		// Pre-reset JWT rejected (proven the same way TestTokenInvalidation_HTTP
		// does: 403 before the reset [authenticated, wrong role], 401 after
		// [middleware itself now rejects the stale token_version]).
		if w := do(r, http.MethodGet, "/api/v1/users/all", oldTokenA); w.Code != http.StatusUnauthorized {
			t.Fatalf("expected the pre-reset JWT to be rejected (401) after reset, got %d", w.Code)
		}

		// c was never targeted: untouched.
		if code := tryLoginStatus(r, c.Username, c.OrigPassword); code != http.StatusOK {
			t.Fatalf("mode=ids: expected untargeted student c to still log in with its original password, got %d", code)
		}
	})

	t.Run("all_students", func(t *testing.T) {
		baseline := maxUserID(t, repo)
		d := mkBulkStudent(t, repo, "modeall_d", "MA-D", "origpw-d-123")
		e := mkBulkStudent(t, repo, "modeall_e", "MA-E", "origpw-e-123")

		w, resp := postBulkReset(r, adminToken, map[string]interface{}{
			"mode":          "all_students",
			"confirm":       "RESET",
			"after_user_id": publicid.Encode(publicid.TypeUser, baseline),
		})
		if w.Code != http.StatusOK {
			t.Fatalf("mode=all_students: want 200, got %d (%s)", w.Code, w.Body.String())
		}
		if resp.Succeeded != 2 || resp.Failed != 0 {
			t.Fatalf("mode=all_students: expected 2 succeeded/0 failed, got %+v", resp)
		}
		got := map[int]bool{}
		for _, res := range resp.Results {
			got[int(res.UserID)] = true
		}
		if !got[int(d.ID)] || !got[int(e.ID)] {
			t.Fatalf("mode=all_students: expected both d and e reset, got results %+v", resp.Results)
		}
	})

	t.Run("exam_roster", func(t *testing.T) {
		baseline := maxUserID(t, repo)
		examID, err := repo.CreateExamination(context.Background(), models.Examination{
			Title: fmt.Sprintf("bulkreset_exam_%d", time.Now().UnixNano()), CreatedBy: models.UserID(int(admin.ID)),
		})
		if err != nil {
			t.Fatalf("CreateExamination: %v", err)
		}
		f := mkBulkStudent(t, repo, "modeexam_f", "ME-F", "origpw-f-123")
		g := mkBulkStudent(t, repo, "modeexam_g", "ME-G", "origpw-g-123")
		h := mkBulkStudent(t, repo, "modeexam_h", "ME-H", "origpw-h-123")
		if err := repo.AddStudentsToExam(context.Background(), examID, []int{int(f.ID), int(g.ID)}); err != nil {
			t.Fatalf("AddStudentsToExam: %v", err)
		}

		w, resp := postBulkReset(r, adminToken, map[string]interface{}{
			"mode":          "exam_roster",
			"exam_id":       publicid.Encode(publicid.TypeExam, examID),
			"confirm":       "RESET",
			"after_user_id": publicid.Encode(publicid.TypeUser, baseline),
		})
		if w.Code != http.StatusOK {
			t.Fatalf("mode=exam_roster: want 200, got %d (%s)", w.Code, w.Body.String())
		}
		if resp.Succeeded != 2 || resp.Failed != 0 {
			t.Fatalf("mode=exam_roster: expected 2 succeeded/0 failed, got %+v", resp)
		}
		got := map[int]bool{}
		for _, res := range resp.Results {
			got[int(res.UserID)] = true
		}
		if !got[int(f.ID)] || !got[int(g.ID)] {
			t.Fatalf("mode=exam_roster: expected f and g reset, got %+v", resp.Results)
		}
		if got[int(h.ID)] {
			t.Fatalf("SECURITY REGRESSION: mode=exam_roster reset a student (h) not enrolled in the exam")
		}
		if code := tryLoginStatus(r, h.Username, h.OrigPassword); code != http.StatusOK {
			t.Fatalf("mode=exam_roster: expected unenrolled student h to still log in with its original password, got %d", code)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// F3: audit rows written only when a key is configured, exactly one warning
// (never one per student) when it is not.
// ─────────────────────────────────────────────────────────────────────────────

func TestBulkResetPassword_KeyConfigured_WritesOneAdminResetEventEach(t *testing.T) {
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	r := buildBulkResetRouter(auth, userCtrl)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	baseline := maxUserID(t, repo)
	students := make([]bulkStudent, 3)
	for i := range students {
		students[i] = mkBulkStudent(t, repo, fmt.Sprintf("keyed_%d", i), fmt.Sprintf("KC-%d", i), "origpw-keyed")
	}

	w, resp := postBulkReset(r, adminToken, map[string]interface{}{
		"mode": "all_students", "confirm": "RESET",
		"after_user_id": publicid.Encode(publicid.TypeUser, baseline),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if !resp.KeyConfigured {
		t.Fatalf("expected key_configured=true in the response")
	}
	if resp.Succeeded != len(students) {
		t.Fatalf("expected all %d students reset, got %+v", len(students), resp)
	}
	for _, s := range students {
		events, err := repo.GetPasswordEvents(context.Background(), int(s.ID))
		if err != nil {
			t.Fatalf("GetPasswordEvents(%d): %v", s.ID, err)
		}
		if len(events) != 1 || events[0].Source != models.PasswordEventAdminReset {
			t.Fatalf("expected exactly 1 admin_reset event for user_id=%d, got %+v", s.ID, events)
		}
	}
}

func TestBulkResetPassword_NoKeyConfigured_SucceedsNoEventsOneWarning(t *testing.T) {
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, "") // explicitly unset for THIS test's AuthService
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	r := buildBulkResetRouter(auth, userCtrl)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	baseline := maxUserID(t, repo)
	const n = 5
	students := make([]int, n)
	for i := range students {
		students[i] = mkBulkStudentFast(t, repo, fmt.Sprintf("nokey_%d", i), fmt.Sprintf("NK-%d", i), "cheap-hash-placeholder")
	}

	var w *httptest.ResponseRecorder
	var resp bulkResetResponseBody
	logs := captureLog(func() {
		w, resp = postBulkReset(r, adminToken, map[string]interface{}{
			"mode": "all_students", "confirm": "RESET",
			"after_user_id": publicid.Encode(publicid.TypeUser, baseline),
		})
	})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 even with no key configured, got %d (%s)", w.Code, w.Body.String())
	}
	if resp.KeyConfigured {
		t.Fatalf("expected key_configured=false")
	}
	if resp.Succeeded != n || resp.Failed != 0 {
		t.Fatalf("expected all %d resets to still succeed, got %+v", n, resp)
	}
	for _, id := range students {
		events, err := repo.GetPasswordEvents(context.Background(), id)
		if err != nil {
			t.Fatalf("GetPasswordEvents(%d): %v", id, err)
		}
		if len(events) != 0 {
			t.Fatalf("expected zero password_events rows with no key configured, got %d for user_id=%d", len(events), id)
		}
	}
	warnCount := strings.Count(logs, "password-event logging disabled")
	if warnCount != 1 {
		t.Fatalf("expected exactly 1 warning for the whole %d-student batch (never one per student), got %d\nlogs:\n%s", n, warnCount, logs)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// F4/S4: no email of any kind, even with a recording sender + ADMIN_NOTIFY_EMAIL
// configured. This is the test that fails if BulkResetPasswords ever reused
// logPasswordEvent.
// ─────────────────────────────────────────────────────────────────────────────

type bulkRecordingEmailSender struct {
	credentialCalls   int
	changeNoticeCalls int
}

func (s *bulkRecordingEmailSender) SendCredential(toEmail, username, password string) error {
	s.credentialCalls++
	return nil
}
func (s *bulkRecordingEmailSender) SendResetCode(toEmail, username, code string) error { return nil }
func (s *bulkRecordingEmailSender) SendChangeNotice(adminEmail string, ev models.PasswordEvent) error {
	s.changeNoticeCalls++
	return nil
}

func TestBulkResetPassword_NeverEmails_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	t.Setenv("ADMIN_NOTIFY_EMAIL", "admin-notify@example.com")
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	spy := &bulkRecordingEmailSender{}
	auth.SetEmailSender(spy)
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	r := buildBulkResetRouter(auth, userCtrl)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	baseline := maxUserID(t, repo)
	const n = 25
	cheapHash, err := bcrypt.GenerateFromPassword([]byte("shared-fixture-pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		mkBulkStudentFast(t, repo, fmt.Sprintf("noemail_%d", i), fmt.Sprintf("NE-%d", i), string(cheapHash))
	}

	w, resp := postBulkReset(r, adminToken, map[string]interface{}{
		"mode": "all_students", "confirm": "RESET",
		"after_user_id": publicid.Encode(publicid.TypeUser, baseline),
		"batch_size":    n,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if resp.Succeeded != n {
		t.Fatalf("expected all %d students reset, got %+v", n, resp)
	}
	if spy.credentialCalls != 0 {
		t.Fatalf("SECURITY REGRESSION: expected 0 SendCredential calls, got %d", spy.credentialCalls)
	}
	if spy.changeNoticeCalls != 0 {
		t.Fatalf("SECURITY REGRESSION: expected 0 SendChangeNotice calls (this fails if BulkResetPasswords reused logPasswordEvent), got %d", spy.changeNoticeCalls)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// F5: partial failure — one student out of a batch fails, everyone else
// still succeeds, HTTP is still 200, and the failing student's credentials
// are provably untouched.
// ─────────────────────────────────────────────────────────────────────────────

func TestBulkResetPassword_PartialFailure_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	r := buildBulkResetRouter(auth, userCtrl)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	baseline := maxUserID(t, repo)
	const n = 20
	cheapHash, err := bcrypt.GenerateFromPassword([]byte("shared-fixture-pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int, n)
	for i := 0; i < n; i++ {
		ids[i] = mkBulkStudentFast(t, repo, fmt.Sprintf("partial_%02d", i), fmt.Sprintf("PF-%02d", i), string(cheapHash))
	}
	poisoned := ids[6] // 7th of 20, 0-indexed

	beforeUser, err := repo.GetUserByID(context.Background(), poisoned)
	if err != nil {
		t.Fatalf("GetUserByID(before): %v", err)
	}
	beforeVersion, err := repo.GetTokenVersion(context.Background(), poisoned)
	if err != nil {
		t.Fatalf("GetTokenVersion(before): %v", err)
	}

	cleanup := poisonPasswordUpdate(t, repo, poisoned)
	defer cleanup()

	w, resp := postBulkReset(r, adminToken, map[string]interface{}{
		"mode": "all_students", "confirm": "RESET",
		"after_user_id": publicid.Encode(publicid.TypeUser, baseline),
		"batch_size":    n,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 even with a partial failure, got %d (%s)", w.Code, w.Body.String())
	}
	if resp.Succeeded != n-1 || resp.Failed != 1 {
		t.Fatalf("expected succeeded=%d failed=1, got succeeded=%d failed=%d", n-1, resp.Succeeded, resp.Failed)
	}
	if len(resp.Failures) != 1 || int(resp.Failures[0].UserID) != poisoned {
		t.Fatalf("expected exactly 1 failure for the poisoned user_id=%d, got %+v", poisoned, resp.Failures)
	}
	if resp.Failures[0].Error == "" {
		t.Fatalf("expected a non-empty error message for the failed student")
	}
	for _, res := range resp.Results {
		if int(res.UserID) == poisoned {
			t.Fatalf("SECURITY REGRESSION: the poisoned student appears in results despite the forced failure")
		}
	}

	cleanup() // drop the trigger BEFORE re-reading, so the verification queries are unaffected
	afterUser, err := repo.GetUserByID(context.Background(), poisoned)
	if err != nil {
		t.Fatalf("GetUserByID(after): %v", err)
	}
	if afterUser.Password != beforeUser.Password {
		t.Fatalf("SECURITY REGRESSION: the failed student's password hash changed despite the forced failure")
	}
	afterVersion, err := repo.GetTokenVersion(context.Background(), poisoned)
	if err != nil {
		t.Fatalf("GetTokenVersion(after): %v", err)
	}
	if afterVersion != beforeVersion {
		t.Fatalf("SECURITY REGRESSION: the failed student's token_version changed despite the forced failure (before=%d after=%d)", beforeVersion, afterVersion)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// F6/S2: mode=ids naming a non-STUDENT id aborts the WHOLE request before any
// write.
// ─────────────────────────────────────────────────────────────────────────────

func TestBulkResetPassword_ModeIDs_NonStudentTarget_Returns400(t *testing.T) {
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	r := buildBulkResetRouter(auth, userCtrl)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	student := mkBulkStudent(t, repo, "nst_student", "NST-1", "origpw-nst")
	lecturer := mkCtrlUser(t, repo, models.RoleLecturer, "lecpw")

	beforeStudent, err := repo.GetUserByID(context.Background(), int(student.ID))
	if err != nil {
		t.Fatalf("GetUserByID(student, before): %v", err)
	}
	beforeLecturer, err := repo.GetUserByID(context.Background(), int(lecturer.ID))
	if err != nil {
		t.Fatalf("GetUserByID(lecturer, before): %v", err)
	}

	w, resp := postBulkReset(r, adminToken, map[string]interface{}{
		"mode": "ids",
		"user_ids": []string{
			publicid.Encode(publicid.TypeUser, int(student.ID)),
			publicid.Encode(publicid.TypeUser, int(lecturer.ID)),
		},
		"confirm": "RESET",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", w.Code, w.Body.String())
	}
	var errBody struct {
		Error   string   `json:"error"`
		UserIDs []string `json:"user_ids"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if errBody.Error != "non_student_target" {
		t.Fatalf("expected error=non_student_target, got %q", errBody.Error)
	}
	if len(errBody.UserIDs) != 1 || errBody.UserIDs[0] != publicid.Encode(publicid.TypeUser, int(lecturer.ID)) {
		t.Fatalf("expected the offending lecturer id named in the error body, got %+v", errBody.UserIDs)
	}
	_ = resp // response body is the error shape here, not bulkResetResponseBody; ignore the zero-valued parse

	afterStudent, err := repo.GetUserByID(context.Background(), int(student.ID))
	if err != nil {
		t.Fatalf("GetUserByID(student, after): %v", err)
	}
	afterLecturer, err := repo.GetUserByID(context.Background(), int(lecturer.ID))
	if err != nil {
		t.Fatalf("GetUserByID(lecturer, after): %v", err)
	}
	if afterStudent.Password != beforeStudent.Password || afterLecturer.Password != beforeLecturer.Password {
		t.Fatalf("SECURITY REGRESSION: a password changed despite the whole request being rejected")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// F7/S3: the confirmation phrase, verbatim, on EVERY request including
// continuation batches.
// ─────────────────────────────────────────────────────────────────────────────

func TestBulkResetPassword_ConfirmGate_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	r := buildBulkResetRouter(auth, userCtrl)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	cases := []struct {
		name    string
		confirm interface{}
		after   string
	}{
		{"missing", nil, ""},
		{"empty", "", ""},
		{"lowercase", "reset", ""},
		{"truncated", "RESE", ""},
		{"continuation_batch_missing", nil, publicid.Encode(publicid.TypeUser, 1)},
		{"continuation_batch_wrong", "reset", publicid.Encode(publicid.TypeUser, 1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := map[string]interface{}{"mode": "all_students"}
			if c.confirm != nil {
				body["confirm"] = c.confirm
			}
			if c.after != "" {
				body["after_user_id"] = c.after
			}
			w, _ := postBulkReset(r, adminToken, body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%s)", w.Code, w.Body.String())
			}
			var errBody struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if errBody.Error != "confirmation_required" {
				t.Fatalf("expected error=confirmation_required, got %q", errBody.Error)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// F8/OQ-4: batching — the default-100 cursor loop over 250 students pages
// 100/100/50, plus batch_size=500 clamps to 200 and batch_size=0 clamps to 1.
// ─────────────────────────────────────────────────────────────────────────────

func TestBulkResetPassword_DefaultBatching_250Students_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	r := buildBulkResetRouter(auth, userCtrl)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	baseline := maxUserID(t, repo)
	const total = 250
	cheapHash, err := bcrypt.GenerateFromPassword([]byte("shared-fixture-pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	created := map[int]bool{}
	for i := 0; i < total; i++ {
		id := mkBulkStudentFast(t, repo, fmt.Sprintf("page250_%03d", i), fmt.Sprintf("PG-%03d", i), string(cheapHash))
		created[id] = true
	}

	var pageSizes []int
	seen := map[int]bool{}
	after := publicid.Encode(publicid.TypeUser, baseline)
	for page := 0; page < 10; page++ {
		w, resp := postBulkReset(r, adminToken, map[string]interface{}{
			"mode": "all_students", "confirm": "RESET", "after_user_id": after,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("page %d: want 200, got %d (%s)", page, w.Code, w.Body.String())
		}
		pageSizes = append(pageSizes, resp.Processed)
		for _, res := range resp.Results {
			if seen[int(res.UserID)] {
				t.Fatalf("duplicate user_id=%d returned across pages", res.UserID)
			}
			seen[int(res.UserID)] = true
		}
		if resp.NextAfterUserID == nil {
			break
		}
		after = *resp.NextAfterUserID
	}

	want := []int{100, 100, 50}
	if len(pageSizes) != len(want) {
		t.Fatalf("expected page sizes %v, got %v", want, pageSizes)
	}
	for i := range want {
		if pageSizes[i] != want[i] {
			t.Fatalf("expected page sizes %v, got %v", want, pageSizes)
		}
	}
	if len(seen) != total {
		t.Fatalf("expected %d unique reset ids across the union of pages, got %d", total, len(seen))
	}
	for id := range created {
		if !seen[id] {
			t.Fatalf("created id %d missing from the paged union", id)
		}
	}
}

func TestBulkResetPassword_BatchSizeClamping_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	r := buildBulkResetRouter(auth, userCtrl)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	baseline := maxUserID(t, repo)
	const total = 201 // > 200, so a clamp-to-200 result is distinguishable from "all available"
	cheapHash, err := bcrypt.GenerateFromPassword([]byte("shared-fixture-pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < total; i++ {
		mkBulkStudentFast(t, repo, fmt.Sprintf("clamp_%03d", i), fmt.Sprintf("CL-%03d", i), string(cheapHash))
	}

	// batch_size=500 must clamp to the hard max of 200, not process all 201.
	w, resp := postBulkReset(r, adminToken, map[string]interface{}{
		"mode": "all_students", "confirm": "RESET",
		"after_user_id": publicid.Encode(publicid.TypeUser, baseline),
		"batch_size":    500,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if resp.Processed != 200 {
		t.Fatalf("expected batch_size=500 to clamp to 200, got processed=%d", resp.Processed)
	}
	if resp.Remaining != total-200 {
		t.Fatalf("expected remaining=%d, got %d", total-200, resp.Remaining)
	}
	if resp.NextAfterUserID == nil {
		t.Fatalf("expected a non-null next_after_user_id with 1 student still remaining")
	}

	// batch_size=0 must clamp to 1, not to 0 (which would mean LIMIT 0 → zero
	// rows processed and remaining left completely unchanged).
	w2, resp2 := postBulkReset(r, adminToken, map[string]interface{}{
		"mode": "all_students", "confirm": "RESET",
		"after_user_id": *resp.NextAfterUserID,
		"batch_size":    0,
	})
	if w2.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w2.Code, w2.Body.String())
	}
	if resp2.Processed != 1 {
		t.Fatalf("expected batch_size=0 to clamp to 1, got processed=%d", resp2.Processed)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// F9/S1: role gating — LECTURER/STUDENT get 403, unauthenticated gets 401.
// ─────────────────────────────────────────────────────────────────────────────

func TestBulkResetPassword_RoleGate_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	r := buildBulkResetRouter(auth, userCtrl)

	lecturer := mkCtrlUser(t, repo, models.RoleLecturer, "lecpw")
	student := mkBulkStudent(t, repo, "rolegate_student", "RG-1", "origpw-rolegate")

	lecToken := login(t, r, lecturer.Username, "lecpw")
	studentToken := login(t, r, student.Username, student.OrigPassword)

	body := map[string]interface{}{"mode": "all_students", "confirm": "RESET"}

	if w, _ := postBulkReset(r, lecToken, body); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for LECTURER, got %d (%s)", w.Code, w.Body.String())
	}
	if w, _ := postBulkReset(r, studentToken, body); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for STUDENT, got %d (%s)", w.Code, w.Body.String())
	}
	if w, _ := postBulkReset(r, "", body); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated, got %d (%s)", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// F10/S5/S6/S7: response headers and the audit log — no-store, exactly one
// audit line, and NEVER a returned password anywhere in the captured log.
// ─────────────────────────────────────────────────────────────────────────────

func TestBulkResetPassword_HeadersAndAuditLog_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	r := buildBulkResetRouter(auth, userCtrl)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	baseline := maxUserID(t, repo)
	students := make([]bulkStudent, 4)
	for i := range students {
		students[i] = mkBulkStudent(t, repo, fmt.Sprintf("audit_%d", i), fmt.Sprintf("AU-%d", i), "origpw-audit")
	}

	var w *httptest.ResponseRecorder
	var resp bulkResetResponseBody
	logs := captureLog(func() {
		w, resp = postBulkReset(r, adminToken, map[string]interface{}{
			"mode": "all_students", "confirm": "RESET",
			"after_user_id": publicid.Encode(publicid.TypeUser, baseline),
		})
	})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("expected Cache-Control: no-store, got %q", got)
	}

	if strings.Count(logs, "audit: bulk_password_reset") != 1 {
		t.Fatalf("expected exactly 1 audit line, got log:\n%s", logs)
	}
	if !strings.Contains(logs, fmt.Sprintf("actor=%q", admin.Username)) {
		t.Fatalf("expected the audit line to name the actor, got:\n%s", logs)
	}
	if !strings.Contains(logs, fmt.Sprintf("succeeded=%d", len(students))) {
		t.Fatalf("expected the audit line to carry the succeeded count, got:\n%s", logs)
	}
	for _, res := range resp.Results {
		if strings.Contains(logs, res.Password) {
			t.Fatalf("SECURITY REGRESSION: a returned password appears in the captured server log")
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Regression-tester (step [7]) gap coverage: two untested interactions the
// brief flagged as "worth probing" — duplicate ids within mode=ids, and
// mode=exam_roster against an exam with zero enrolled students.
// ─────────────────────────────────────────────────────────────────────────────

// TestBulkResetPassword_DuplicateUserID_DedupedToOne_HTTP proves that naming
// the SAME student's opaque token more than once in mode=ids' user_ids array
// resolves to exactly ONE reset, not one per occurrence. ResolveBulkResetTargets
// (postgres_password_bulk.go) matches with `u.id = ANY($2::int[])` against
// app.system_users — a plain WHERE predicate, not a JOIN against unnest — so a
// repeated id in the array cannot multiply the matched row; this test is the
// end-to-end proof of that from the real HTTP response shape (processed/
// succeeded counts and the results array), not just the SQL semantics.
func TestBulkResetPassword_DuplicateUserID_DedupedToOne_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	r := buildBulkResetRouter(auth, userCtrl)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	s := mkBulkStudent(t, repo, "dupids_s", "DUP-S", "origpw-dup-123")
	token := publicid.Encode(publicid.TypeUser, int(s.ID))

	w, resp := postBulkReset(r, adminToken, map[string]interface{}{
		"mode":     "ids",
		"user_ids": []string{token, token, token},
		"confirm":  "RESET",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("duplicate ids: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if resp.Processed != 1 || resp.Succeeded != 1 || resp.Failed != 0 {
		t.Fatalf("expected a user_ids list naming the SAME student 3 times to resolve to exactly ONE target (processed=succeeded=1), got %+v", resp)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected exactly ONE result row for the triplicated id, got %d: %+v", len(resp.Results), resp.Results)
	}
	if int(resp.Results[0].UserID) != int(s.ID) {
		t.Fatalf("expected the single result to be for the duplicated student, got %+v", resp.Results[0])
	}

	// The student's password was changed exactly once and is a single,
	// coherent generated password (not e.g. three re-hashes racing each
	// other and leaving an unpredictable final hash) — proven by
	// successfully logging in with the ONE password the response reported.
	_ = login(t, r, s.Username, resp.Results[0].Password)
}

// TestBulkResetPassword_ExamRoster_EmptyRoster_HTTP proves mode=exam_roster
// against an exam with ZERO enrolled students is a well-formed empty success
// (200, all counts zero, empty results/failures, next_after_user_id=null) —
// never an error and never a nil-slice-index panic in the "no targets"
// branch (user_controller.go's `remaining > 0 && len(targets) > 0` guard).
func TestBulkResetPassword_ExamRoster_EmptyRoster_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	r := buildBulkResetRouter(auth, userCtrl)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	examID, err := repo.CreateExamination(context.Background(), models.Examination{
		Title: fmt.Sprintf("bulkreset_emptyroster_%d", time.Now().UnixNano()), CreatedBy: models.UserID(int(admin.ID)),
	})
	if err != nil {
		t.Fatalf("CreateExamination: %v", err)
	}
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })
	// Deliberately zero enrolled students.

	w, resp := postBulkReset(r, adminToken, map[string]interface{}{
		"mode":    "exam_roster",
		"exam_id": publicid.Encode(publicid.TypeExam, examID),
		"confirm": "RESET",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("exam_roster with an empty roster: want 200 (not an error), got %d (%s)", w.Code, w.Body.String())
	}
	if resp.Processed != 0 || resp.Succeeded != 0 || resp.Failed != 0 || resp.Remaining != 0 {
		t.Fatalf("expected an exam with zero enrolled students to resolve to zero targets with no error, got %+v", resp)
	}
	if len(resp.Results) != 0 || len(resp.Failures) != 0 {
		t.Fatalf("expected empty results/failures arrays, got results=%+v failures=%+v", resp.Results, resp.Failures)
	}
	if resp.NextAfterUserID != nil {
		t.Fatalf("expected next_after_user_id=null for a zero-target request, got %v", *resp.NextAfterUserID)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Optional credential email (opt-in). The default path is pinned by
// TestBulkResetPassword_NeverEmails_HTTP above, which sends no send_email at
// all: these cover what happens once an admin asks for delivery.
// ─────────────────────────────────────────────────────────────────────────────

// bulkResetEmailEnv builds the router with a counting email spy and a live job
// registry, and returns everything a delivery scenario needs.
type bulkResetEmailEnv struct {
	engine   *gin.Engine
	spy      *mutexRecordingEmailSender
	registry *services.EmailJobRegistry
	repo     *repository.PostgresRepository
	token    string
	adminID  int
	baseline int
}

func newBulkResetEmailEnv(t *testing.T, sender services.EmailSender) *bulkResetEmailEnv {
	t.Helper()
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	// Armed BEFORE construction: s.adminEmail is read once in NewAuthService,
	// so a later Setenv would leave the SendChangeNotice assertion vacuous.
	t.Setenv("ADMIN_NOTIFY_EMAIL", "admin-notify@example.com")

	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	spy, _ := sender.(*mutexRecordingEmailSender)
	if sender != nil {
		auth.SetEmailSender(sender)
	}

	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	registry := services.NewEmailJobRegistry()
	userCtrl.SetEmailJobRegistry(registry)
	engine := buildBulkResetRouter(auth, userCtrl)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	return &bulkResetEmailEnv{
		engine:   engine,
		spy:      spy,
		registry: registry,
		repo:     repo,
		token:    login(t, engine, admin.Username, "adminpw"),
		adminID:  int(admin.ID),
		baseline: maxUserID(t, repo),
	}
}

// waitForBulkJob blocks until the background send finishes, so the assertions
// below observe a settled job rather than racing the worker.
func (e *bulkResetEmailEnv) waitForBulkJob(t *testing.T, jobID string) services.EmailJob {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		job, ok := e.registry.Get(jobID, e.adminID)
		if ok && job.Done {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("email job %q never finished", jobID)
	return services.EmailJob{}
}

func TestBulkResetPassword_SendEmailTrue_EmailsAndReportsStatus(t *testing.T) {
	env := newBulkResetEmailEnv(t, &mutexRecordingEmailSender{})
	cheapHash, err := bcrypt.GenerateFromPassword([]byte("shared-fixture-pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	// One student WITH an address, one WITHOUT: the two must be reported and
	// treated differently.
	mkBulkStudentWithEmail(t, env.repo, "mailed", "ME-1", "fixture-pw")
	mkBulkStudentFast(t, env.repo, "unmailed", "UM-1", string(cheapHash))

	w, resp := postBulkReset(env.engine, env.token, map[string]interface{}{
		"mode": "all_students", "confirm": "RESET",
		"after_user_id": publicid.Encode(publicid.TypeUser, env.baseline),
		"batch_size":    10,
		"send_email":    true,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if resp.Succeeded != 2 {
		t.Fatalf("expected 2 resets, got %+v", resp)
	}
	if resp.JobID == "" {
		t.Fatal("job_id is empty: the UI has no way to show send progress")
	}

	var queued, noEmail int
	for _, r := range resp.Results {
		switch r.EmailStatus {
		case services.EmailStatusQueued:
			queued++
			if r.Email == "" {
				t.Errorf("%s: queued with no address", r.Username)
			}
		case services.EmailStatusNoEmail:
			noEmail++
		default:
			t.Errorf("%s: unexpected email_status %q", r.Username, r.EmailStatus)
		}
		// Delivery outcome must never suppress the password: the response is
		// still the only copy.
		if r.Password == "" {
			t.Errorf("%s: empty password", r.Username)
		}
	}
	if queued != 1 || noEmail != 1 {
		t.Errorf("statuses = %d queued / %d no_email, want 1/1", queued, noEmail)
	}

	job := env.waitForBulkJob(t, resp.JobID)
	if job.Total != 1 || job.Sent != 1 || job.Failed != 0 {
		t.Errorf("job = %+v, want total/sent/failed 1/1/0 (only the row with an address)", job)
	}
	if got := env.spy.CredentialCount(); got != 1 {
		t.Errorf("SendCredential called %d times, want 1", got)
	}
	// The single-user reset path emails ADMIN_NOTIFY_EMAIL per event; the bulk
	// path must not, or one reset of 200 students becomes 200 admin emails.
	if got := env.spy.NoticeCount(); got != 0 {
		t.Errorf("SendChangeNotice called %d times, want 0", got)
	}
}

// With no real sender wired in (EMAIL_SENDER_ADDRESS unset in a deployment,
// reproduced here by leaving AuthService's NoopEmailSender in place), an opt-in
// send must report "not_configured" rather than claiming delivery:
// NoopEmailSender.SendCredential returns nil, so counting it as success would
// tell the admin every student was emailed when none were.
func TestBulkResetPassword_SendEmailTrue_NotConfigured_ClaimsNothing(t *testing.T) {
	env := newBulkResetEmailEnv(t, nil)
	mkBulkStudentWithEmail(t, env.repo, "noconf", "NC-1", "fixture-pw")

	w, resp := postBulkReset(env.engine, env.token, map[string]interface{}{
		"mode": "all_students", "confirm": "RESET",
		"after_user_id": publicid.Encode(publicid.TypeUser, env.baseline),
		"batch_size":    10,
		"send_email":    true,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if resp.JobID != "" {
		t.Errorf("job_id = %q, want empty: nothing can be sent, so there is no job", resp.JobID)
	}
	if len(resp.Results) != 1 || resp.Results[0].EmailStatus != services.EmailStatusNotConfigured {
		t.Fatalf("results = %+v, want one row with email_status=not_configured", resp.Results)
	}
}

// The default path must stay non-emailing AND must not grow an email_status:
// an empty field is what tells the UI this run never asked to send, so it can
// leave the column off instead of showing a row of blanks.
func TestBulkResetPassword_DefaultOmitsEmailStatusAndJob(t *testing.T) {
	env := newBulkResetEmailEnv(t, &mutexRecordingEmailSender{})
	mkBulkStudentWithEmail(t, env.repo, "default", "DF-1", "fixture-pw")

	w, resp := postBulkReset(env.engine, env.token, map[string]interface{}{
		"mode": "all_students", "confirm": "RESET",
		"after_user_id": publicid.Encode(publicid.TypeUser, env.baseline),
		"batch_size":    10,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if resp.JobID != "" {
		t.Errorf("job_id = %q, want empty on a non-emailing reset", resp.JobID)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("want 1 result, got %d", len(resp.Results))
	}
	if resp.Results[0].EmailStatus != "" {
		t.Errorf("email_status = %q, want empty on a run that never asked to email", resp.Results[0].EmailStatus)
	}
	// A student WITH an address on file still gets no mail by default.
	if got := env.spy.CredentialCount(); got != 0 {
		t.Errorf("SendCredential called %d times, want 0", got)
	}
}
