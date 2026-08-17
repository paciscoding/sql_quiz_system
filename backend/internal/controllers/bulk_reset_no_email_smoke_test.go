package controllers

// Smoke test for ARCH-031/032 S4, as NARROWED on 2026-08-17: by default the
// admin bulk password-reset endpoint must deliver the newly generated password
// to nobody — not the student, not the configured admin-notify address, not
// anyone — so that on the default path the plaintext still travels on exactly
// one channel: the HTTP response body shown to the admin who triggered it.
//
// S4's original "NEVER, under any circumstances" was relaxed by owner decision
// to add an opt-in credential email (request field send_email, default false).
// Every request below deliberately OMITS send_email, so this file continues to
// pin the default path — which is what an admin who has not changed their
// habits actually gets. The opt-in path has its own coverage in
// bulk_reset_http_test.go (TestBulkResetPassword_SendEmailTrue_*), including
// that it STILL never fans out an admin change-notice per event.
//
// The test is deliberately hostile to the invariant:
//
//   - Every student fixture HAS an email address on file, so any accidental
//     SendCredential/SendChangeNotice call would have a real recipient.
//   - ADMIN_NOTIFY_EMAIL is set BEFORE NewAuthService, so the admin
//     change-notice path (AuthService.logPasswordEvent -> SendChangeNotice)
//     is armed. If BulkResetPasswords ever regresses to calling
//     logPasswordEvent, this test fails.
//   - PASSWORD_LOG_KEY is configured, so the encrypted audit-event insert
//     really runs (the enc != nil branch of ApplyPasswordReset).
//   - All three selection modes (ids / all_students / exam_roster) are
//     exercised in one run.
//   - A positive control at the end proves the recorder is actually wired:
//     the SINGLE-user reset route with email_password=true does send exactly
//     one email. Without that control, a mis-wired sender would make the
//     "zero emails" assertions vacuously true.
//
// Requires TEST_DATABASE_URL; skips cleanly via openCtrlTestRepo otherwise.
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/controllers/... \
//        -run TestBulkReset_NeverEmailsPassword -v

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
	"golang.org/x/crypto/bcrypt"
)

// recordedEmail is one attempted delivery, whatever its kind.
type recordedEmail struct {
	Kind string // "credential" | "reset_code" | "change_notice"
	To   string
	Body string // password / code / event summary — asserted against, never logged in prod
}

// recordingEmailSender records every delivery attempt instead of sending it.
// It never fails a send, so a regression shows up as a RECORDED email rather
// than as an error the production code might swallow.
type recordingEmailSender struct {
	mu   sync.Mutex
	sent []recordedEmail
}

func (s *recordingEmailSender) record(kind, to, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, recordedEmail{Kind: kind, To: to, Body: body})
	return nil
}

func (s *recordingEmailSender) SendCredential(toEmail, username, password string) error {
	return s.record("credential", toEmail, password)
}

func (s *recordingEmailSender) SendResetCode(toEmail, username, code string) error {
	return s.record("reset_code", toEmail, code)
}

func (s *recordingEmailSender) SendChangeNotice(adminEmail string, ev models.PasswordEvent) error {
	return s.record("change_notice", adminEmail, ev.Source)
}

func (s *recordingEmailSender) snapshot() []recordedEmail {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recordedEmail, len(s.sent))
	copy(out, s.sent)
	return out
}

// mkBulkStudentWithEmail creates a STUDENT fixture that HAS an email address
// on file (uniquified against the account-email unique index, the same way
// mkCtrlUserWithEmail does) plus the profile columns the bulk-reset response
// echoes back.
func mkBulkStudentWithEmail(t *testing.T, repo *repository.PostgresRepository, prefix, studentNumber, plaintext string) bulkStudent {
	t.Helper()
	nano := time.Now().UnixNano()
	username := fmt.Sprintf("%s_%d", prefix, nano)
	email := fmt.Sprintf("%s+%d@students.example.test", prefix, nano)
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	id, err := repo.CreateUser(context.Background(), models.User{
		Username: username, Password: string(hash), Role: models.RoleStudent, Email: &email,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	firstName, lastName, secLec, secLab := prefix+"_first", prefix+"_last", "LEC1", "LAB1"
	u := models.User{
		ID: models.UserID(id), Username: username, Role: models.RoleStudent, Email: &email,
		StudentNumber: &studentNumber, FirstName: &firstName, LastName: &lastName,
		SecLec: &secLec, SecLab: &secLab,
	}
	// UpdateUser does not touch the email column, so the address created above
	// survives this profile fill-in.
	if err := repo.UpdateUser(context.Background(), u); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	return bulkStudent{User: u, OrigPassword: plaintext}
}

// buildBulkResetAndSingleResetRouter is buildBulkResetRouter plus the
// single-user reset route, needed only by the positive control at the end.
func buildBulkResetAndSingleResetRouter(auth *services.AuthService, userCtrl *UserController) *gin.Engine {
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
			admin.POST("/:id/reset-password", userCtrl.ResetPassword)
		}
	}
	return r
}

func TestBulkReset_NeverEmailsPassword_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	// Arm the admin change-notice path BEFORE the service is constructed:
	// NewAuthService snapshots ADMIN_NOTIFY_EMAIL at construction time.
	t.Setenv("ADMIN_NOTIFY_EMAIL", "admin-notify@example.test")

	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	sender := &recordingEmailSender{}
	auth.SetEmailSender(sender)
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	r := buildBulkResetAndSingleResetRouter(auth, userCtrl)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	adminToken := login(t, r, admin.Username, "adminpw")

	// Everything below shares one recorder; each phase asserts the running
	// total is still zero, so the first offending mode is named precisely.
	assertNoEmails := func(t *testing.T, phase string) {
		t.Helper()
		if got := sender.snapshot(); len(got) != 0 {
			t.Fatalf("SECURITY REGRESSION: bulk reset (%s) sent %d email(s): %+v", phase, len(got), got)
		}
	}

	var idsPasswords []string

	t.Run("mode=ids sends nothing", func(t *testing.T) {
		a := mkBulkStudentWithEmail(t, repo, "noemail_ids_a", "NE-A", "origpw-a-123")
		b := mkBulkStudentWithEmail(t, repo, "noemail_ids_b", "NE-B", "origpw-b-123")

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
		if resp.Succeeded != 2 || resp.Failed != 0 {
			t.Fatalf("mode=ids: expected 2 succeeded/0 failed, got %+v", resp)
		}
		if !resp.KeyConfigured {
			t.Fatalf("expected key_configured=true so the encrypted audit-event insert really ran")
		}
		for _, res := range resp.Results {
			if res.Password == "" {
				t.Fatalf("expected the plaintext password in the HTTP response (its only channel), got %+v", res)
			}
			idsPasswords = append(idsPasswords, res.Password)
		}
		assertNoEmails(t, "mode=ids")
	})

	t.Run("mode=all_students sends nothing", func(t *testing.T) {
		baseline := maxUserID(t, repo)
		c := mkBulkStudentWithEmail(t, repo, "noemail_all_c", "NE-C", "origpw-c-123")
		d := mkBulkStudentWithEmail(t, repo, "noemail_all_d", "NE-D", "origpw-d-123")

		w, resp := postBulkReset(r, adminToken, map[string]interface{}{
			"mode":          "all_students",
			"confirm":       "RESET",
			"after_user_id": publicid.Encode(publicid.TypeUser, baseline),
		})
		if w.Code != http.StatusOK {
			t.Fatalf("mode=all_students: want 200, got %d (%s)", w.Code, w.Body.String())
		}
		if resp.Succeeded != 2 || resp.Failed != 0 {
			t.Fatalf("mode=all_students: expected 2 succeeded/0 failed for %q/%q, got %+v",
				c.Username, d.Username, resp)
		}
		assertNoEmails(t, "mode=all_students")
	})

	t.Run("mode=exam_roster sends nothing", func(t *testing.T) {
		baseline := maxUserID(t, repo)
		examID, err := repo.CreateExamination(context.Background(), models.Examination{
			Title:     fmt.Sprintf("noemail_exam_%d", time.Now().UnixNano()),
			CreatedBy: models.UserID(int(admin.ID)),
		})
		if err != nil {
			t.Fatalf("CreateExamination: %v", err)
		}
		e := mkBulkStudentWithEmail(t, repo, "noemail_exam_e", "NE-E", "origpw-e-123")
		f := mkBulkStudentWithEmail(t, repo, "noemail_exam_f", "NE-F", "origpw-f-123")
		if err := repo.AddStudentsToExam(context.Background(), examID, []int{int(e.ID), int(f.ID)}); err != nil {
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
		assertNoEmails(t, "mode=exam_roster")
	})

	t.Run("audit line carries no password", func(t *testing.T) {
		g := mkBulkStudentWithEmail(t, repo, "noemail_audit_g", "NE-G", "origpw-g-123")

		var resp bulkResetResponseBody
		var code int
		logged := captureLog(func() {
			w, parsed := postBulkReset(r, adminToken, map[string]interface{}{
				"mode":     "ids",
				"user_ids": []string{publicid.Encode(publicid.TypeUser, int(g.ID))},
				"confirm":  "RESET",
			})
			code, resp = w.Code, parsed
		})
		if code != http.StatusOK || resp.Succeeded != 1 {
			t.Fatalf("audit phase: want 200/1 succeeded, got %d %+v", code, resp)
		}
		pw := resp.Results[0].Password
		if strings.Contains(logged, pw) {
			t.Fatalf("SECURITY REGRESSION: the generated password leaked into the log output")
		}
		if g.Email == nil || strings.Contains(logged, *g.Email) {
			t.Fatalf("expected the student email on file and NOT in the log output; log=%q", logged)
		}
		assertNoEmails(t, "audit phase")
	})

	// Positive control: the recorder IS wired, so the zero-email assertions
	// above are meaningful. The single-user route with email_password=true is
	// the one admin-driven path that is ALLOWED to email a password.
	t.Run("positive control: single-user reset with email_password does send", func(t *testing.T) {
		h := mkBulkStudentWithEmail(t, repo, "noemail_ctrl_h", "NE-H", "origpw-h-123")

		w := postJSON(r, fmt.Sprintf("/api/v1/users/%s/reset-password",
			publicid.Encode(publicid.TypeUser, int(h.ID))), adminToken,
			map[string]interface{}{"email_password": true})
		if w.Code != http.StatusOK {
			t.Fatalf("single-user reset: want 200, got %d (%s)", w.Code, w.Body.String())
		}
		got := sender.snapshot()
		if len(got) == 0 {
			t.Fatalf("positive control failed: the recording sender captured nothing, so the " +
				"zero-email assertions above prove nothing — check SetEmailSender wiring")
		}
		var credentialToStudent bool
		for _, m := range got {
			if m.Kind == "credential" && h.Email != nil && m.To == *h.Email {
				credentialToStudent = true
			}
		}
		if !credentialToStudent {
			t.Fatalf("positive control failed: expected a credential email to %v, recorded %+v", h.Email, got)
		}
	})

	// Sanity: the bulk phases really did produce distinct plaintexts on the
	// response channel (the thing the admin must copy out, since nothing will
	// ever re-deliver it).
	if len(idsPasswords) == 2 && idsPasswords[0] == idsPasswords[1] {
		t.Fatalf("SECURITY REGRESSION: two students received the identical generated password")
	}
}
