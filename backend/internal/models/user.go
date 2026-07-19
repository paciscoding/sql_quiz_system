package models

import "time"

type Role string

const (
	RoleAdmin    Role = "ADMIN"
	RoleLecturer Role = "LECTURER"
	RoleStudent  Role = "STUDENT"
)

type User struct {
	ID            UserID    `json:"id"`
	Username      string    `json:"username"`
	Password      string    `json:"-"`
	Role          Role      `json:"role"`
	CreatedBy     *UserID   `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	Email         *string   `json:"email,omitempty"`
	StudentNumber *string   `json:"student_number,omitempty"`
	StudentIDExt  *string   `json:"student_id_ext,omitempty"`
	SecLec        *string   `json:"sec_lec,omitempty"`
	SecLab        *string   `json:"sec_lab,omitempty"`
	FirstName     *string   `json:"first_name,omitempty"`
	LastName      *string   `json:"last_name,omitempty"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Token string `json:"token"`
	User  User   `json:"user"`
}

type CreateUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     Role   `json:"role"`
	Email    string `json:"email"`
}

type StudentImportRow struct {
	StudentNumber string `json:"student_number"`
	StudentIDExt  string `json:"student_id_ext"`
	SecLec        string `json:"sec_lec"`
	SecLab        string `json:"sec_lab"`
	FirstName     string `json:"first_name"`
	LastName      string `json:"last_name"`
	Email         string `json:"email"`
	PasswordHash  string `json:"password_hash"`
}

type BulkCreateUserRequest struct {
	Users []CreateUserRequest `json:"users"`
}

type ChangePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// ForgotPasswordRequest is the body for the public POST /forgot-password
// endpoint (step 1: send a verification code). Only the username is needed;
// the response is always generic.
type ForgotPasswordRequest struct {
	Username string `json:"username"`
}

// ForgotPasswordVerifyRequest is the body for the public POST
// /forgot-password/verify endpoint (step 2: submit the emailed code).
type ForgotPasswordVerifyRequest struct {
	Username string `json:"username"`
	OTP      string `json:"otp"`
}
