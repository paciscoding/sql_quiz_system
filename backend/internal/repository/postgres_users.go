package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// emailUniqueIndex is the name of the partial unique index on system_users.email
// (see migrateUniqueEmailIndex). Used to recognize email-collision errors.
const emailUniqueIndex = "idx_system_users_email"

// IsEmailConflict reports whether err is a Postgres unique-violation raised by
// the account-email uniqueness index, so callers can surface a clean "email
// already in use" message instead of a raw driver error.
func IsEmailConflict(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505" && pgErr.ConstraintName == emailUniqueIndex
	}
	return false
}

func (r *PostgresRepository) CreateUser(ctx context.Context, u models.User) (int, error) {
	query := `INSERT INTO app.system_users (username, password_hash, role, created_by, email) VALUES ($1, $2, $3, $4, $5) RETURNING id`
	var id int
	err := r.pool.QueryRow(ctx, query, u.Username, u.Password, string(u.Role), u.CreatedBy, u.Email).Scan(&id)
	return id, err
}

func (r *PostgresRepository) UpdateUserPassword(ctx context.Context, id int, hashedPassword string) error {
	query := `UPDATE app.system_users SET password_hash = $1 WHERE id = $2`
	_, err := r.pool.Exec(ctx, query, hashedPassword, id)
	return err
}

func (r *PostgresRepository) GetUserByID(ctx context.Context, id int) (models.User, error) {
	query := `SELECT id, username, password_hash, role, created_by, created_at, email FROM app.system_users WHERE id = $1`
	var u models.User
	var role string
	err := r.pool.QueryRow(ctx, query, id).Scan(&u.ID, &u.Username, &u.Password, &role, &u.CreatedBy, &u.CreatedAt, &u.Email)
	u.Role = models.Role(role)
	return u, err
}

func (r *PostgresRepository) GetUserByUsername(ctx context.Context, username string) (models.User, error) {
	query := `SELECT id, username, password_hash, role, created_by, created_at, email FROM app.system_users WHERE username = $1`
	var u models.User
	var role string
	err := r.pool.QueryRow(ctx, query, username).Scan(&u.ID, &u.Username, &u.Password, &role, &u.CreatedBy, &u.CreatedAt, &u.Email)
	u.Role = models.Role(role)
	return u, err
}

func (r *PostgresRepository) GetAllUsers(ctx context.Context) ([]models.User, error) {
	query := `
		SELECT id, username, role, created_by, created_at, email,
		       student_number, student_id_ext, sec_lec, sec_lab, first_name, last_name
		FROM app.system_users ORDER BY id ASC`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []models.User
	for rows.Next() {
		var u models.User
		var roleStr string
		err := rows.Scan(
			&u.ID, &u.Username, &roleStr, &u.CreatedBy, &u.CreatedAt, &u.Email,
			&u.StudentNumber, &u.StudentIDExt, &u.SecLec, &u.SecLab, &u.FirstName, &u.LastName,
		)
		if err != nil {
			return nil, err
		}
		u.Role = models.Role(roleStr)
		users = append(users, u)
	}
	return users, rows.Err()
}

func (r *PostgresRepository) UpdateUser(ctx context.Context, u models.User) error {
	query := `
		UPDATE app.system_users SET
			username = $1, role = $2, student_number = $3, student_id_ext = $4,
			sec_lec = $5, sec_lab = $6, first_name = $7, last_name = $8
		WHERE id = $9`

	_, err := r.pool.Exec(ctx, query,
		u.Username, u.Role, u.StudentNumber, u.StudentIDExt,
		u.SecLec, u.SecLab, u.FirstName, u.LastName, u.ID,
	)
	return err
}

func (r *PostgresRepository) DeleteUser(ctx context.Context, id int) error {
	query := `DELETE FROM app.system_users WHERE id = $1`
	_, err := r.pool.Exec(ctx, query, id)
	return err
}

func (r *PostgresRepository) GetAllUsersByRole(ctx context.Context, role models.Role) ([]models.User, error) {
	query := `SELECT id, username, role, created_by, created_at, email, student_number, student_id_ext, sec_lec, sec_lab, first_name, last_name FROM app.system_users WHERE role = $1 ORDER BY username`
	rows, err := r.pool.Query(ctx, query, string(role))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := []models.User{}
	for rows.Next() {
		var u models.User
		var roleStr string
		err := rows.Scan(&u.ID, &u.Username, &roleStr, &u.CreatedBy, &u.CreatedAt, &u.Email, &u.StudentNumber, &u.StudentIDExt, &u.SecLec, &u.SecLab, &u.FirstName, &u.LastName)
		if err != nil {
			return nil, err
		}
		u.Role = models.Role(roleStr)
		users = append(users, u)
	}
	return users, rows.Err()
}

// InsertPasswordEvent records one password-change event. The password value is
// already encrypted by the caller (services.PasswordCipher); only ciphertext
// and nonce are persisted.
func (r *PostgresRepository) InsertPasswordEvent(ctx context.Context, ev models.PasswordEvent) (int, error) {
	query := `
		INSERT INTO app.password_events (user_id, changed_by, source, new_password_enc, nonce, notified_admin)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`
	var id int
	err := r.pool.QueryRow(ctx, query,
		ev.UserID, ev.ChangedBy, ev.Source, ev.NewPasswordEnc, ev.Nonce, ev.NotifiedAdmin,
	).Scan(&id)
	return id, err
}

// GetPasswordEvents returns a user's password-change events, newest first, with
// the acting user's username resolved. The ciphertext/nonce are returned as-is
// for the service layer to decrypt.
func (r *PostgresRepository) GetPasswordEvents(ctx context.Context, userID int) ([]models.PasswordEventRow, error) {
	query := `
		SELECT e.source, COALESCE(a.username, '') AS changed_by_username, e.created_at,
		       e.new_password_enc, e.nonce
		FROM app.password_events e
		LEFT JOIN app.system_users a ON a.id = e.changed_by
		WHERE e.user_id = $1
		ORDER BY e.created_at DESC, e.id DESC`
	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.PasswordEventRow{}
	for rows.Next() {
		var row models.PasswordEventRow
		if err := rows.Scan(&row.Source, &row.ChangedByUsername, &row.CreatedAt, &row.NewPasswordEnc, &row.Nonce); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// GetLatestPasswordEventTime returns the creation time of the most recent
// password event of the given source for a user. found is false when the user
// has no such event yet. Used to throttle forgot-password regeneration.
func (r *PostgresRepository) GetLatestPasswordEventTime(ctx context.Context, userID int, source string) (t time.Time, found bool, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT created_at FROM app.password_events
		 WHERE user_id = $1 AND source = $2
		 ORDER BY created_at DESC, id DESC
		 LIMIT 1`, userID, source).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return t, true, nil
}

// GetLastForgotResetAt returns the time of the user's most recent honoured
// forgot-password regeneration, or nil when they have never had one. This is the
// authoritative throttle clock and is independent of PASSWORD_LOG_KEY (unlike the
// password_events audit trail, which is only written when a key is configured).
func (r *PostgresRepository) GetLastForgotResetAt(ctx context.Context, userID int) (*time.Time, error) {
	var t *time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT last_forgot_reset_at FROM app.system_users WHERE id = $1`, userID).Scan(&t)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// SetLastForgotResetAt stamps the user's forgot-password throttle clock to now().
// Called synchronously the moment a reset is authorized, which both drives the
// throttle and narrows the TOCTOU double-reset window.
func (r *PostgresRepository) SetLastForgotResetAt(ctx context.Context, userID int) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE app.system_users SET last_forgot_reset_at = now() WHERE id = $1`, userID)
	return err
}

// SetResetOTP stores the SHA-256 hash and expiry of a forgot-password
// verification code and resets the attempt counter. The plaintext code is
// never persisted.
func (r *PostgresRepository) SetResetOTP(ctx context.Context, userID int, otpHash string, expiresAt time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE app.system_users
		 SET reset_otp_hash = $1, reset_otp_expires_at = $2, reset_otp_attempts = 0
		 WHERE id = $3`, otpHash, expiresAt, userID)
	return err
}

// GetResetOTP returns the stored verification-code hash, its expiry, and the
// current attempt count. hash/expiresAt are nil when no code is outstanding.
func (r *PostgresRepository) GetResetOTP(ctx context.Context, userID int) (hash *string, expiresAt *time.Time, attempts int, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT reset_otp_hash, reset_otp_expires_at, reset_otp_attempts
		 FROM app.system_users WHERE id = $1`, userID).Scan(&hash, &expiresAt, &attempts)
	if err != nil {
		return nil, nil, 0, err
	}
	return hash, expiresAt, attempts, nil
}

// IncrementResetOTPAttempts atomically bumps the verification attempt counter
// and returns the new count. Doing this in a single UPDATE...RETURNING keeps
// concurrent verify requests from under-counting.
func (r *PostgresRepository) IncrementResetOTPAttempts(ctx context.Context, userID int) (newCount int, err error) {
	err = r.pool.QueryRow(ctx,
		`UPDATE app.system_users SET reset_otp_attempts = reset_otp_attempts + 1
		 WHERE id = $1 RETURNING reset_otp_attempts`, userID).Scan(&newCount)
	return newCount, err
}

// ClearResetOTP removes any outstanding verification code (hash + expiry) and
// resets the attempt counter. Called after a successful verification so the
// code cannot be replayed.
func (r *PostgresRepository) ClearResetOTP(ctx context.Context, userID int) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE app.system_users
		 SET reset_otp_hash = NULL, reset_otp_expires_at = NULL, reset_otp_attempts = 0
		 WHERE id = $1`, userID)
	return err
}

// BumpTokenVersion increments a user's token_version, immediately invalidating
// every previously issued JWT for that user.
func (r *PostgresRepository) BumpTokenVersion(ctx context.Context, userID int) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE app.system_users SET token_version = token_version + 1 WHERE id = $1`, userID)
	return err
}

// GetTokenVersion returns a user's current token_version.
func (r *PostgresRepository) GetTokenVersion(ctx context.Context, userID int) (int, error) {
	var v int
	err := r.pool.QueryRow(ctx,
		`SELECT token_version FROM app.system_users WHERE id = $1`, userID).Scan(&v)
	return v, err
}

// UpsertStudentByNumber inserts or updates a student profile keyed by
// student_number. The login username is the student_id_ext (external student
// ID). The password hash is only used for the insert path.
func (r *PostgresRepository) UpsertStudentByNumber(ctx context.Context, tx pgx.Tx, in models.StudentImportRow, creatorID int) (outcome string, userID int, err error) {
	query := `
		INSERT INTO app.system_users (
			username, password_hash, role, created_by,
			student_number, student_id_ext, sec_lec, sec_lab, first_name, last_name, email
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (student_number) WHERE student_number IS NOT NULL DO UPDATE SET
			username = EXCLUDED.username,
			student_id_ext = EXCLUDED.student_id_ext,
			sec_lec = EXCLUDED.sec_lec,
			sec_lab = EXCLUDED.sec_lab,
			first_name = EXCLUDED.first_name,
			last_name = EXCLUDED.last_name,
			email = EXCLUDED.email
		RETURNING id, (xmax = 0) AS inserted`

	// email is stored NULL when empty so the column stays genuinely optional.
	var emailArg *string
	if in.Email != "" {
		emailArg = &in.Email
	}

	var inserted bool
	// Login username = the external student_id (student_id_ext), NOT the
	// student_number. The student_number remains the stable upsert conflict key
	// and the default password source. username is refreshed on conflict so a
	// re-import migrates an existing student's login to a changed student_id.
	err = tx.QueryRow(ctx, query,
		in.StudentIDExt,
		in.PasswordHash,
		string(models.RoleStudent),
		creatorID,
		in.StudentNumber,
		in.StudentIDExt,
		in.SecLec,
		in.SecLab,
		in.FirstName,
		in.LastName,
		emailArg,
	).Scan(&userID, &inserted)
	if err != nil {
		return "", 0, err
	}
	if inserted {
		return "created", userID, nil
	}
	return "updated", userID, nil
}
