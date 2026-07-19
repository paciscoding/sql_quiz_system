package repository

import (
	"context"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// EnsureExportState seeds the singleton export_state row on first boot. It is
// idempotent: once the row exists, the defaultEnabled argument is ignored, so a
// later admin UI toggle is never clobbered by the env-derived default.
func (r *PostgresRepository) EnsureExportState(ctx context.Context, defaultEnabled bool) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO app.export_state (id, enabled, last_export_at, updated_at)
		 VALUES (1, $1, now(), now())
		 ON CONFLICT (id) DO NOTHING`,
		defaultEnabled)
	return err
}

// GetExportState reads the singleton row.
func (r *PostgresRepository) GetExportState(ctx context.Context) (models.ExportState, error) {
	var st models.ExportState
	err := r.pool.QueryRow(ctx,
		`SELECT enabled, last_export_at, updated_at FROM app.export_state WHERE id = 1`).
		Scan(&st.Enabled, &st.LastExportAt, &st.UpdatedAt)
	return st, err
}

// SetExportEnabled flips the admin toggle and bumps updated_at.
func (r *PostgresRepository) SetExportEnabled(ctx context.Context, enabled bool) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE app.export_state SET enabled = $1, updated_at = now() WHERE id = 1`,
		enabled)
	return err
}

// AdvanceExportWatermark moves last_export_at forward after a successful (or
// confirmed-empty) export cycle.
func (r *PostgresRepository) AdvanceExportWatermark(ctx context.Context, t time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE app.export_state SET last_export_at = $1, updated_at = now() WHERE id = 1`,
		t)
	return err
}

// ExportSubmissions returns exam submissions with submitted_at in (from, to],
// joined to the student's username. FK columns are nullable, so they scan into
// pointers; validation_details is emitted as its raw JSON text.
func (r *PostgresRepository) ExportSubmissions(ctx context.Context, from, to time.Time) ([]models.SubmissionExportRow, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT s.id, s.exam_id, s.student_id, COALESCE(u.username, ''), s.question_id,
		        s.is_correct, s.score, s.submitted_at, s.sql_text,
		        COALESCE(s.validation_details::text, '')
		 FROM app.submissions s
		 LEFT JOIN app.system_users u ON u.id = s.student_id
		 WHERE s.submitted_at > $1 AND s.submitted_at <= $2
		 ORDER BY s.submitted_at ASC, s.id ASC`,
		from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.SubmissionExportRow
	for rows.Next() {
		var row models.SubmissionExportRow
		if err := rows.Scan(&row.ID, &row.ExamID, &row.StudentID, &row.Username, &row.QuestionID,
			&row.IsCorrect, &row.Score, &row.SubmittedAt, &row.SQLText, &row.ValidationDetails); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ExportPracticeAttempts returns practice attempts with submitted_at in
// (from, to], joined to the student's username.
func (r *PostgresRepository) ExportPracticeAttempts(ctx context.Context, from, to time.Time) ([]models.PracticeAttemptExportRow, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT a.id, a.student_id, COALESCE(u.username, ''), a.practice_question_id,
		        COALESCE(pc.name, ''), COALESCE(pq.requirement, ''),
		        a.is_correct, a.submitted_at, a.sql_text,
		        COALESCE(a.validation_details::text, '')
		 FROM app.practice_attempts a
		 LEFT JOIN app.system_users u ON u.id = a.student_id
		 LEFT JOIN app.practice_questions pq ON pq.id = a.practice_question_id
		 LEFT JOIN app.practice_categories pc ON pc.id = pq.category_id
		 WHERE a.submitted_at > $1 AND a.submitted_at <= $2
		 ORDER BY a.submitted_at ASC, a.id ASC`,
		from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.PracticeAttemptExportRow
	for rows.Next() {
		var row models.PracticeAttemptExportRow
		if err := rows.Scan(&row.ID, &row.StudentID, &row.Username, &row.PracticeQuestionID,
			&row.CategoryName, &row.QuestionRequirement,
			&row.IsCorrect, &row.SubmittedAt, &row.SQLText, &row.ValidationDetails); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
