package repository

import (
	"context"
	"fmt"
)

// ─── Exam Status Probe ────────────────────────────────────────────────────────

func (r *PostgresRepository) IsAnyExamActive(ctx context.Context) (bool, error) {
	var active bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.examinations WHERE is_active = true)`).Scan(&active)
	return active, err
}

// IsPracticeFrozen reports whether practice should be blocked right now: true
// only when at least one ACTIVE examination has its block_practice_during_exam
// toggle on. With the toggle off (the default), practice stays live during
// exams — the payoff of giving each practice category its own schema.
func (r *PostgresRepository) IsPracticeFrozen(ctx context.Context) (bool, error) {
	var frozen bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.examinations WHERE is_active = true AND block_practice_during_exam = true)`).Scan(&frozen)
	return frozen, err
}

// SetExamPracticeFreeze flips the per-exam practice-freeze toggle.
func (r *PostgresRepository) SetExamPracticeFreeze(ctx context.Context, examID int, block bool) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE app.examinations SET block_practice_during_exam = $1 WHERE id = $2`, block, examID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("examination %d not found", examID)
	}
	return nil
}
