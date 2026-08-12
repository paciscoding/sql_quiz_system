package repository

import (
	"context"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// CreateAnnouncement inserts a new announcement for examID, authored by
// lecturerID, and returns the full row (with lecturer_name resolved) as sent
// back to the caller in the 201 response.
func (r *PostgresRepository) CreateAnnouncement(ctx context.Context, examID int, lecturerID int, message string) (models.ExamAnnouncement, error) {
	var a models.ExamAnnouncement
	query := `
		INSERT INTO app.exam_announcements (exam_id, lecturer_id, message)
		VALUES ($1, $2, $3)
		RETURNING id, exam_id, sent_at`
	if err := r.pool.QueryRow(ctx, query, examID, lecturerID, message).Scan(&a.ID, &a.ExamID, &a.SentAt); err != nil {
		return models.ExamAnnouncement{}, err
	}
	a.Message = message
	name, err := r.lecturerNameByID(ctx, lecturerID)
	if err != nil {
		return models.ExamAnnouncement{}, err
	}
	a.LecturerName = name
	return a, nil
}

// lecturerNameByID resolves the display name shown for an announcement's
// author, matching the username column GetExamRoster uses for identity.
func (r *PostgresRepository) lecturerNameByID(ctx context.Context, lecturerID int) (string, error) {
	var name string
	if err := r.pool.QueryRow(ctx, `SELECT username FROM app.system_users WHERE id = $1`, lecturerID).Scan(&name); err != nil {
		return "", err
	}
	return name, nil
}

// ListAnnouncementsForExam returns every announcement ever sent for examID,
// newest first. Staff-only: unlike ListActiveAnnouncementsForStudent, this is
// NOT filtered by activated_at.
func (r *PostgresRepository) ListAnnouncementsForExam(ctx context.Context, examID int) ([]models.ExamAnnouncement, error) {
	query := `
		SELECT ea.id, ea.exam_id, su.username, ea.message, ea.sent_at
		FROM app.exam_announcements ea
		JOIN app.system_users su ON ea.lecturer_id = su.id
		WHERE ea.exam_id = $1
		ORDER BY ea.sent_at DESC`
	rows, err := r.pool.Query(ctx, query, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	announcements := []models.ExamAnnouncement{}
	for rows.Next() {
		var a models.ExamAnnouncement
		if err := rows.Scan(&a.ID, &a.ExamID, &a.LecturerName, &a.Message, &a.SentAt); err != nil {
			return nil, err
		}
		announcements = append(announcements, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return announcements, nil
}

// ListActiveAnnouncementsForStudent returns announcements for examID sent at
// or after the exam's current activated_at, newest first. This is the sole
// enforcement of "announcements are cleared when the exam is re-activated" —
// no row is ever deleted or mutated, older announcements simply fall outside
// the activated_at cutoff once the exam is reactivated.
func (r *PostgresRepository) ListActiveAnnouncementsForStudent(ctx context.Context, examID int) ([]models.StudentExamAnnouncement, error) {
	query := `
		SELECT ea.id, su.username, ea.message, ea.sent_at
		FROM app.exam_announcements ea
		JOIN app.system_users su ON ea.lecturer_id = su.id
		JOIN app.examinations e ON e.id = ea.exam_id
		WHERE ea.exam_id = $1
		  AND e.activated_at IS NOT NULL
		  AND ea.sent_at >= e.activated_at
		ORDER BY ea.sent_at DESC`
	rows, err := r.pool.Query(ctx, query, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	announcements := []models.StudentExamAnnouncement{}
	for rows.Next() {
		var a models.StudentExamAnnouncement
		if err := rows.Scan(&a.ID, &a.LecturerName, &a.Message, &a.SentAt); err != nil {
			return nil, err
		}
		announcements = append(announcements, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return announcements, nil
}

// DeleteAnnouncement deletes the announcement identified by announcementID,
// scoped to examID so a lecturer can never delete another exam's row by
// guessing an id. Returns the number of rows deleted (0 means not found).
func (r *PostgresRepository) DeleteAnnouncement(ctx context.Context, examID int, announcementID int) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM app.exam_announcements WHERE id = $1 AND exam_id = $2`, announcementID, examID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
