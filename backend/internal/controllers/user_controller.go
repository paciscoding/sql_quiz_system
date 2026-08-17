package controllers

import (
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

type UserController struct {
	authService    *services.AuthService
	studentService *services.StudentImportService
	emailJobs      *services.EmailJobRegistry
}

func NewUserController(authService *services.AuthService, studentService *services.StudentImportService) *UserController {
	return &UserController{authService: authService, studentService: studentService}
}

// SetEmailJobRegistry wires the in-memory progress registry used by the opt-in
// credential email on bulk password reset. It must be the SAME registry
// instance the LecturerController holds: the admin UI polls one status
// endpoint (GET /students/import-credentials/:job_id/status) for jobs started
// by either path, and that endpoint reads the lecturer controller's registry.
// Leaving it nil disables the send outright (startCredentialEmailJob returns
// "" and no worker runs) rather than sending untracked.
func (ctrl *UserController) SetEmailJobRegistry(reg *services.EmailJobRegistry) {
	ctrl.emailJobs = reg
}

func (ctrl *UserController) Login(c *gin.Context) {
	var req models.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	res, err := ctrl.authService.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, res)
}

func (ctrl *UserController) ChangePassword(c *gin.Context) {
	userVal, _ := c.Get("user")
	currentUser := userVal.(models.User)

	var req models.ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	token, err := ctrl.authService.ChangeOwnPassword(c.Request.Context(), currentUser, req.OldPassword, req.NewPassword)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	// Returns a fresh JWT (token_version bumped) so the current device stays
	// logged in while other devices' tokens are invalidated.
	c.JSON(http.StatusOK, gin.H{"token": token})
}

func (ctrl *UserController) ListStudents(c *gin.Context) {
	students, err := ctrl.authService.ListUsersByRole(c.Request.Context(), models.RoleStudent)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, students)
}

func (ctrl *UserController) GetAllUsers(c *gin.Context) {
	users, err := ctrl.authService.GetAllUsers(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, users)
}

func (ctrl *UserController) UpdateUser(c *gin.Context) {
	id, ok := idParam(c, "id", publicid.TypeUser)
	if !ok {
		return
	}

	var u models.User
	if err := c.ShouldBindJSON(&u); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	u.ID = models.UserID(id)

	if err := ctrl.authService.UpdateUser(c.Request.Context(), u); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "User updated successfully"})
}

func (ctrl *UserController) DeleteUser(c *gin.Context) {
	id, ok := idParam(c, "id", publicid.TypeUser)
	if !ok {
		return
	}

	if err := ctrl.authService.DeleteUser(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "User deleted successfully"})
}

// CreateAccount handles POST /api/v1/users (Admin, Lecturer). It creates a
// LECTURER or ADMIN account with a server-generated password. A STUDENT role is
// rejected with 400 pointing at /students.
func (ctrl *UserController) CreateAccount(c *gin.Context) {
	// Get creator from context (set by middleware)
	userVal, _ := c.Get("user")
	creator := userVal.(models.User)

	var req models.CreateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	id, password, err := ctrl.authService.CreateStaff(c.Request.Context(), creator, req)
	if err != nil {
		if errors.Is(err, services.ErrStudentRoleNotAllowed) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, services.ErrEmailInUse) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": publicid.Encode(publicid.TypeUser, id), "generated_password": password})
}

// CreateStudent handles POST /api/v1/students (Admin, Lecturer). It creates or
// upserts a single student through the SAME shared path as the CSV importer, so
// a manual add and a one-row import produce an identical row. New students get a
// server-generated password, emailed when an address is present and shown once.
func (ctrl *UserController) CreateStudent(c *gin.Context) {
	userVal, _ := c.Get("user")
	creator := userVal.(models.User)

	var req models.StudentImportRow
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.StudentNumber == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "student_number is required"})
		return
	}
	// student_id_ext is the login username, so it is mandatory for students.
	if req.StudentIDExt == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "student_id is required"})
		return
	}

	outcome, id, password, err := ctrl.studentService.CreateSingle(c.Request.Context(), int(creator.ID), services.StudentUpsertInput{
		StudentNumber: req.StudentNumber,
		StudentIDExt:  req.StudentIDExt,
		SecLec:        req.SecLec,
		SecLab:        req.SecLab,
		FirstName:     req.FirstName,
		LastName:      req.LastName,
		Email:         req.Email,
	})
	if err != nil {
		if errors.Is(err, services.ErrEmailInUse) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	resp := gin.H{"id": publicid.Encode(publicid.TypeUser, id), "created": outcome == "created"}
	// Only a newly-inserted row has a fresh password (the upsert preserves an
	// existing student's password on conflict). Log the event + email + reveal it
	// only in that case.
	if outcome == "created" {
		ctrl.authService.LogStudentCreate(c.Request.Context(), creator, id, password, req.StudentIDExt, req.Email)
		resp["generated_password"] = password
	}

	c.JSON(http.StatusCreated, resp)
}

// PasswordResetEmailEnabledEnv gates the two PUBLIC forgot-password routes.
// It defaults to DISABLED: SMTP delivery is unreliable in this deployment, so
// the emailed-code flow is a dead end for students, and a dead end that still
// accepts input is worse than one that says so. Admins reset passwords from the
// admin UI instead (POST /users/:id/reset-password, which never needs email and
// is NOT affected by this flag). Set to a truthy value to restore the flow with
// no code change.
const PasswordResetEmailEnabledEnv = "PASSWORD_RESET_EMAIL_ENABLED"

// passwordResetDisabledMessage is the ONLY body the gated routes emit. Like
// forgotPasswordMessage below it is a constant: it must not vary by username,
// by whether the account exists, or by anything else, so the disabled routes are
// no more of an enumeration oracle than the enabled ones.
const passwordResetDisabledMessage = "Password reset by email is unavailable. Please contact your administrator to have your password reset."

// PasswordResetEmailEnabled reports whether the public email-based reset flow is
// switched on. Unset, empty, or any non-boolean value means disabled — the fail-
// closed direction, matching the "explicitly truthy or off" reading of the flag.
func PasswordResetEmailEnabled() bool {
	v := os.Getenv(PasswordResetEmailEnabledEnv)
	if v == "" {
		return false
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return false
	}
	return parsed
}

// PasswordResetEmailGate is middleware for the two public forgot-password
// routes. When the flow is disabled it aborts with a constant 404 before the
// handler runs, so no lookup, no email, and no timing-observable work happens;
// when enabled it is a pass-through and the existing handlers are untouched.
// Registered as middleware rather than by omitting the routes so flipping the
// env var is the only thing needed to bring the flow back.
func PasswordResetEmailGate(enabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if enabled {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": passwordResetDisabledMessage})
	}
}

// forgotPasswordMessage is the single, generic response for step 1 of the
// public forgot-password flow (code delivery). It is returned identically
// whether or not the account exists / has an email, so the endpoint cannot be
// used to enumerate usernames.
const forgotPasswordMessage = "If an account with that username exists and has an email on file, a verification code has been sent."

// forgotPasswordVerifiedMessage is the success response for step 2 of the
// public forgot-password flow: the code was accepted and the new password is
// on its way by email.
const forgotPasswordVerifiedMessage = "Verification successful. A new password has been sent to your email."

// ForgotPassword handles the PUBLIC POST /api/v1/forgot-password endpoint (no
// auth middleware) — step 1 of the two-step flow: it emails a one-time
// verification code. It always responds 200 with an identical generic body.
func (ctrl *UserController) ForgotPassword(c *gin.Context) {
	var req models.ForgotPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Even a malformed body gets the generic 200 to avoid signalling anything.
		c.JSON(http.StatusOK, gin.H{"message": forgotPasswordMessage})
		return
	}
	// The service is enumeration-safe (returns nil in all non-fatal cases). We
	// ignore its error entirely and always return the same body.
	_ = ctrl.authService.ForgotPassword(c.Request.Context(), req.Username)
	c.JSON(http.StatusOK, gin.H{"message": forgotPasswordMessage})
}

// VerifyForgotPassword handles the PUBLIC POST /api/v1/forgot-password/verify
// endpoint (no auth middleware) — step 2 of the two-step flow. Every failure
// (malformed body, unknown user, wrong/expired code, too many attempts) gets
// the IDENTICAL generic 400 so the endpoint reveals nothing about which check
// failed. Success triggers the actual password regeneration + credential email.
func (ctrl *UserController) VerifyForgotPassword(c *gin.Context) {
	var req models.ForgotPasswordVerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": services.ErrInvalidResetCode.Error()})
		return
	}
	if err := ctrl.authService.VerifyForgotPassword(c.Request.Context(), req.Username, req.OTP); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": services.ErrInvalidResetCode.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": forgotPasswordVerifiedMessage})
}

// ResetPassword handles POST /api/v1/users/:id/reset-password (Admin only).
func (ctrl *UserController) ResetPassword(c *gin.Context) {
	userVal, _ := c.Get("user")
	actor := userVal.(models.User)

	id, ok := idParam(c, "id", publicid.TypeUser)
	if !ok {
		return
	}

	var body struct {
		EmailPassword bool `json:"email_password"`
	}
	_ = c.ShouldBindJSON(&body) // optional body; empty/absent => EmailPassword defaults to false

	password, err := ctrl.authService.ResetPassword(c.Request.Context(), actor, id, body.EmailPassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"generated_password": password, "email_requested": body.EmailPassword})
}

// bulkResetPasswordRequest is the body for POST
// /api/v1/users/bulk-reset-password (ARCH-031 (2)). BatchSize is a pointer so
// an OMITTED field (-> default 100) can be distinguished from an explicit 0
// (-> clamped to 1); every opaque token field is decoded by the handler, not
// by JSON binding, so a malformed token yields a clean 400 rather than a
// bind-time panic/500.
type bulkResetPasswordRequest struct {
	Mode        string   `json:"mode"`
	UserIDs     []string `json:"user_ids"`
	ExamID      string   `json:"exam_id"`
	Confirm     string   `json:"confirm"`
	AfterUserID string   `json:"after_user_id"`
	BatchSize   *int     `json:"batch_size"`
	// SendEmail opts INTO emailing each new password. It defaults to false
	// (absent == false, the zero value) on purpose: this endpoint has never
	// sent mail, its results are designed to be printed and handed out, and a
	// silent default of true would mail an entire roster the first time an
	// admin ran their usual reset. The rotating roster importer defaults the
	// other way because emailing is that endpoint's whole point.
	SendEmail bool `json:"send_email"`
}

// BulkResetPassword handles POST /api/v1/users/bulk-reset-password
// (Admin only — route registration itself is a later wave's cmd/main.go
// TODO; this handler is complete and ready to be wired in). It resets a
// batch of STUDENT accounts selected by one of three modes (ARCH-031/032):
// explicit ids, all_students, or exam_roster. Every validation below runs
// BEFORE any write, in the order ARCH-031 (2) specifies: the confirmation
// phrase, the mode enum, mode/field agreement, the user_ids cap, then
// batch_size clamping. mode=="ids" additionally aborts the WHOLE request
// before any write when any supplied id does not resolve to a STUDENT
// (ARCH-031 (3) / ARCH-032 S2) — this is what makes "reset everyone
// including the admins" impossible through this endpoint: staff and admin
// accounts (including the actor's own) can only be reset via the existing
// single-user /users/:id/reset-password route.
func (ctrl *UserController) BulkResetPassword(c *gin.Context) {
	userVal, _ := c.Get("user")
	actor := userVal.(models.User)

	var req bulkResetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// S3: the literal confirmation phrase is required on EVERY request,
	// including continuation batches carrying after_user_id. The server never
	// trusts the UI's own confirm-gate for this.
	if req.Confirm != "RESET" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "confirmation_required"})
		return
	}

	switch req.Mode {
	case "ids", "all_students", "exam_roster":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown mode"})
		return
	}

	// Mode/field agreement: the mode is an explicit enum, never inferred from
	// which optional field happens to be present (ARCH-031 (1)).
	switch req.Mode {
	case "ids":
		if len(req.UserIDs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "user_ids is required for mode=ids"})
			return
		}
		if req.ExamID != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "exam_id must not be set for mode=ids"})
			return
		}
	case "all_students":
		if len(req.UserIDs) != 0 || req.ExamID != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "user_ids/exam_id must not be set for mode=all_students"})
			return
		}
	case "exam_roster":
		if req.ExamID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "exam_id is required for mode=exam_roster"})
			return
		}
		if len(req.UserIDs) != 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "user_ids must not be set for mode=exam_roster"})
			return
		}
	}

	if len(req.UserIDs) > 1000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too_many_ids"})
		return
	}

	// batch_size: default 100, hard clamp [1,200] (ARCH-031 (2)/(6), OQ-4).
	batchSize := 100
	if req.BatchSize != nil {
		batchSize = *req.BatchSize
	}
	if batchSize < 1 {
		batchSize = 1
	}
	if batchSize > 200 {
		batchSize = 200
	}

	userIDs := make([]int, 0, len(req.UserIDs))
	for _, token := range req.UserIDs {
		id, err := publicid.Decode(publicid.TypeUser, token)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user_ids token"})
			return
		}
		userIDs = append(userIDs, id)
	}

	examID := 0
	if req.ExamID != "" {
		id, err := publicid.Decode(publicid.TypeExam, req.ExamID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid exam_id"})
			return
		}
		examID = id
	}

	afterID := 0
	if req.AfterUserID != "" {
		id, err := publicid.Decode(publicid.TypeUser, req.AfterUserID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid after_user_id"})
			return
		}
		afterID = id
	}

	ctx := c.Request.Context()

	// S2 hard rule: mode=="ids" can never touch a non-STUDENT row. Abort the
	// WHOLE request before any write when even one supplied id doesn't resolve
	// to an existing STUDENT.
	if req.Mode == "ids" {
		badIDs, err := ctrl.authService.NonStudentUserIDs(ctx, userIDs)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if len(badIDs) > 0 {
			badTokens := make([]string, len(badIDs))
			for i, id := range badIDs {
				badTokens[i] = publicid.Encode(publicid.TypeUser, id)
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "non_student_target", "user_ids": badTokens})
			return
		}
	}

	targets, repoRemaining, err := ctrl.authService.ResolveBulkResetTargets(ctx, req.Mode, userIDs, examID, afterID, batchSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	results, failures := ctrl.authService.BulkResetPasswords(ctx, actor, targets)

	// Optional delivery (opt-in; see bulkResetPasswordRequest.SendEmail). Only
	// rows that actually reset are considered -- a failed reset left that
	// student's OLD password in place, so mailing the new one would tell them
	// to type a password that does not work.
	//
	// This runs per batch: a 250-student reset is three requests and therefore
	// three jobs. The admin UI tracks the whole set (see useEmailJobStatus's
	// multi-job aggregation) rather than only the newest one, so the progress
	// it shows covers the entire run instead of the last 50 students.
	jobID := ""
	if req.SendEmail {
		emailConfigured := ctrl.authService != nil && ctrl.authService.EmailConfigured()
		recipients := make([]credentialRecipient, 0, len(results))
		for i := range results {
			switch {
			case results[i].Email == "":
				// No address on file. Still reset, still in the response --
				// this password can only be delivered by hand.
				results[i].EmailStatus = services.EmailStatusNoEmail
			case !emailConfigured:
				// Never claim a send on a server with no sender wired in:
				// NoopEmailSender.SendCredential returns nil, so counting it
				// as success would report "40/40 emailed" with nothing sent.
				results[i].EmailStatus = services.EmailStatusNotConfigured
			default:
				results[i].EmailStatus = services.EmailStatusQueued
				recipients = append(recipients, credentialRecipient{
					Email: results[i].Email, LoginID: results[i].Username, Password: results[i].Password,
				})
			}
		}
		jobID = startCredentialEmailJob(ctrl.authService, ctrl.emailJobs, int(actor.ID), recipients)
	}

	remaining := repoRemaining - len(targets)
	if remaining < 0 {
		remaining = 0
	}
	var nextAfterUserID interface{}
	if remaining > 0 && len(targets) > 0 {
		nextAfterUserID = publicid.Encode(publicid.TypeUser, targets[len(targets)-1].UserID)
	}

	// S6: exactly one structured audit line per request, naming the actor,
	// mode, counts and the internal ids of the affected set. NEVER a
	// password, a hash, or a ciphertext.
	affectedIDs := make([]int, len(targets))
	for i, t := range targets {
		affectedIDs[i] = t.UserID
	}
	log.Printf("audit: bulk_password_reset actor=%q actor_id=%d mode=%s exam_id=%d requested=%d succeeded=%d failed=%d send_email=%t user_ids=%v",
		actor.Username, actor.ID, req.Mode, examID, len(targets), len(results), len(failures), req.SendEmail, affectedIDs)

	// S7: POST-only (never GET) and no-store, so a credential dump can never
	// land in a URL, browser history, proxy access log, or be replayed from
	// a cache.
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"mode":               req.Mode,
		"processed":          len(targets),
		"succeeded":          len(results),
		"failed":             len(failures),
		"remaining":          remaining,
		"next_after_user_id": nextAfterUserID,
		"key_configured":     ctrl.authService.KeyConfigured(),
		"results":            results,
		"failures":           failures,
		// "" whenever nothing is being sent: send_email was false (the
		// default), no reset row had an address, or delivery is not configured.
		"job_id": jobID,
	})
}

// ResendCredential handles POST /api/v1/users/:id/resend-credential (Admin only).
// Re-emails the user's current credential to their stored address.
func (ctrl *UserController) ResendCredential(c *gin.Context) {
	userVal, _ := c.Get("user")
	actor := userVal.(models.User)

	id, ok := idParam(c, "id", publicid.TypeUser)
	if !ok {
		return
	}

	emailedTo, err := ctrl.authService.ResendCredential(c.Request.Context(), actor, id)
	if err != nil {
		// These are operator-facing preconditions (no email / no key / nothing to
		// send), not server faults — report them as 400.
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"emailed_to": emailedTo})
}

// GetPasswordEvents handles GET /api/v1/users/:id/password-events (Admin only).
func (ctrl *UserController) GetPasswordEvents(c *gin.Context) {
	userVal, _ := c.Get("user")
	actor := userVal.(models.User)

	id, ok := idParam(c, "id", publicid.TypeUser)
	if !ok {
		return
	}

	keyConfigured, events, err := ctrl.authService.GetPasswordEventsView(c.Request.Context(), actor, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"key_configured": keyConfigured, "events": events})
}

// AuthMiddleware extracts the JWT and validates the user
func AuthMiddleware(authService *services.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header required"})
			c.Abort()
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid auth header format"})
			c.Abort()
			return
		}

		// AuthenticateRequest also verifies the token_version against the DB, so a
		// token minted before a password change/reset is rejected immediately.
		user, err := authService.AuthenticateRequest(c.Request.Context(), parts[1])
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			c.Abort()
			return
		}

		// Store user in context
		c.Set("user", user)
		c.Next()
	}
}

// RoleMiddleware checks if the user has the required role
func RoleMiddleware(roles ...models.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		userVal, exists := c.Get("user")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "User context not found"})
			c.Abort()
			return
		}

		user, ok := userVal.(models.User)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid user context"})
			c.Abort()
			return
		}

		allowed := false
		for _, role := range roles {
			if user.Role == role {
				allowed = true
				break
			}
		}

		if !allowed {
			c.JSON(http.StatusForbidden, gin.H{"error": "Access denied for this role"})
			c.Abort()
			return
		}

		c.Next()
	}
}
