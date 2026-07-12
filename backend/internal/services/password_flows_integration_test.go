package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/testsupport"
	"golang.org/x/crypto/bcrypt"
)

// testFixedKey is a deterministic 32-byte base64 key used to enable password
// event logging in integration tests.
const testFixedKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="

func integrationRepo(t *testing.T) *repository.PostgresRepository {
	t.Helper()
	return testsupport.SharedRepo(t)
}

// newIntegrationAuth builds an AuthService with the encryption key configured so
// password-event logging is active.
func newIntegrationAuth(t *testing.T, repo *repository.PostgresRepository) *AuthService {
	t.Helper()
	t.Setenv(PasswordCryptoEnvKey, testFixedKey)
	return NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
}

func mkUser(t *testing.T, repo *repository.PostgresRepository, role models.Role, plainPassword string) models.User {
	t.Helper()
	ctx := context.Background()
	hash, err := bcrypt.GenerateFromPassword([]byte(plainPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	username := fmt.Sprintf("u_%s_%d", role, time.Now().UnixNano())
	id, err := repo.CreateUser(ctx, models.User{Username: username, Password: string(hash), Role: role})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return models.User{ID: id, Username: username, Role: role}
}

func findUser(t *testing.T, repo *repository.PostgresRepository, id int) models.User {
	t.Helper()
	users, err := repo.GetAllUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u.ID == id {
			return u
		}
	}
	t.Fatalf("user id %d not found", id)
	return models.User{}
}

func strp(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// TestParity_SingleVsBulk proves a single POST /students create and a one-row
// bulk import produce an identical system_users row (field-by-field). The rows
// are created for the SAME student_number sequentially (the single row is
// deleted before the bulk create) so the unique keys do not collide.
func TestParity_SingleVsBulk(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	svc := NewStudentImportService(repo)

	admin := mkUser(t, repo, models.RoleAdmin, "adminpw")
	sn := fmt.Sprintf("PARITY_%d", time.Now().UnixNano())
	in := StudentUpsertInput{
		StudentNumber: sn,
		StudentIDExt:  sn + "_EXT",
		SecLec:        "L1",
		SecLab:        "B1",
		FirstName:     "Pa",
		LastName:      "Rity",
		Email:         "parity@example.com",
	}

	// Single path.
	outcome, singleID, singlePw, err := svc.CreateSingle(ctx, admin.ID, in)
	if err != nil {
		t.Fatalf("CreateSingle: %v", err)
	}
	if outcome != "created" {
		t.Fatalf("expected created, got %q", outcome)
	}
	singleRow := findUser(t, repo, singleID)
	singleHash, err := repo.GetUserByID(ctx, singleID)
	if err != nil {
		t.Fatal(err)
	}
	// Remove so the bulk path can reuse the same unique student_number/id_ext.
	if err := repo.DeleteUser(ctx, singleID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	// Bulk path — one row, identical fields (including email).
	report, err := svc.Import(ctx, admin.ID, []ParsedStudentRow{{
		RowNumber:     2,
		StudentNumber: in.StudentNumber,
		StudentIDExt:  in.StudentIDExt,
		SecLec:        in.SecLec,
		SecLab:        in.SecLab,
		FirstName:     in.FirstName,
		LastName:      in.LastName,
		Email:         in.Email,
	}})
	if err != nil {
		t.Fatalf("bulk Import: %v", err)
	}
	if report.Created != 1 {
		t.Fatalf("expected 1 created in bulk, got %+v", report)
	}
	if len(report.CreatedAccounts) != 1 {
		t.Fatalf("expected 1 created account in bulk report, got %d", len(report.CreatedAccounts))
	}
	bulkPw := report.CreatedAccounts[0].Password
	// Find bulk row by student_number (id is a new serial).
	var bulkRow models.User
	users, _ := repo.GetAllUsers(ctx)
	var bulkID int
	for _, u := range users {
		if u.StudentNumber != nil && *u.StudentNumber == sn {
			bulkRow = u
			bulkID = u.ID
			break
		}
	}
	if bulkID == 0 {
		t.Fatalf("bulk row not found for %s", sn)
	}
	bulkHash, err := repo.GetUserByID(ctx, bulkID)
	if err != nil {
		t.Fatal(err)
	}

	// Field-by-field parity (excluding id, created_at and the salted hash string).
	if singleRow.Username != bulkRow.Username {
		t.Errorf("username mismatch: %q vs %q", singleRow.Username, bulkRow.Username)
	}
	// Login username must be the external student ID (student_id_ext), not the
	// student_number.
	if singleRow.Username != in.StudentIDExt {
		t.Errorf("username should be student_id_ext %q, got %q", in.StudentIDExt, singleRow.Username)
	}
	if singleRow.StudentNumber == nil || *singleRow.StudentNumber == singleRow.Username {
		t.Errorf("username must differ from student_number: user=%q num=%v", singleRow.Username, singleRow.StudentNumber)
	}
	if singleRow.Role != bulkRow.Role || singleRow.Role != models.RoleStudent {
		t.Errorf("role mismatch: %q vs %q", singleRow.Role, bulkRow.Role)
	}
	if singleRow.CreatedBy == nil || bulkRow.CreatedBy == nil || *singleRow.CreatedBy != *bulkRow.CreatedBy {
		t.Errorf("created_by mismatch: %v vs %v", singleRow.CreatedBy, bulkRow.CreatedBy)
	}
	for _, pair := range [][2]string{
		{strp(singleRow.StudentNumber), strp(bulkRow.StudentNumber)},
		{strp(singleRow.StudentIDExt), strp(bulkRow.StudentIDExt)},
		{strp(singleRow.SecLec), strp(bulkRow.SecLec)},
		{strp(singleRow.SecLab), strp(bulkRow.SecLab)},
		{strp(singleRow.FirstName), strp(bulkRow.FirstName)},
		{strp(singleRow.LastName), strp(bulkRow.LastName)},
		{strp(singleRow.Email), strp(bulkRow.Email)},
	} {
		if pair[0] != pair[1] {
			t.Errorf("demographic mismatch: %q vs %q", pair[0], pair[1])
		}
	}
	// Each path generates its OWN random password (no longer the student_number).
	// The returned/reported plaintext must validate against the stored hash, and
	// must NOT be the student_number.
	if singlePw == sn || bulkPw == sn {
		t.Errorf("passwords must be generated, not the student_number: single=%q bulk=%q sn=%q", singlePw, bulkPw, sn)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(singleHash.Password), []byte(singlePw)); err != nil {
		t.Errorf("single-path stored hash does not match its generated password: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(bulkHash.Password), []byte(bulkPw)); err != nil {
		t.Errorf("bulk-path stored hash does not match its generated password: %v", err)
	}
}

func TestEmailUniqueness_RejectsDuplicateAcrossPaths(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)
	svc := NewStudentImportService(repo)
	admin := mkUser(t, repo, models.RoleAdmin, "adminpw")

	email := fmt.Sprintf("dup_%d@example.com", time.Now().UnixNano())

	// First staff account claims the email.
	if _, _, err := auth.CreateStaff(ctx, admin, models.CreateUserRequest{
		Username: fmt.Sprintf("staffA_%d", time.Now().UnixNano()), Role: models.RoleLecturer, Email: email,
	}); err != nil {
		t.Fatalf("first CreateStaff: %v", err)
	}

	// Second staff account with the SAME email → ErrEmailInUse.
	if _, _, err := auth.CreateStaff(ctx, admin, models.CreateUserRequest{
		Username: fmt.Sprintf("staffB_%d", time.Now().UnixNano()), Role: models.RoleLecturer, Email: email,
	}); !errors.Is(err, ErrEmailInUse) {
		t.Fatalf("expected ErrEmailInUse for duplicate staff email, got %v", err)
	}

	// A student claiming a staff email → ErrEmailInUse (enforced across paths).
	if _, _, _, err := svc.CreateSingle(ctx, admin.ID, StudentUpsertInput{
		StudentNumber: fmt.Sprintf("SNU_%d", time.Now().UnixNano()),
		StudentIDExt:  fmt.Sprintf("SIDU_%d", time.Now().UnixNano()),
		Email:         email,
	}); !errors.Is(err, ErrEmailInUse) {
		t.Fatalf("expected ErrEmailInUse for student reusing a staff email, got %v", err)
	}

	// Bulk import of a row with that email → skipped as a row error, not created.
	report, err := svc.Import(ctx, admin.ID, []ParsedStudentRow{{
		RowNumber:     1,
		StudentNumber: fmt.Sprintf("SNB_%d", time.Now().UnixNano()),
		StudentIDExt:  fmt.Sprintf("SIDB_%d", time.Now().UnixNano()),
		Email:         email,
	}})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.Created != 0 || report.Skipped != 1 || len(report.Errors) != 1 {
		t.Fatalf("expected the duplicate-email row to be skipped with an error, got %+v", report)
	}

	// A NULL/empty email never collides — many students legitimately have none.
	if _, _, _, err := svc.CreateSingle(ctx, admin.ID, StudentUpsertInput{
		StudentNumber: fmt.Sprintf("SNN1_%d", time.Now().UnixNano()),
		StudentIDExt:  fmt.Sprintf("SIDN1_%d", time.Now().UnixNano()),
	}); err != nil {
		t.Fatalf("first no-email student: %v", err)
	}
	if _, _, _, err := svc.CreateSingle(ctx, admin.ID, StudentUpsertInput{
		StudentNumber: fmt.Sprintf("SNN2_%d", time.Now().UnixNano()),
		StudentIDExt:  fmt.Sprintf("SIDN2_%d", time.Now().UnixNano()),
	}); err != nil {
		t.Fatalf("second no-email student must NOT collide on NULL email: %v", err)
	}
}

func TestImport_CreatedAccountsGenerateAndPreserveOnReimport(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	svc := NewStudentImportService(repo)
	admin := mkUser(t, repo, models.RoleAdmin, "adminpw")

	base := fmt.Sprintf("BULK_%d", time.Now().UnixNano())
	rows := []ParsedStudentRow{
		{RowNumber: 1, StudentNumber: base + "_A", StudentIDExt: base + "_AX", FirstName: "A", LastName: "A", Email: base + "a@example.com"},
		{RowNumber: 2, StudentNumber: base + "_B", StudentIDExt: base + "_BX", FirstName: "B", LastName: "B"},
	}

	report, err := svc.Import(ctx, admin.ID, rows)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.Created != 2 || len(report.CreatedAccounts) != 2 {
		t.Fatalf("expected 2 created accounts, got created=%d accounts=%d", report.Created, len(report.CreatedAccounts))
	}
	a, b := report.CreatedAccounts[0], report.CreatedAccounts[1]
	// Passwords are generated (not the student_number) and distinct per student.
	if a.Password == a.StudentNumber || b.Password == b.StudentNumber {
		t.Fatalf("passwords must be generated, not the student_number")
	}
	if a.Password == b.Password {
		t.Fatalf("each student must get its own password")
	}
	// The generated password actually validates against the stored hash.
	ua, err := repo.GetUserByID(ctx, a.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(ua.Password), []byte(a.Password)); err != nil {
		t.Fatalf("generated password does not validate: %v", err)
	}

	// Re-import the SAME roster → all updates, no new passwords, password preserved.
	report2, err := svc.Import(ctx, admin.ID, rows)
	if err != nil {
		t.Fatalf("re-Import: %v", err)
	}
	if report2.Created != 0 || len(report2.CreatedAccounts) != 0 {
		t.Fatalf("re-import should create nothing, got created=%d accounts=%d", report2.Created, len(report2.CreatedAccounts))
	}
	if report2.Updated != 2 {
		t.Fatalf("re-import should update 2, got %d", report2.Updated)
	}
	ua2, err := repo.GetUserByID(ctx, a.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(ua2.Password), []byte(a.Password)); err != nil {
		t.Fatalf("re-import must preserve the original password: %v", err)
	}
}

func TestCreateStaff_GeneratesPasswordLogsEventRejectsStudent(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)

	admin := mkUser(t, repo, models.RoleAdmin, "adminpw")

	// STUDENT role → dedicated sentinel error (controller maps to 400 → /students).
	if _, _, err := auth.CreateStaff(ctx, admin, models.CreateUserRequest{
		Username: "should_fail", Role: models.RoleStudent,
	}); err != ErrStudentRoleNotAllowed {
		t.Fatalf("expected ErrStudentRoleNotAllowed, got %v", err)
	}

	// Lecturer creation with a server-generated password + `create` event.
	username := fmt.Sprintf("newlec_%d", time.Now().UnixNano())
	id, pw, err := auth.CreateStaff(ctx, admin, models.CreateUserRequest{Username: username, Role: models.RoleLecturer})
	if err != nil {
		t.Fatalf("CreateStaff: %v", err)
	}
	if len(pw) != PasswordLength {
		t.Fatalf("expected generated password, got %q", pw)
	}
	created, err := repo.GetUserByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(created.Password), []byte(pw)); err != nil {
		t.Fatalf("generated password does not validate: %v", err)
	}
	_, events, err := auth.GetPasswordEventsView(ctx, admin, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Source != models.PasswordEventCreate || events[0].NewPassword != pw {
		t.Fatalf("expected 1 create event with the generated password, got %+v", events)
	}

	// Lecturer cannot create an admin (role hierarchy preserved).
	lecturer := mkUser(t, repo, models.RoleLecturer, "lecpw")
	if _, _, err := auth.CreateStaff(ctx, lecturer, models.CreateUserRequest{
		Username: "wannabe_admin", Role: models.RoleAdmin,
	}); err == nil {
		t.Fatalf("expected lecturer→admin creation to be rejected")
	}
}

// spyEmailSender records SendCredential calls so tests can assert what was
// emailed. The other interface methods are no-ops.
type spyEmailSender struct {
	credentialCalls []struct{ to, username, password string }
}

func (s *spyEmailSender) SendCredential(toEmail, username, password string) error {
	if toEmail == "" {
		return nil // mirror SMTP: empty address → no send
	}
	s.credentialCalls = append(s.credentialCalls, struct{ to, username, password string }{toEmail, username, password})
	return nil
}
func (s *spyEmailSender) SendResetCode(toEmail, username, code string) error { return nil }
func (s *spyEmailSender) SendChangeNotice(adminEmail string, ev models.PasswordEvent) error {
	return nil
}

func TestSeedAdminAccount_GeneratesEmailsAndIsIdempotent(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)
	spy := &spyEmailSender{}
	auth.SetEmailSender(spy)

	username := fmt.Sprintf("seedadmin_%d", time.Now().UnixNano())
	email := username + "@example.com"

	// First seed: generates a password, stores the email, emails it.
	pw, err := auth.SeedAdminAccount(ctx, username, email)
	if err != nil {
		t.Fatalf("SeedAdminAccount: %v", err)
	}
	if len(pw) != PasswordLength {
		t.Fatalf("expected generated password, got %q", pw)
	}
	created, err := repo.GetUserByUsername(ctx, username)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if created.Role != models.RoleAdmin {
		t.Fatalf("expected admin role, got %q", created.Role)
	}
	if created.Email == nil || *created.Email != email {
		t.Fatalf("expected stored email %q, got %v", email, strp(created.Email))
	}
	if err := bcrypt.CompareHashAndPassword([]byte(created.Password), []byte(pw)); err != nil {
		t.Fatalf("generated password does not validate: %v", err)
	}
	// A create event was written with the generated password.
	_, events, err := auth.GetPasswordEventsView(ctx, created, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Source != models.PasswordEventCreate || events[0].NewPassword != pw {
		t.Fatalf("expected 1 create event with the generated password, got %+v", events)
	}
	// The credential was emailed to the author, matching the returned password.
	if len(spy.credentialCalls) != 1 {
		t.Fatalf("expected 1 SendCredential call, got %d", len(spy.credentialCalls))
	}
	if c := spy.credentialCalls[0]; c.to != email || c.username != username || c.password != pw {
		t.Fatalf("emailed credential mismatch: %+v (want to=%q user=%q pw=%q)", c, email, username, pw)
	}

	// Second seed: idempotent — no new password, no re-email.
	pw2, err := auth.SeedAdminAccount(ctx, username, email)
	if err != nil {
		t.Fatalf("second SeedAdminAccount: %v", err)
	}
	if pw2 != "" {
		t.Fatalf("expected empty password on idempotent seed, got %q", pw2)
	}
	if len(spy.credentialCalls) != 1 {
		t.Fatalf("expected no additional email on idempotent seed, got %d calls", len(spy.credentialCalls))
	}
}

func TestSeedAdminAccount_NoEmail_StillCreates(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)
	spy := &spyEmailSender{}
	auth.SetEmailSender(spy)

	username := fmt.Sprintf("seedadmin_noemail_%d", time.Now().UnixNano())

	// Empty email: account still created with a returned (stdout-fallback) password,
	// email column NULL, and no send attempted.
	pw, err := auth.SeedAdminAccount(ctx, username, "")
	if err != nil {
		t.Fatalf("SeedAdminAccount: %v", err)
	}
	if len(pw) != PasswordLength {
		t.Fatalf("expected generated password, got %q", pw)
	}
	created, err := repo.GetUserByUsername(ctx, username)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if created.Email != nil {
		t.Fatalf("expected NULL email, got %q", *created.Email)
	}
	if len(spy.credentialCalls) != 0 {
		t.Fatalf("expected no email when address is empty, got %d calls", len(spy.credentialCalls))
	}
}

func TestResendCredential_ReSendsCurrentPasswordToStoredEmail(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)
	spy := &spyEmailSender{}
	auth.SetEmailSender(spy)

	admin := mkUser(t, repo, models.RoleAdmin, "adminpw")
	username := fmt.Sprintf("resendlec_%d", time.Now().UnixNano())
	email := username + "@example.com"

	// Create staff with an email → one credential email at create time.
	id, pw, err := auth.CreateStaff(ctx, admin, models.CreateUserRequest{Username: username, Role: models.RoleLecturer, Email: email})
	if err != nil {
		t.Fatalf("CreateStaff: %v", err)
	}
	if len(spy.credentialCalls) != 1 {
		t.Fatalf("expected 1 email on create, got %d", len(spy.credentialCalls))
	}

	// Resend re-emails the SAME current password (recovered from the changelog),
	// to the stored address, without changing it.
	to, err := auth.ResendCredential(ctx, admin, id)
	if err != nil {
		t.Fatalf("ResendCredential: %v", err)
	}
	if to != email {
		t.Fatalf("expected emailed_to %q, got %q", email, to)
	}
	if len(spy.credentialCalls) != 2 {
		t.Fatalf("expected 2 emails after resend, got %d", len(spy.credentialCalls))
	}
	if c := spy.credentialCalls[1]; c.to != email || c.password != pw {
		t.Fatalf("resend mismatch: %+v (want to=%q pw=%q)", c, email, pw)
	}
	// Password unchanged (still logs in).
	updated, err := repo.GetUserByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(updated.Password), []byte(pw)); err != nil {
		t.Fatalf("password should be unchanged after resend: %v", err)
	}

	// A user with no email → clear precondition error, no send.
	noEmail := mkUser(t, repo, models.RoleLecturer, "x")
	if _, err := auth.ResendCredential(ctx, admin, noEmail.ID); err == nil {
		t.Fatalf("expected error resending to a user with no email")
	}
	if len(spy.credentialCalls) != 2 {
		t.Fatalf("expected no extra send for no-email user, got %d", len(spy.credentialCalls))
	}
}

func TestResetPassword_WritesEventAndReturnsPassword(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)

	admin := mkUser(t, repo, models.RoleAdmin, "adminpw")
	target := mkUser(t, repo, models.RoleLecturer, "oldpw")

	newPass, err := auth.ResetPassword(ctx, admin, target.ID)
	if err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if len(newPass) != PasswordLength {
		t.Fatalf("expected generated password, got %q", newPass)
	}
	// New password must actually log in.
	updated, err := repo.GetUserByID(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(updated.Password), []byte(newPass)); err != nil {
		t.Fatalf("reset password does not validate: %v", err)
	}
	// admin_reset event written with the decrypted value == the returned password.
	keyOK, events, err := auth.GetPasswordEventsView(ctx, admin, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !keyOK {
		t.Fatalf("expected key configured")
	}
	if len(events) != 1 || events[0].Source != models.PasswordEventAdminReset {
		t.Fatalf("expected 1 admin_reset event, got %+v", events)
	}
	if events[0].NewPassword != newPass {
		t.Fatalf("changelog value %q != returned password %q", events[0].NewPassword, newPass)
	}
	if events[0].ChangedByUsername != admin.Username {
		t.Fatalf("expected actor %q, got %q", admin.Username, events[0].ChangedByUsername)
	}
}

func TestSelfChange_WritesEventAndReturnsToken(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)

	user := mkUser(t, repo, models.RoleLecturer, "oldpw")

	token, err := auth.ChangeOwnPassword(ctx, user, "oldpw", "brandNewPw99")
	if err != nil {
		t.Fatalf("ChangeOwnPassword: %v", err)
	}
	if token == "" {
		t.Fatalf("expected a fresh token")
	}
	// The fresh token authenticates (carries the bumped token_version).
	if _, err := auth.AuthenticateRequest(ctx, token); err != nil {
		t.Fatalf("fresh token should authenticate: %v", err)
	}
	// self_change event recorded with decrypted value.
	_, events, err := auth.GetPasswordEventsView(ctx, user, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Source != models.PasswordEventSelfChange {
		t.Fatalf("expected 1 self_change event, got %+v", events)
	}
	if events[0].NewPassword != "brandNewPw99" {
		t.Fatalf("expected decrypted new password, got %q", events[0].NewPassword)
	}
	// Wrong old password is rejected.
	if _, err := auth.ChangeOwnPassword(ctx, user, "wrong", "whatever12345"); err == nil {
		t.Fatalf("expected rejection with wrong old password")
	}
}

func TestChangelog_KeyNotConfigured_OmitsValues(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()

	// AuthService WITH key writes an event.
	authWithKey := newIntegrationAuth(t, repo)
	admin := mkUser(t, repo, models.RoleAdmin, "adminpw")
	target := mkUser(t, repo, models.RoleLecturer, "oldpw")
	if _, err := authWithKey.ResetPassword(ctx, admin, target.ID); err != nil {
		t.Fatal(err)
	}

	// AuthService WITHOUT key: changelog omits new_password and reports not configured.
	os.Unsetenv(PasswordCryptoEnvKey)
	authNoKey := NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	keyOK, events, err := authNoKey.GetPasswordEventsView(ctx, admin, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if keyOK {
		t.Fatalf("expected key_configured=false")
	}
	if len(events) != 1 {
		t.Fatalf("expected the event to still be listed, got %d", len(events))
	}
	if events[0].NewPassword != "" {
		t.Fatalf("expected new_password omitted when key not configured, got %q", events[0].NewPassword)
	}
}

func TestTokenInvalidation_ResetEvictsOldToken(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)

	admin := mkUser(t, repo, models.RoleAdmin, "adminpw")
	user := mkUser(t, repo, models.RoleLecturer, "userpw")

	// Mint a token via the normal login path.
	loginRes, err := auth.Login(ctx, user.Username, "userpw")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	oldToken := loginRes.Token
	if _, err := auth.AuthenticateRequest(ctx, oldToken); err != nil {
		t.Fatalf("token should be valid before reset: %v", err)
	}

	// Admin reset bumps token_version.
	if _, err := auth.ResetPassword(ctx, admin, user.ID); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}

	// Old token is now rejected.
	if _, err := auth.AuthenticateRequest(ctx, oldToken); err == nil {
		t.Fatalf("expected old token to be rejected after reset")
	}
}
