package services
import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

type AuthService struct {
	repo          *repository.PostgresRepository
	jwtSecret     []byte
	loginAttempts sync.Map

	// cipher is nil when PASSWORD_LOG_KEY is unset/invalid: password-event
	// logging is disabled but login and password changes still work.
	cipher     *PasswordCipher
	email      EmailSender
	adminEmail string
}

func NewAuthService(repo *repository.PostgresRepository, secret string) *AuthService {
	cipher, ok := NewPasswordCipherFromEnv()
	if ok {
		log.Printf("auth: password-event logging enabled (encryption key configured)")
	}
	return &AuthService{
		repo:       repo,
		jwtSecret:  []byte(secret),
		cipher:     cipher,
		email:      NoopEmailSender{},
		adminEmail: os.Getenv("ADMIN_NOTIFY_EMAIL"),
	}
}

// SetEmailSender overrides the email delivery implementation (used for wiring
// and tests). The default is NoopEmailSender.
func (s *AuthService) SetEmailSender(sender EmailSender) { s.email = sender }

// KeyConfigured reports whether the password-event encryption key is available.
func (s *AuthService) KeyConfigured() bool { return s.cipher != nil }

func (s *AuthService) Login(ctx context.Context, username, password string) (models.LoginResponse, error) {
	// Rate Limiting: 5 attempts per 10 minutes
	now := time.Now()
	val, _ := s.loginAttempts.LoadOrStore(username, []time.Time{})
	attempts := val.([]time.Time)

	// Filter recent attempts
	var recent []time.Time
	for _, t := range attempts {
		if now.Sub(t) < 10*time.Minute {
			recent = append(recent, t)
		}
	}

	if len(recent) >= 5 {
		return models.LoginResponse{}, errors.New("too many login attempts. please try again in 10 minutes")
	}

	user, err := s.repo.GetUserByUsername(ctx, username)
	if err != nil {
		s.loginAttempts.Store(username, append(recent, now))
		return models.LoginResponse{}, errors.New("invalid username or password")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		s.loginAttempts.Store(username, append(recent, now))
		return models.LoginResponse{}, errors.New("invalid username or password")
	}

	// Success: clear attempts
	s.loginAttempts.Delete(username)

	version, err := s.repo.GetTokenVersion(ctx, int(user.ID))
	if err != nil {
		return models.LoginResponse{}, err
	}

	token, err := s.generateToken(user, version)
	if err != nil {
		return models.LoginResponse{}, err
	}


	user.Password = "" // Clear hash before returning
	return models.LoginResponse{
		Token: token,
		User:  user,
	}, nil
}

func (s *AuthService) ListUsersByRole(ctx context.Context, role models.Role) ([]models.User, error) {
	return s.repo.GetAllUsersByRole(ctx, role)
}

func (s *AuthService) GetAllUsers(ctx context.Context) ([]models.User, error) {
	return s.repo.GetAllUsers(ctx)
}

func (s *AuthService) UpdateUser(ctx context.Context, u models.User) error {
	return s.repo.UpdateUser(ctx, u)
}

func (s *AuthService) DeleteUser(ctx context.Context, id int) error {
	return s.repo.DeleteUser(ctx, id)
}

// ErrStudentRoleNotAllowed is returned by CreateStaff when the request asks for
// a STUDENT account. Controllers map this to 400 and point the caller at the
// dedicated /students endpoint (which drives the single-student parity path).
var ErrStudentRoleNotAllowed = errors.New("STUDENT accounts must be created via /api/v1/students")

// ErrEmailInUse is returned when a create/import would assign an email address
// that another account already holds (enforced by a unique index). Controllers
// map this to 409 Conflict.
var ErrEmailInUse = errors.New("email already in use")

// CreateStaff creates a LECTURER or ADMIN account with a SERVER-GENERATED
// password (the caller may not supply one). It records a `create` password
// event and emails the credential. The returned password is shown to the admin
// exactly once. The existing role hierarchy is preserved: a lecturer may not
// create an admin.
func (s *AuthService) CreateStaff(ctx context.Context, creator models.User, req models.CreateUserRequest) (int, string, error) {
	if req.Role == models.RoleStudent {
		return 0, "", ErrStudentRoleNotAllowed
	}
	if req.Role != models.RoleLecturer && req.Role != models.RoleAdmin {
		return 0, "", errors.New("role must be LECTURER or ADMIN")
	}
	if creator.Role == models.RoleStudent {
		return 0, "", errors.New("students cannot create accounts")
	}
	// Role hierarchy: a lecturer cannot create an admin.
	if creator.Role == models.RoleLecturer && req.Role == models.RoleAdmin {
		return 0, "", errors.New("lecturers cannot create admin accounts")
	}

	password := GeneratePassword()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, "", err
	}

	creatorID := creator.ID
	// email is optional; store NULL when empty so the column stays nullable.
	var emailPtr *string
	if req.Email != "" {
		emailPtr = &req.Email
	}
	user := models.User{
		Username:  req.Username,
		Password:  string(hash),
		Role:      req.Role,
		CreatedBy: &creatorID,
		Email:     emailPtr,
	}
	id, err := s.repo.CreateUser(ctx, user)
	if err != nil {
		if repository.IsEmailConflict(err) {
			return 0, "", ErrEmailInUse
		}
		return 0, "", err
	}

	// New account starts at token_version 0 (no prior tokens exist).
	// logPasswordEvent's changedBy is *int (ARCH-002: repo/service layer stays
	// int-native), so it needs its own int-typed local distinct from creatorID
	// (which stays models.UserID for the CreatedBy field above).
	creatorIDInt := int(creatorID)
	s.logPasswordEvent(ctx, id, &creatorIDInt, models.PasswordEventCreate, password)
	// SendCredential skips silently when the address is empty.
	if err := s.email.SendCredential(req.Email, req.Username, password); err != nil {
		log.Printf("auth: SendCredential failed for new user %q: %v", req.Username, err)
	}
	return id, password, nil
}

// LogStudentCreate records the `create` password event for a newly-created
// student and emails the server-generated credential when an address is on file.
// Used by both the single-student endpoint and the bulk importer (once per newly
// inserted row). Best-effort: never fails the create.
func (s *AuthService) LogStudentCreate(ctx context.Context, creator models.User, studentID int, password, username, email string) {
	creatorID := int(creator.ID)
	s.logPasswordEvent(ctx, studentID, &creatorID, models.PasswordEventCreate, password)
	// SendCredential skips silently when email is empty (some students have none).
	if err := s.email.SendCredential(email, username, password); err != nil {
		log.Printf("auth: SendCredential failed for new student %q: %v", username, err)
	}
}

// ForgotResetThrottle is the minimum interval between honoured forgot-password
// requests for a single account. A request inside this window is silently
// dropped (the generic success response is still returned).
const ForgotResetThrottle = 5 * time.Minute

// Forgot-password OTP verification parameters. Step 1 emails a one-time code;
// step 2 (VerifyForgotPassword) must present it before a new password is
// generated.
const (
	// ResetOTPLength is the number of decimal digits in a verification code.
	ResetOTPLength = 6
	// ResetOTPTTL is how long a verification code stays valid after step 1.
	ResetOTPTTL = 10 * time.Minute
	// ResetOTPMaxAttempts caps verification tries per issued code.
	ResetOTPMaxAttempts = 5
)

// ErrInvalidResetCode is the single sentinel returned for EVERY step-2 failure
// (unknown username, no email, no/expired code, too many attempts, wrong code)
// so the endpoint never reveals which condition failed.
var ErrInvalidResetCode = errors.New("invalid or expired code")

// ForgotPassword handles step 1 of the public, unauthenticated two-step
// forgot-password flow: it issues a one-time verification code and emails it to
// the account's address. No password is changed at this stage — that happens
// only after VerifyForgotPassword accepts the code. The flow is deliberately
// enumeration-safe on TWO axes:
//
//   - Result: it returns nil in every non-fatal case (unknown user, no email on
//     file, throttled) so the caller always emits the same generic 200 without
//     leaking whether the account exists.
//   - Timing: the synchronous portion is uniformly fast on EVERY path. The
//     blocking SMTP round-trip is deferred to a background goroutine, so the
//     response latency no longer reveals valid targets.
//
// When the user exists AND has an email AND is not throttled, beginForgotReset
// authorizes the request, stamps the throttle clock, and stores the code's
// SHA-256 hash + expiry — all synchronously, so a rapid verify cannot race the
// store. Only the email delivery is asynchronous.
func (s *AuthService) ForgotPassword(ctx context.Context, username string) error {
	user, otp, authorized := s.beginForgotReset(ctx, username)
	if !authorized {
		return nil
	}
	// Defer the blocking SMTP send so the response time is identical to the
	// unknown/no-email/throttled paths.
	go s.sendResetCode(user, otp)
	return nil
}

// beginForgotReset is the SYNCHRONOUS, fast portion of forgot-password step 1.
// It resolves the user, requires an email on file, evaluates the abuse
// throttle, and — when the request is authorized — stamps last_forgot_reset_at
// to now() AND stores the freshly generated code (SHA-256 hash + expiry,
// attempts reset) BEFORE returning. Stamping synchronously both drives the
// throttle and narrows the TOCTOU window for concurrent duplicate requests;
// storing the code synchronously means a verify request arriving right after
// the generic 200 can never observe a missing code. It returns
// (user, otp, true) only when the caller should proceed to email the code.
//
// The throttle decision relies exclusively on system_users.last_forgot_reset_at,
// NOT on the password_events audit trail. This makes it independent of
// PASSWORD_LOG_KEY: without an encryption key no password event is written, yet
// the throttle still fires correctly.
func (s *AuthService) beginForgotReset(ctx context.Context, username string) (models.User, string, bool) {
	user, err := s.repo.GetUserByUsername(ctx, username)
	if err != nil {
		// Unknown username (or lookup failure): do not leak via error/timing.
		return models.User{}, "", false
	}
	if user.Email == nil || *user.Email == "" {
		// No email on file — nothing to deliver to. Silent no-op.
		return models.User{}, "", false
	}

	// Abuse throttle: key-independent, reads last_forgot_reset_at only.
	if last, err := s.repo.GetLastForgotResetAt(ctx, int(user.ID)); err == nil && last != nil {
		if time.Since(*last) < ForgotResetThrottle {
			log.Printf("auth: forgot-password throttled for user_id=%d (last reset %s ago)", user.ID, time.Since(*last))
			return models.User{}, "", false
		}
	}

	// Authorize now: stamp the throttle clock synchronously, before any async
	// work is spawned, so a rapid second request is throttled immediately.
	if err := s.repo.SetLastForgotResetAt(ctx, int(user.ID)); err != nil {
		log.Printf("auth: forgot-password failed to stamp throttle clock for user_id=%d: %v", user.ID, err)
		return models.User{}, "", false
	}

	// Issue the one-time code: only its SHA-256 hash is persisted, alongside the
	// expiry, and the attempt counter is reset.
	otp := GenerateResetOTP()
	if err := s.repo.SetResetOTP(ctx, int(user.ID), hashResetOTP(otp), time.Now().Add(ResetOTPTTL)); err != nil {
		log.Printf("auth: forgot-password failed to store reset code for user_id=%d: %v", user.ID, err)
		return models.User{}, "", false
	}
	return user, otp, true
}

// sendResetCode is the ASYNCHRONOUS portion of forgot-password step 1: it only
// emails the verification code. Failures are logged, never returned, and the
// code itself is never logged.
func (s *AuthService) sendResetCode(user models.User, otp string) {
	if user.Email == nil || *user.Email == "" {
		return
	}
	if err := s.email.SendResetCode(*user.Email, user.Username, otp); err != nil {
		log.Printf("auth: forgot-password SendResetCode failed for %q: %v", user.Username, err)
	}
}

// VerifyForgotPassword handles step 2 of the forgot-password flow: it checks
// the submitted one-time code and, on success, clears the code synchronously
// (so it cannot be replayed) and triggers the actual password regeneration +
// credential email asynchronously via performForgotReset. Every failure mode
// returns the single sentinel ErrInvalidResetCode.
func (s *AuthService) VerifyForgotPassword(ctx context.Context, username, otp string) error {
	user, err := s.verifyResetOTP(ctx, username, otp)
	if err != nil {
		return err
	}
	// Defer the expensive work (bcrypt + synchronous SMTP send) as in step 1.
	// Use a fresh context: the request context is cancelled the instant the
	// handler returns.
	go s.performForgotReset(context.Background(), user)
	return nil
}

// verifyResetOTP is the SYNCHRONOUS verification core of step 2. It never
// distinguishes failure causes: unknown username, no email on file, no code
// outstanding, expired code, exhausted attempts, and a wrong code all collapse
// into ErrInvalidResetCode. The attempt counter is incremented atomically
// BEFORE the code comparison, so failed guesses are always counted, and the
// comparison itself is constant-time over the SHA-256 digests.
func (s *AuthService) verifyResetOTP(ctx context.Context, username, otp string) (models.User, error) {
	user, err := s.repo.GetUserByUsername(ctx, username)
	if err != nil {
		return models.User{}, ErrInvalidResetCode
	}
	if user.Email == nil || *user.Email == "" {
		return models.User{}, ErrInvalidResetCode
	}

	hash, expiresAt, attempts, err := s.repo.GetResetOTP(ctx, int(user.ID))
	if err != nil || hash == nil || expiresAt == nil {
		return models.User{}, ErrInvalidResetCode
	}
	if time.Now().After(*expiresAt) {
		return models.User{}, ErrInvalidResetCode
	}
	if attempts >= ResetOTPMaxAttempts {
		return models.User{}, ErrInvalidResetCode
	}

	// Count the attempt FIRST (atomically), so a wrong guess below is already
	// recorded and concurrent guesses cannot slip past the cap.
	newCount, err := s.repo.IncrementResetOTPAttempts(ctx, int(user.ID))
	if err != nil {
		return models.User{}, ErrInvalidResetCode
	}
	if newCount > ResetOTPMaxAttempts {
		return models.User{}, ErrInvalidResetCode
	}

	if subtle.ConstantTimeCompare([]byte(hashResetOTP(otp)), []byte(*hash)) != 1 {
		return models.User{}, ErrInvalidResetCode
	}

	// Clear the code synchronously so it cannot be replayed, even by a request
	// racing the async reset below.
	if err := s.repo.ClearResetOTP(ctx, int(user.ID)); err != nil {
		log.Printf("auth: forgot-password failed to clear reset code for user_id=%d: %v", user.ID, err)
		return models.User{}, ErrInvalidResetCode
	}
	return user, nil
}

// performForgotReset is the ASYNCHRONOUS, heavy portion of the forgot-password
// flow, run only after VerifyForgotPassword has accepted the one-time code. It
// generates a new password, hashes and persists it, bumps token_version
// (evicting existing sessions), writes the `forgot_reset` audit event (only when
// an encryption key is configured), and emails the credential. It is a directly
// callable method (not a closure) so tests can exercise it synchronously without
// depending on goroutine timing. All failures are logged, never returned, and the
// plaintext password is never logged.
func (s *AuthService) performForgotReset(ctx context.Context, user models.User) {
	if user.Email == nil || *user.Email == "" {
		return
	}
	password := GeneratePassword()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		log.Printf("auth: forgot-password hash failed for user_id=%d: %v", user.ID, err)
		return
	}
	if err := s.repo.UpdateUserPassword(ctx, int(user.ID), string(hash)); err != nil {
		log.Printf("auth: forgot-password persist failed for user_id=%d: %v", user.ID, err)
		return
	}
	if err := s.repo.BumpTokenVersion(ctx, int(user.ID)); err != nil {
		log.Printf("auth: forgot-password token bump failed for user_id=%d: %v", user.ID, err)
	}

	changedBy := int(user.ID)
	// Audit event: only persisted when a PASSWORD_LOG_KEY is configured. The
	// throttle above does NOT depend on this write.
	s.logPasswordEvent(ctx, int(user.ID), &changedBy, models.PasswordEventForgotReset, password)
	if err := s.email.SendCredential(*user.Email, user.Username, password); err != nil {
		log.Printf("auth: forgot-password SendCredential failed for %q: %v", user.Username, err)
	}
}

// ResetPassword (admin-only) generates a new password for targetID, updates the
// login hash, bumps token_version (evicting existing sessions), and records an
// `admin_reset` event. The new password is emailed to the target ONLY when
// emailPassword is true and the target has an address on file; otherwise the
// password is returned to the admin (shown once in the UI) and is never
// emailed. Emailing a plaintext password is a deliberate opt-in tradeoff, so
// it defaults off at the caller. Returns the new password once.
func (s *AuthService) ResetPassword(ctx context.Context, actor models.User, targetID int, emailPassword bool) (string, error) {
	target, err := s.repo.GetUserByID(ctx, targetID)
	if err != nil {
		return "", err
	}

	password := GeneratePassword()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	if err := s.repo.UpdateUserPassword(ctx, targetID, string(hash)); err != nil {
		return "", err
	}
	if err := s.repo.BumpTokenVersion(ctx, targetID); err != nil {
		return "", err
	}

	actorID := int(actor.ID)
	s.logPasswordEvent(ctx, targetID, &actorID, models.PasswordEventAdminReset, password)
	recipient := ""
	if emailPassword && target.Email != nil && *target.Email != "" {
		recipient = *target.Email
	}
	if err := s.email.SendCredential(recipient, target.Username, password); err != nil {
		log.Printf("auth: SendCredential failed for reset of %q: %v", target.Username, err)
	}
	return password, nil
}

// ResendCredential re-emails a user's current credential to their stored email
// address (admin-only). The password is recovered from the most recent
// changelog entry (newest first) rather than being re-generated, so the account
// keeps working and existing sessions are not evicted. Requires an email on file
// and a configured encryption key (to recover the plaintext). Returns the
// address the mail was sent to.
func (s *AuthService) ResendCredential(ctx context.Context, actor models.User, targetID int) (string, error) {
	target, err := s.repo.GetUserByID(ctx, targetID)
	if err != nil {
		return "", err
	}
	if target.Email == nil || *target.Email == "" {
		return "", errors.New("user has no email address on file")
	}
	if s.cipher == nil {
		return "", errors.New("password encryption key not configured; cannot recover the password to resend")
	}
	rows, err := s.repo.GetPasswordEvents(ctx, targetID)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", errors.New("no password on record to resend")
	}
	latest := rows[0] // GetPasswordEvents returns newest first.
	password, derr := s.cipher.Decrypt(latest.NewPasswordEnc, latest.Nonce)
	if derr != nil {
		return "", errors.New("failed to recover the stored password")
	}
	log.Printf("auth: admin %q (id=%d) resent credential email for user_id=%d", actor.Username, actor.ID, targetID)
	if err := s.email.SendCredential(*target.Email, target.Username, password); err != nil {
		return "", err
	}
	return *target.Email, nil
}

// GetPasswordEventsView returns the decrypted changelog for a user (admin-only).
// The access itself is logged (metadata only). When the encryption key is not
// configured, keyConfigured is false and NewPassword is omitted from each entry.
func (s *AuthService) GetPasswordEventsView(ctx context.Context, actor models.User, targetID int) (keyConfigured bool, events []models.PasswordEventView, err error) {
	log.Printf("auth: admin %q (id=%d) viewed password events for user_id=%d", actor.Username, actor.ID, targetID)
	rows, err := s.repo.GetPasswordEvents(ctx, targetID)
	if err != nil {
		return s.cipher != nil, nil, err
	}
	views := make([]models.PasswordEventView, 0, len(rows))
	for _, r := range rows {
		v := models.PasswordEventView{
			Source:            r.Source,
			ChangedByUsername: r.ChangedByUsername,
			CreatedAt:         r.CreatedAt,
		}
		if s.cipher != nil {
			if pt, derr := s.cipher.Decrypt(r.NewPasswordEnc, r.Nonce); derr == nil {
				v.NewPassword = pt
			} else {
				log.Printf("auth: failed to decrypt password event for user_id=%d: %v", targetID, derr)
			}
		}
		views = append(views, v)
	}
	return s.cipher != nil, views, nil
}

// logPasswordEvent encrypts and persists a password event, then best-effort
// notifies the admin. When no encryption key is configured, logging is disabled
// (the operation itself is unaffected).
func (s *AuthService) logPasswordEvent(ctx context.Context, userID int, changedBy *int, source, plaintext string) {
	if s.cipher == nil {
		log.Printf("auth: password-event logging disabled (no key); skipping %s event for user_id=%d", source, userID)
		return
	}
	ct, nonce, err := s.cipher.Encrypt(plaintext)
	if err != nil {
		log.Printf("auth: encrypt password event failed (user_id=%d, source=%s): %v", userID, source, err)
		return
	}
	ev := models.PasswordEvent{
		UserID:         userID,
		ChangedBy:      changedBy,
		Source:         source,
		NewPasswordEnc: ct,
		Nonce:          nonce,
	}
	id, err := s.repo.InsertPasswordEvent(ctx, ev)
	if err != nil {
		log.Printf("auth: insert password event failed (user_id=%d, source=%s): %v", userID, source, err)
		return
	}
	ev.ID = id
	if s.adminEmail != "" {
		if err := s.email.SendChangeNotice(s.adminEmail, ev); err != nil {
			log.Printf("auth: SendChangeNotice failed (user_id=%d): %v", userID, err)
		}
	}
}

// SeedAdminAccount creates the initial admin on first startup. The password is
// server-generated (never supplied by hand) and returned so the caller can log
// it once as a bootstrap fallback; when email is set it is also emailed to that
// address (the account owner / author), which is stored on the account so the
// admin can use the forgot-password flow. Idempotent: if the admin already
// exists it returns "" and does nothing (no re-generate, no re-email).
func (s *AuthService) SeedAdminAccount(ctx context.Context, username, email string) (string, error) {
	_, err := s.repo.GetUserByUsername(ctx, username)
	if err == nil {
		return "", nil // Admin exists
	}

	password := GeneratePassword()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}

	// Store the email so the seed admin can receive credentials and reset codes;
	// keep the column NULL when no address is configured.
	var emailPtr *string
	if email != "" {
		emailPtr = &email
	}
	user := models.User{
		Username: username,
		Password: string(hash),
		Role:     models.RoleAdmin,
		Email:    emailPtr,
	}

	id, err := s.repo.CreateUser(ctx, user)
	if err != nil {
		return "", err
	}

	// System-seeded: no human actor, so changedBy is nil.
	s.logPasswordEvent(ctx, id, nil, models.PasswordEventCreate, password)
	// SendCredential skips silently when the address is empty.
	if err := s.email.SendCredential(email, username, password); err != nil {
		log.Printf("auth: SendCredential failed for seed admin %q: %v", username, err)
	}
	return password, nil
}

// ChangeOwnPassword verifies the old password, sets the new one, records a
// `self_change` event, and bumps token_version — which invalidates every
// previously-issued token for the user, including on their other devices. To
// keep the current device logged in, it returns a freshly minted JWT carrying
// the new token_version.
func (s *AuthService) ChangeOwnPassword(ctx context.Context, current models.User, oldPassword, newPassword string) (string, error) {
	user, err := s.repo.GetUserByID(ctx, int(current.ID))
	if err != nil {
		return "", err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(oldPassword)); err != nil {
		return "", errors.New("invalid old password")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	if err := s.repo.UpdateUserPassword(ctx, int(current.ID), string(hash)); err != nil {
		return "", err
	}
	if err := s.repo.BumpTokenVersion(ctx, int(current.ID)); err != nil {
		return "", err
	}

	changedBy := int(current.ID)
	s.logPasswordEvent(ctx, int(current.ID), &changedBy, models.PasswordEventSelfChange, newPassword)

	// Mint a fresh token so the caller's current device stays logged in.
	version, err := s.repo.GetTokenVersion(ctx, int(current.ID))
	if err != nil {
		return "", err
	}
	return s.generateToken(user, version)
}

func (s *AuthService) generateToken(user models.User, tokenVersion int) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		// user_id is deliberately encoded as a plain int, not the models.UserID
		// value: jwt.MapClaims is json.Marshal'ed internally, and UserID's
		// MarshalJSON would otherwise silently turn this internal JWT claim
		// into an opaque public-id STRING instead of the numeric claim
		// parseToken below expects.
		"user_id":       int(user.ID),
		"username":      user.Username,
		"role":          user.Role,
		"token_version": tokenVersion,
		"exp":           time.Now().Add(time.Hour * 24).Unix(),
	})

	return token.SignedString(s.jwtSecret)
}

// parseToken validates the JWT signature/claims and returns the embedded user
// plus the token_version claim. It does NOT check the version against the DB.
func (s *AuthService) parseToken(tokenString string) (models.User, int, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.jwtSecret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))

	if err != nil || !token.Valid {
		return models.User{}, 0, errors.New("invalid token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return models.User{}, 0, errors.New("invalid claims")
	}

	userID, ok1 := claims["user_id"].(float64)
	username, ok2 := claims["username"].(string)
	role, ok3 := claims["role"].(string)

	if !ok1 || !ok2 || !ok3 {
		return models.User{}, 0, errors.New("invalid claims format")
	}

	// token_version is optional for backward compatibility; absent → 0.
	version := 0
	if v, ok := claims["token_version"].(float64); ok {
		version = int(v)
	}

	return models.User{
		ID:       models.UserID(int(userID)),
		Username: username,
		Role:     models.Role(role),
	}, version, nil
}

// ValidateToken validates signature and claims only (no token_version check).
func (s *AuthService) ValidateToken(tokenString string) (models.User, error) {
	user, _, err := s.parseToken(tokenString)
	return user, err
}

// AuthenticateRequest validates the token AND compares its token_version to the
// user's current value. A mismatch (password changed/reset since issue) returns
// an error so the middleware can reject with 401.
func (s *AuthService) AuthenticateRequest(ctx context.Context, tokenString string) (models.User, error) {
	user, version, err := s.parseToken(tokenString)
	if err != nil {
		return models.User{}, err
	}
	current, err := s.repo.GetTokenVersion(ctx, int(user.ID))
	if err != nil {
		return models.User{}, errors.New("invalid token")
	}
	if version != current {
		return models.User{}, errors.New("token invalidated by password change")
	}
	return user, nil
}
