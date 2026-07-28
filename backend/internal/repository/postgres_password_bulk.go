package repository

// postgres_password_bulk.go implements the read/write primitives behind the
// admin bulk password-reset endpoint (ARCH-031). It is a NEW file, kept
// disjoint from postgres_users.go by design (ARCH-035 (3) file-ownership
// map), so the two can be edited in parallel without conflict.
//
// ResolveBulkResetTargets and NonStudentUserIDs are pure reads: neither one
// ever changes a password. ApplyPasswordReset is the only writer, and it
// performs the hash update, the token_version bump, and the (optional)
// encrypted audit-event insert in ONE transaction per student (ARCH-031
// (3)/(5)): a partial failure can never leave a student whose password
// changed but whose old sessions still work (or vice versa), and a failure
// for one student never touches any other student in the same batch.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// ResolveBulkResetTargets returns up to `limit` STUDENT rows (ordered by id
// ascending, restricted to id > afterID) matching the given selection mode,
// plus `remaining`: the TOTAL count of rows matching the same predicates
// (id > afterID and the mode filter), computed WITHOUT the LIMIT. Because
// `remaining` is a total rather than a post-batch count, the caller (the
// bulk-reset controller) derives the API-level "how many are left after this
// batch" figure as remaining-len(targets).
//
// mode must be one of "ids", "all_students", "exam_roster"; every mode
// implicitly restricts to role = 'STUDENT' — this is the mechanical
// enforcement of ARCH-031/032's S2 hard rule that no bulk action can ever
// resolve an ADMIN or LECTURER row, regardless of which mode is used or what
// user_ids are supplied.
func (r *PostgresRepository) ResolveBulkResetTargets(ctx context.Context, mode string, userIDs []int, examID int, afterID int, limit int) (targets []models.BulkResetTarget, remaining int, err error) {
	from := "FROM app.system_users u"
	where := "WHERE u.role = 'STUDENT' AND u.id > $1"
	args := []interface{}{afterID}

	switch mode {
	case "all_students":
		// No additional predicate: every STUDENT row past the cursor is a target.
	case "exam_roster":
		from += " JOIN app.exam_students es ON es.student_id = u.id AND es.exam_id = $2"
		args = append(args, examID)
	case "ids":
		where += " AND u.id = ANY($2::int[])"
		args = append(args, userIDs)
	default:
		return nil, 0, fmt.Errorf("postgres_password_bulk: unknown mode %q", mode)
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) %s %s", from, where)
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&remaining); err != nil {
		return nil, 0, err
	}

	selectArgs := append(append([]interface{}{}, args...), limit)
	selectQuery := fmt.Sprintf(`
		SELECT u.id, u.username, COALESCE(u.student_number, ''), COALESCE(u.first_name, ''),
		       COALESCE(u.last_name, ''), COALESCE(u.sec_lec, ''), COALESCE(u.sec_lab, '')
		%s
		%s
		ORDER BY u.id ASC
		LIMIT $%d`, from, where, len(selectArgs))

	rows, err := r.pool.Query(ctx, selectQuery, selectArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	targets = []models.BulkResetTarget{}
	for rows.Next() {
		var t models.BulkResetTarget
		if err := rows.Scan(&t.UserID, &t.Username, &t.StudentNumber, &t.FirstName, &t.LastName, &t.SecLec, &t.SecLab); err != nil {
			return nil, 0, err
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	return targets, remaining, nil
}

// NonStudentUserIDs is used for mode=="ids" ONLY. It returns every id in
// userIDs that either does not exist at all or exists with a role other than
// STUDENT. A non-empty result means the controller must abort the WHOLE
// request before any write (ARCH-031 (3)/ARCH-032 S2): this is what makes
// "reset everyone including the admins" impossible through this endpoint —
// staff and admin accounts (including the actor's own) can only be reset via
// the existing single-user route.
func (r *PostgresRepository) NonStudentUserIDs(ctx context.Context, userIDs []int) ([]int, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}

	query := `
		SELECT id FROM (
			SELECT id FROM unnest($1::int[]) AS t(id)
			EXCEPT
			SELECT id FROM app.system_users WHERE id = ANY($1::int[]) AND role = 'STUDENT'
		) AS non_students
		ORDER BY id`

	rows, err := r.pool.Query(ctx, query, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []int{}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ApplyPasswordReset performs the ONE transaction behind a single student's
// bulk reset: the password hash update, the token_version bump (which
// immediately invalidates every previously-issued JWT for that user), and,
// when enc is non-nil, the encrypted admin_reset audit-event insert. All
// three land together or none do — per-student atomicity, deliberately NOT
// extended across students in a batch (see AuthService.BulkResetPasswords).
// When enc is nil (no PASSWORD_LOG_KEY configured), the audit insert is
// skipped entirely and only the hash+token_version effects are committed.
func (r *PostgresRepository) ApplyPasswordReset(ctx context.Context, userID int, passwordHash string, changedBy int, source string, enc []byte, nonce []byte) error {
	return r.WithTransaction(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE app.system_users SET password_hash = $1 WHERE id = $2`,
			passwordHash, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE app.system_users SET token_version = token_version + 1 WHERE id = $1`,
			userID); err != nil {
			return err
		}
		if enc != nil {
			if _, err := tx.Exec(ctx,
				`INSERT INTO app.password_events (user_id, changed_by, source, new_password_enc, nonce)
				 VALUES ($1, $2, $3, $4, $5)`,
				userID, changedBy, source, enc, nonce); err != nil {
				return err
			}
		}
		return nil
	})
}
