package repository

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

func (r *PostgresRepository) CreateUser(ctx context.Context, u models.User) (int, error) {
	query := `INSERT INTO app.system_users (username, password_hash, role, created_by) VALUES ($1, $2, $3, $4) RETURNING id`
	var id int
	err := r.pool.QueryRow(ctx, query, u.Username, u.Password, string(u.Role), u.CreatedBy).Scan(&id)
	return id, err
}

func (r *PostgresRepository) UpdateUserPassword(ctx context.Context, id int, hashedPassword string) error {
	query := `UPDATE app.system_users SET password_hash = $1 WHERE id = $2`
	_, err := r.pool.Exec(ctx, query, hashedPassword, id)
	return err
}

func (r *PostgresRepository) GetUserByID(ctx context.Context, id int) (models.User, error) {
	query := `SELECT id, username, password_hash, role, created_by, created_at FROM app.system_users WHERE id = $1`
	var u models.User
	var role string
	err := r.pool.QueryRow(ctx, query, id).Scan(&u.ID, &u.Username, &u.Password, &role, &u.CreatedBy, &u.CreatedAt)
	u.Role = models.Role(role)
	return u, err
}

func (r *PostgresRepository) GetUserByUsername(ctx context.Context, username string) (models.User, error) {
	query := `SELECT id, username, password_hash, role, created_by, created_at FROM app.system_users WHERE username = $1`
	var u models.User
	var role string
	err := r.pool.QueryRow(ctx, query, username).Scan(&u.ID, &u.Username, &u.Password, &role, &u.CreatedBy, &u.CreatedAt)
	u.Role = models.Role(role)
	return u, err
}

func (r *PostgresRepository) GetAllUsers(ctx context.Context) ([]models.User, error) {
	query := `
		SELECT id, username, role, created_by, created_at,
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
			&u.ID, &u.Username, &roleStr, &u.CreatedBy, &u.CreatedAt,
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
	query := `SELECT id, username, role, created_by, created_at, student_number, student_id_ext, sec_lec, sec_lab, first_name, last_name FROM app.system_users WHERE role = $1 ORDER BY username`
	rows, err := r.pool.Query(ctx, query, string(role))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := []models.User{}
	for rows.Next() {
		var u models.User
		var roleStr string
		err := rows.Scan(&u.ID, &u.Username, &roleStr, &u.CreatedBy, &u.CreatedAt, &u.StudentNumber, &u.StudentIDExt, &u.SecLec, &u.SecLab, &u.FirstName, &u.LastName)
		if err != nil {
			return nil, err
		}
		u.Role = models.Role(roleStr)
		users = append(users, u)
	}
	return users, rows.Err()
}

// UpsertStudentByNumber inserts or updates a student profile keyed by
// student_number. The password hash is only used for the insert path.
func (r *PostgresRepository) UpsertStudentByNumber(ctx context.Context, tx pgx.Tx, in models.StudentImportRow, creatorID int) (outcome string, userID int, err error) {
	query := `
		INSERT INTO app.system_users (
			username, password_hash, role, created_by,
			student_number, student_id_ext, sec_lec, sec_lab, first_name, last_name
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (student_number) WHERE student_number IS NOT NULL DO UPDATE SET
			student_id_ext = EXCLUDED.student_id_ext,
			sec_lec = EXCLUDED.sec_lec,
			sec_lab = EXCLUDED.sec_lab,
			first_name = EXCLUDED.first_name,
			last_name = EXCLUDED.last_name
		RETURNING id, (xmax = 0) AS inserted`

	var inserted bool
	err = tx.QueryRow(ctx, query,
		in.StudentNumber,
		in.PasswordHash,
		string(models.RoleStudent),
		creatorID,
		in.StudentNumber,
		in.StudentIDExt,
		in.SecLec,
		in.SecLab,
		in.FirstName,
		in.LastName,
	).Scan(&userID, &inserted)
	if err != nil {
		return "", 0, err
	}
	if inserted {
		return "created", userID, nil
	}
	return "updated", userID, nil
}
