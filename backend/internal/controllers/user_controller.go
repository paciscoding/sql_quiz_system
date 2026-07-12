package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

type UserController struct {
	authService    *services.AuthService
	studentService *services.StudentImportService
}

func NewUserController(authService *services.AuthService, studentService *services.StudentImportService) *UserController {
	return &UserController{authService: authService, studentService: studentService}
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
	idStr := c.Param("id")
	var id int
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	var u models.User
	if err := c.ShouldBindJSON(&u); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	u.ID = id

	if err := ctrl.authService.UpdateUser(c.Request.Context(), u); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "User updated successfully"})
}

func (ctrl *UserController) DeleteUser(c *gin.Context) {
	idStr := c.Param("id")
	var id int
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
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

	c.JSON(http.StatusCreated, gin.H{"id": id, "generated_password": password})
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

	outcome, id, password, err := ctrl.studentService.CreateSingle(c.Request.Context(), creator.ID, services.StudentUpsertInput{
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

	resp := gin.H{"id": id, "created": outcome == "created"}
	// Only a newly-inserted row has a fresh password (the upsert preserves an
	// existing student's password on conflict). Log the event + email + reveal it
	// only in that case.
	if outcome == "created" {
		ctrl.authService.LogStudentCreate(c.Request.Context(), creator, id, password, req.StudentIDExt, req.Email)
		resp["generated_password"] = password
	}

	c.JSON(http.StatusCreated, resp)
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

	idStr := c.Param("id")
	var id int
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}

	password, err := ctrl.authService.ResetPassword(c.Request.Context(), actor, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"generated_password": password})
}

// ResendCredential handles POST /api/v1/users/:id/resend-credential (Admin only).
// Re-emails the user's current credential to their stored address.
func (ctrl *UserController) ResendCredential(c *gin.Context) {
	userVal, _ := c.Get("user")
	actor := userVal.(models.User)

	idStr := c.Param("id")
	var id int
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
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

	idStr := c.Param("id")
	var id int
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
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
