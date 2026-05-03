# API_SPEC.md

## Base URL
`http://localhost:80/api/v1` (Proxied via Nginx)

## Authentication
Most endpoints require a JWT token in the header:
`Authorization: Bearer <your_jwt_token>`

## Public Endpoints

### 1. Login
Returns a JWT token and user info.
- **URL:** `/login`
- **Method:** `POST`
- **Payload:** `{"username": "...", "password": "..."}`
- **Response:** `{"token": "...", "user": {"id": 1, "username": "admin", "role": "ADMIN", ...}}`

## Protected Endpoints

### 2. Change Password (All Roles)
Updates the current user's password.
- **URL:** `/users/password`
- **Method:** `POST`
- **Headers:** `Authorization: Bearer <token>`
- **Payload:** `{"old_password": "...", "new_password": "..."}`

### 3. Submit Answer (Student Only)
Validates a student's SQL submission.
- **URL:** `/quiz/submit`
- **Method:** `POST`
- **Headers:** `Authorization: Bearer <token>`
- **Payload:** `{"student_id": "...", "question_number": 1, "sql_text": "..."}`

### 4. Get Available Exams (Student Only)
Lists exams the student is enrolled in.
- **URL:** `/quiz/exams`
- **Method:** `GET`
- **Headers:** `Authorization: Bearer <token>`

### 5. Get Exam Questions (Student Only)
Fetches questions for a specific exam.
- **URL:** `/quiz/exams/:id/questions`
- **Method:** `GET`
- **Headers:** `Authorization: Bearer <token>`

### 6. Create User (Admin/Lecturer)
Generates a new account.
- **URL:** `/users`
- **Method:** `POST`
- **Headers:** `Authorization: Bearer <token>`
- **Payload:** `{"username": "...", "password": "...", "role": "STUDENT"}`

### 7. Bulk Create Users (Admin/Lecturer)
Generates multiple accounts in one go.
- **URL:** `/users/bulk`
- **Method:** `POST`
- **Headers:** `Authorization: Bearer <token>`
- **Payload:** `{"users": [{"username": "...", "password": "...", "role": "STUDENT"}, ...]}`

### 8. Lecturer: Upsert Question
- **URL:** `/lecturer/question`
- **Method:** `POST`
- **Headers:** `Authorization: Bearer <token>`
- **Payload:**
```json
{
  "id": 1,
  "exam_id": 1,
  "target_table": "users",
  "correct_sql": "SELECT ...",
  "type": "QUERY",
  "points": 10,
  "strict_mode": {
    "ordering": true,
    "attributes": true,
    "record_order": true
  }
}
```

### 9. Lecturer: Import Schema
Initializes the exam database with a SQL script.
- **URL:** `/lecturer/import-schema`
- **Method:** `POST`
- **Headers:** `Authorization: Bearer <token>`
- **Payload:** `{"sql": "CREATE TABLE ..."}`

### 10. Lecturer: List/Create Exams
- **URL:** `/lecturer/exams`
- **Method:** `GET`, `POST`
- **Headers:** `Authorization: Bearer <token>`
- **POST Payload:** `{"name": "Midterm Exam", "description": "...", "is_active": false}`

### 11. Lecturer: Activate Exam
- **URL:** `/lecturer/exams/:id/activate`
- **Method:** `PUT`
- **Headers:** `Authorization: Bearer <token>`
- **Payload:** `{"is_active": true}`

### 12. Lecturer: Assign Students
- **URL:** `/lecturer/exams/:id/students`
- **Method:** `POST`
- **Headers:** `Authorization: Bearer <token>`
- **Payload:** `{"student_ids": [1, 2, 3]}`
