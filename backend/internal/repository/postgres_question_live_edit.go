package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// LiveEditableField is a question field a lecturer is allowed to change while
// the owning examination is running.
//
// The list is deliberately one entry long. Everything else on app.questions is
// either read by grading (correct_sql, target_table, question_type, dml_kind,
// seed_sql, the strict_* flags, dual_schema) or decides what a submission is
// worth (points), and changing any of those mid-exam would mean the students
// who submitted before the edit were graded against a different question than
// the ones who submitted after it — with no way to tell the two groups apart
// afterwards. Those edits stay refused until the exam is deactivated, which is
// what CreateQuestion's ErrExamActive guard already enforces on the normal
// save path.
//
// Wording is safe to change because grading never reads it: it is the prose
// the student is asked to satisfy, not part of the answer key.
const LiveEditableField = "requirement"

// ErrLiveEditNotAllowed is returned by UpdateQuestionRequirementLive when the
// caller tried to change something other than the question's wording while the
// examination is active. The error text names the offending fields.
var ErrLiveEditNotAllowed = fmt.Errorf("field cannot be changed while the examination is active")

// UpdateQuestionRequirementLive updates ONLY app.questions.requirement for one
// question of one exam.
//
// This is intentionally the narrowest possible write. It does not re-run the
// Phase-0 capture (the answer key is unchanged by definition, so the captured
// expected_result stays valid), and it does not read or write app.submissions —
// no score, no is_correct, and no validation_details is touched, so every grade
// already recorded stays exactly as it was.
//
// The exam_id is part of the WHERE clause so a lecturer cannot reach a question
// belonging to a different examination by guessing its id.
func (r *PostgresRepository) UpdateQuestionRequirementLive(ctx context.Context, examID, questionID int, requirement string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE app.questions SET requirement = $1, updated_at = NOW() WHERE id = $2 AND exam_id = $3`,
		requirement, questionID, examID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
