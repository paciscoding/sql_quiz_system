package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

func (r *PostgresRepository) CreateExamination(ctx context.Context, exam models.Examination) (int, error) {
	query := `INSERT INTO app.examinations (title, is_active, created_by, introduction) VALUES ($1, $2, $3, $4) RETURNING id`
	var id int
	err := r.pool.QueryRow(ctx, query, exam.Title, exam.IsActive, exam.CreatedBy, exam.Introduction).Scan(&id)
	return id, err
}

// UpdateExaminationIntroduction persists a new description text for an exam.
// Returns an error if the exam id does not exist.
func (r *PostgresRepository) UpdateExaminationIntroduction(ctx context.Context, id int, introduction string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE app.examinations SET introduction = $1 WHERE id = $2`,
		introduction, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *PostgresRepository) SetExaminationActive(ctx context.Context, id int, active bool, scheduledStartAt *time.Time, scheduledEndAt *time.Time) error {
	if !active {
		return r.ArchiveExamination(ctx, id)
	}

	// Validate schedule timestamps
	if scheduledStartAt != nil && scheduledEndAt != nil && !scheduledEndAt.After(*scheduledStartAt) {
		return fmt.Errorf("scheduled_end_at must be after scheduled_start_at")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM app.questions WHERE exam_id = $1", id).Scan(&total); err != nil {
		return err
	}
	if total == 0 {
		return fmt.Errorf("%w: define at least one question before activating", ErrEmptyExam)
	}

	var notCaptured int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM app.questions WHERE exam_id = $1 AND expected_result_status != 'captured'", id).Scan(&notCaptured); err != nil {
		return err
	}
	if notCaptured > 0 {
		return fmt.Errorf("%w: %d question(s) have not successfully captured expected results", ErrCaptureNotReady, notCaptured)
	}

	// Instant mode: if no scheduled_start_at provided, set to now()
	if scheduledStartAt == nil {
		now := time.Now().UTC()
		scheduledStartAt = &now
	}

	query := `UPDATE app.examinations SET is_active = TRUE, activated_at = NOW(), archived_at = NULL, scheduled_start_at = $2, scheduled_end_at = $3 WHERE id = $1`
	if _, err := r.pool.Exec(ctx, query, id, scheduledStartAt, scheduledEndAt); err != nil {
		return err
	}

	// Lazy sandbox prep: only clone if the window is currently open and the exam has DML questions.
	examForState := models.Examination{IsActive: true, ScheduledStartAt: scheduledStartAt, ScheduledEndAt: scheduledEndAt}
	if state := ExaminationEffectiveState(examForState); state.ActiveNow {
		hasDML, err := r.examHasDMLQuestions(ctx, id)
		if err != nil {
			return err
		}
		if hasDML {
			if err := r.InitializeExamSandbox(ctx, id); err != nil {
				return fmt.Errorf("failed to initialize exam sandbox: %w", err)
			}
		}
	}
	return nil
}

// EnsureExamSandboxReady checks whether the exam sandbox already exists; if not,
// and the exam has DML questions, it calls InitializeExamSandbox.
// Used by GetExamQuestions to lazily prep the sandbox on first access.
func (r *PostgresRepository) EnsureExamSandboxReady(ctx context.Context, examID int) error {
	var createdAt *time.Time
	err := r.pool.QueryRow(ctx,
		"SELECT created_at FROM app.exam_sandboxes WHERE exam_id = $1 AND dropped_at IS NULL",
		examID).Scan(&createdAt)
	if err == nil && createdAt != nil {
		// Sandbox already exists
		return nil
	}
	// Check if the exam has DML questions
	hasDML, err := r.examHasDMLQuestions(ctx, examID)
	if err != nil {
		return err
	}
	if !hasDML {
		return nil
	}
	return r.InitializeExamSandbox(ctx, examID)
}

// SetScheduledEndAt updates only the scheduled_end_at timestamp for a given exam.
// Used by the "deactivate at a time" flow to set a future end without dropping the sandbox.
func (r *PostgresRepository) SetScheduledEndAt(ctx context.Context, examID int, scheduledEndAt *time.Time) error {
	_, err := r.pool.Exec(ctx,
		"UPDATE app.examinations SET scheduled_end_at = $1 WHERE id = $2",
		scheduledEndAt, examID)
	return err
}

func (r *PostgresRepository) ArchiveExamination(ctx context.Context, id int) error {
	return r.WithTransaction(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE app.examinations SET is_active = FALSE, archived_at = NOW() WHERE id = $1", id)
		if err != nil {
			return err
		}

		var schema string
		err = tx.QueryRow(ctx,
			"SELECT schema_name FROM app.exam_sandboxes WHERE exam_id = $1 AND dropped_at IS NULL",
			id).Scan(&schema)
		if err != nil {
			if err == pgx.ErrNoRows {
				// Pure-SELECT exam: no sandbox was ever created, nothing to drop.
				return nil
			}
			return err
		}

		safeSchema := pgx.Identifier{schema}.Sanitize()
		if _, err := tx.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safeSchema)); err != nil {
			fmt.Printf("Warning: failed to drop exam sandbox %s: %v\n", schema, err)
		}

		_, err = tx.Exec(ctx, "UPDATE app.exam_sandboxes SET dropped_at = NOW() WHERE exam_id = $1", id)
		return err
	})
}

func (r *PostgresRepository) AddStudentsToExam(ctx context.Context, examID int, studentIDs []int) error {
	exam, err := r.GetExamByID(ctx, examID)
	if err == nil && ExaminationEffectiveState(exam).ActiveNow {
		return fmt.Errorf("%w: cannot assign students to an active examination", ErrExamActive)
	}

	return r.WithTransaction(ctx, func(tx pgx.Tx) error {
		for _, studentID := range studentIDs {
			_, err := tx.Exec(ctx,
				"INSERT INTO app.exam_students (exam_id, student_id, assigned_at) VALUES ($1, $2, NOW()) ON CONFLICT DO NOTHING",
				examID, studentID)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *PostgresRepository) IsStudentEnrolledInExam(ctx context.Context, studentID, examID int) (bool, error) {
	var count int
	query := `SELECT COUNT(*) FROM app.exam_students WHERE exam_id = $1 AND student_id = $2`
	err := r.pool.QueryRow(ctx, query, examID, studentID).Scan(&count)
	return count > 0, err
}

func (r *PostgresRepository) GetExamByID(ctx context.Context, id int) (models.Examination, error) {
	query := `SELECT id, title, is_active, created_by, created_at, scheduled_start_at, scheduled_end_at, activated_at, archived_at, block_practice_during_exam, introduction FROM app.examinations WHERE id = $1`
	var e models.Examination
	err := r.pool.QueryRow(ctx, query, id).Scan(&e.ID, &e.Title, &e.IsActive, &e.CreatedBy, &e.CreatedAt, &e.ScheduledStartAt, &e.ScheduledEndAt, &e.ActivatedAt, &e.ArchivedAt, &e.BlockPracticeDuringExam, &e.Introduction)
	return e, err
}

func (r *PostgresRepository) GetAllExaminations(ctx context.Context) ([]models.Examination, error) {
	query := `SELECT id, title, is_active, created_by, created_at, scheduled_start_at, scheduled_end_at, activated_at, archived_at, block_practice_during_exam, introduction FROM app.examinations ORDER BY created_at DESC`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	exams := []models.Examination{}
	for rows.Next() {
		var e models.Examination
		err := rows.Scan(&e.ID, &e.Title, &e.IsActive, &e.CreatedBy, &e.CreatedAt, &e.ScheduledStartAt, &e.ScheduledEndAt, &e.ActivatedAt, &e.ArchivedAt, &e.BlockPracticeDuringExam, &e.Introduction)
		if err != nil {
			return nil, err
		}
		exams = append(exams, e)
	}
	return exams, nil
}

// GetAllExaminationsWithCounts returns all examinations enriched with three
// readiness aggregates derived from app.questions:
//   - question_count  — total number of questions
//   - total_points    — SUM of question points (0 when no questions)
//   - uncaptured_count — number of questions where expected_result_status <> 'captured'
//
// Only the lecturer list path calls this; the student paths use GetStudentExams /
// GetStudentEnrolledExams which remain unchanged.
func (r *PostgresRepository) GetAllExaminationsWithCounts(ctx context.Context) ([]models.Examination, error) {
	query := `
		SELECT e.id, e.title, e.is_active, e.created_by, e.created_at,
		       e.scheduled_start_at, e.scheduled_end_at, e.activated_at, e.archived_at,
		       e.block_practice_during_exam, e.introduction,
		       COALESCE(e.curriculum_schema, 'public')                               AS curriculum_schema,
		       COUNT(q.id)                                                          AS question_count,
		       COALESCE(SUM(q.points), 0.0)                                         AS total_points,
		       COUNT(q.id) FILTER (WHERE q.expected_result_status <> 'captured')    AS uncaptured_count
		FROM app.examinations e
		LEFT JOIN app.questions q ON q.exam_id = e.id
		GROUP BY e.id
		ORDER BY e.created_at DESC`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	exams := []models.Examination{}
	for rows.Next() {
		var e models.Examination
		err := rows.Scan(
			&e.ID, &e.Title, &e.IsActive, &e.CreatedBy, &e.CreatedAt,
			&e.ScheduledStartAt, &e.ScheduledEndAt, &e.ActivatedAt, &e.ArchivedAt,
			&e.BlockPracticeDuringExam, &e.Introduction,
			&e.CurriculumSchema,
			&e.QuestionCount, &e.TotalPoints, &e.UncapturedCount,
		)
		if err != nil {
			return nil, err
		}
		exams = append(exams, e)
	}
	return exams, rows.Err()
}

// UpdateExaminationTitle renames an examination. Returns pgx.ErrNoRows when the
// exam id does not exist. Allowed regardless of the exam's active state.
func (r *PostgresRepository) UpdateExaminationTitle(ctx context.Context, id int, title string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE app.examinations SET title = $2 WHERE id = $1`, id, title)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// DeleteExamination permanently removes an examination and all of its dependents
// in a single transaction. FK-safe deletion order:
//  1. app.submissions
//  2. app.exam_students
//  3. app.examination_intro_images
//  4. app.questions
//  5. DROP exam sandbox schema (if present and not already dropped)
//  6. app.exam_sandboxes
//  7. app.examinations
//
// Returns pgx.ErrNoRows when the exam id does not exist.
func (r *PostgresRepository) DeleteExamination(ctx context.Context, id int) error {
	return r.WithTransaction(ctx, func(tx pgx.Tx) error {
		// Verify the exam exists before doing any deletes.
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM app.examinations WHERE id = $1)`, id,
		).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return pgx.ErrNoRows
		}

		// 1. Submissions
		if _, err := tx.Exec(ctx, `DELETE FROM app.submissions WHERE exam_id = $1`, id); err != nil {
			return err
		}

		// 2. Exam–student enrollments
		if _, err := tx.Exec(ctx, `DELETE FROM app.exam_students WHERE exam_id = $1`, id); err != nil {
			return err
		}

		// 3. Intro images
		if _, err := tx.Exec(ctx, `DELETE FROM app.examination_intro_images WHERE examination_id = $1`, id); err != nil {
			return err
		}

		// 4. Questions
		if _, err := tx.Exec(ctx, `DELETE FROM app.questions WHERE exam_id = $1`, id); err != nil {
			return err
		}

		// 5. Sandbox schema: look up the schema name (only live/non-dropped entries).
		var sandboxSchema string
		err := tx.QueryRow(ctx,
			`SELECT schema_name FROM app.exam_sandboxes WHERE exam_id = $1 AND dropped_at IS NULL`, id,
		).Scan(&sandboxSchema)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		if sandboxSchema != "" {
			safe := pgx.Identifier{sandboxSchema}.Sanitize()
			if _, err := tx.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safe)); err != nil {
				fmt.Printf("Warning: failed to drop exam sandbox %s during delete: %v\n", sandboxSchema, err)
			}
		}

		// 5b. Per-exam curriculum schema: drop only when it's not 'public' (never
		// drop 'public'; default value means no per-exam import happened).
		var curriculumSchema string
		csErr := tx.QueryRow(ctx,
			`SELECT COALESCE(NULLIF(curriculum_schema, ''), 'public')
			 FROM app.examinations WHERE id = $1`, id).Scan(&curriculumSchema)
		if csErr == nil && curriculumSchema != "" && curriculumSchema != "public" {
			safe := pgx.Identifier{curriculumSchema}.Sanitize()
			if _, err := tx.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safe)); err != nil {
				fmt.Printf("Warning: failed to drop exam curriculum schema %s during delete: %v\n", curriculumSchema, err)
			}
		}

		// 6. Sandbox bookkeeping row (covers both live and already-dropped entries)
		if _, err := tx.Exec(ctx, `DELETE FROM app.exam_sandboxes WHERE exam_id = $1`, id); err != nil {
			return err
		}

		// 7. The examination itself
		if _, err := tx.Exec(ctx, `DELETE FROM app.examinations WHERE id = $1`, id); err != nil {
			return err
		}

		return nil
	})
}

func (r *PostgresRepository) GetStudentExams(ctx context.Context, studentID int) ([]models.Examination, error) {
	query := `
		SELECT e.id, e.title, e.is_active, e.created_by, e.created_at, e.scheduled_start_at, e.scheduled_end_at, e.activated_at, e.archived_at, e.introduction
		FROM app.examinations e
		JOIN app.exam_students es ON e.id = es.exam_id
		WHERE es.student_id = $1 AND e.is_active = TRUE`

	rows, err := r.pool.Query(ctx, query, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	exams := []models.Examination{}
	for rows.Next() {
		var e models.Examination
		err := rows.Scan(&e.ID, &e.Title, &e.IsActive, &e.CreatedBy, &e.CreatedAt, &e.ScheduledStartAt, &e.ScheduledEndAt, &e.ActivatedAt, &e.ArchivedAt, &e.Introduction)
		if err != nil {
			return nil, err
		}
		exams = append(exams, e)
	}
	return exams, rows.Err()
}

// GetExamMaxScores returns a map of exam title -> sum of question points for all examinations.
// Used by the Results tab to show earned/max denominators.
func (r *PostgresRepository) GetExamMaxScores(ctx context.Context) (map[string]float64, error) {
	query := `
		SELECT e.title, COALESCE(SUM(q.points), 0.0)
		FROM app.examinations e
		LEFT JOIN app.questions q ON q.exam_id = e.id
		GROUP BY e.id, e.title
		ORDER BY e.title`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]float64)
	for rows.Next() {
		var title string
		var maxPts float64
		if err := rows.Scan(&title, &maxPts); err != nil {
			return nil, err
		}
		result[title] = maxPts
	}
	return result, rows.Err()
}

// GetStudentEnrolledExams returns ALL exams a student is enrolled in, regardless of is_active status.
// Used by the waiting-page flow so students can select an exam before it opens.
func (r *PostgresRepository) GetStudentEnrolledExams(ctx context.Context, studentID int) ([]models.Examination, error) {
	query := `
		SELECT e.id, e.title, e.is_active, e.created_by, e.created_at, e.scheduled_start_at, e.scheduled_end_at, e.activated_at, e.archived_at, e.introduction
		FROM app.examinations e
		JOIN app.exam_students es ON e.id = es.exam_id
		WHERE es.student_id = $1
		ORDER BY e.created_at DESC`

	rows, err := r.pool.Query(ctx, query, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	exams := []models.Examination{}
	for rows.Next() {
		var e models.Examination
		err := rows.Scan(&e.ID, &e.Title, &e.IsActive, &e.CreatedBy, &e.CreatedAt, &e.ScheduledStartAt, &e.ScheduledEndAt, &e.ActivatedAt, &e.ArchivedAt, &e.Introduction)
		if err != nil {
			return nil, err
		}
		exams = append(exams, e)
	}
	return exams, rows.Err()
}

// GetExamRoster returns all students enrolled in an exam with their profile details.
func (r *PostgresRepository) GetExamRoster(ctx context.Context, examID int) ([]models.RosterStudent, error) {
	query := `
		SELECT su.id, su.username,
			COALESCE(su.student_number, ''),
			COALESCE(su.first_name, ''),
			COALESCE(su.last_name, ''),
			COALESCE(su.sec_lec, ''),
			COALESCE(su.sec_lab, ''),
			es.assigned_at
		FROM app.exam_students es
		JOIN app.system_users su ON es.student_id = su.id
		WHERE es.exam_id = $1
		ORDER BY su.student_number ASC NULLS LAST, su.username ASC`

	rows, err := r.pool.Query(ctx, query, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roster []models.RosterStudent
	for rows.Next() {
		var s models.RosterStudent
		if err := rows.Scan(&s.StudentID, &s.Username, &s.StudentNumber, &s.FirstName, &s.LastName, &s.SecLec, &s.SecLab, &s.AssignedAt); err != nil {
			return nil, err
		}
		roster = append(roster, s)
	}
	if roster == nil {
		roster = []models.RosterStudent{}
	}
	return roster, rows.Err()
}

// RemoveStudentFromExam removes a student from an exam's roster.
// Returns ErrExamActive if the exam is effectively active.
func (r *PostgresRepository) RemoveStudentFromExam(ctx context.Context, examID, studentID int) error {
	exam, err := r.GetExamByID(ctx, examID)
	if err != nil {
		return err
	}
	if ExaminationEffectiveState(exam).ActiveNow {
		return fmt.Errorf("%w: cannot remove student while exam is effectively active", ErrExamActive)
	}
	_, err = r.pool.Exec(ctx,
		"DELETE FROM app.exam_students WHERE exam_id = $1 AND student_id = $2",
		examID, studentID)
	return err
}
