package models

import "time"

type Role string

const (
	RoleAdmin    Role = "ADMIN"
	RoleLecturer Role = "LECTURER"
	RoleStudent  Role = "STUDENT"
)

type User struct {
	ID            int       `json:"id"`
	Username      string    `json:"username"`
	Password      string    `json:"-"`
	Role          Role      `json:"role"`
	CreatedBy     *int      `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
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
}

type StudentImportRow struct {
	StudentNumber string `json:"student_number"`
	StudentIDExt  string `json:"student_id_ext"`
	SecLec        string `json:"sec_lec"`
	SecLab        string `json:"sec_lab"`
	FirstName     string `json:"first_name"`
	LastName      string `json:"last_name"`
	PasswordHash  string `json:"password_hash"`
}

type BulkCreateUserRequest struct {
	Users []CreateUserRequest `json:"users"`
}

type ChangePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}
