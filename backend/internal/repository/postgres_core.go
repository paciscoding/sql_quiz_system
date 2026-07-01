package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Query executes a SELECT statement within a specific schema
func (r *PostgresRepository) Query(ctx context.Context, schema, sql string) ([]map[string]interface{}, error) {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	// Set schema for this session
	safeSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET search_path TO %s, public", safeSchema)); err != nil {
		return nil, fmt.Errorf("failed to set search_path: %w", err)
	}

	rows, err := conn.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fieldDescriptions := rows.FieldDescriptions()
	var results []map[string]interface{}

	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}

		rowMap := make(map[string]interface{})
		for i, fd := range fieldDescriptions {
			rowMap[string(fd.Name)] = values[i]
		}
		results = append(results, rowMap)
	}

	return results, rows.Err()
}

// Exec executes a DML statement within a specific schema
func (r *PostgresRepository) Exec(ctx context.Context, schema, sql string) error {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	safeSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET search_path TO %s, public", safeSchema)); err != nil {
		return fmt.Errorf("failed to set search_path: %w", err)
	}

	_, err = conn.Exec(ctx, sql)
	return err
}

// GetTableState returns a string representation of the table state
func (r *PostgresRepository) GetTableState(ctx context.Context, schema, tableName string) (string, error) {
	safeTable := pgx.Identifier{tableName}.Sanitize()
	sql := fmt.Sprintf("SELECT * FROM %s", safeTable)
	results, err := r.Query(ctx, schema, sql)
	if err != nil {
		return "", err
	}

	b, err := json.Marshal(results)
	return string(b), err
}

// WithTransaction executes a function within a database transaction
func (r *PostgresRepository) WithTransaction(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}

	err = fn(tx)
	if err != nil {
		_ = tx.Rollback(ctx)
		return err
	}

	return tx.Commit(ctx)
}

func (r *PostgresRepository) queryWithTx(ctx context.Context, tx pgx.Tx, schema, sql string) ([]map[string]interface{}, error) {
	safeSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s, public", safeSchema)); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fieldDescriptions := rows.FieldDescriptions()
	var results []map[string]interface{}

	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}

		rowMap := make(map[string]interface{})
		for i, fd := range fieldDescriptions {
			rowMap[string(fd.Name)] = values[i]
		}
		results = append(results, rowMap)
	}

	return results, rows.Err()
}

// queryWithTxCols is like queryWithTx but also returns ordered column names
// derived from pgx FieldDescriptions, so callers can capture the projection
// order for the ColumnOrder strict-mode gate.
func (r *PostgresRepository) queryWithTxCols(ctx context.Context, tx pgx.Tx, schema, sql string) ([]map[string]interface{}, []string, error) {
	safeSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s, public", safeSchema)); err != nil {
		return nil, nil, err
	}
	rows, err := tx.Query(ctx, sql)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	fds := rows.FieldDescriptions()
	columns := make([]string, len(fds))
	for i, fd := range fds {
		columns[i] = string(fd.Name)
	}
	var results []map[string]interface{}
	for rows.Next() {
		values, verr := rows.Values()
		if verr != nil {
			return nil, nil, verr
		}
		rowMap := make(map[string]interface{})
		for i, fd := range fds {
			rowMap[string(fd.Name)] = values[i]
		}
		results = append(results, rowMap)
	}
	return results, columns, rows.Err()
}

// collectRows drains a pgx.Rows into a slice of column-keyed maps. The caller
// is responsible for closing the rows.
func collectRows(rows pgx.Rows) ([]map[string]interface{}, error) {
	defer rows.Close()
	fieldDescriptions := rows.FieldDescriptions()
	var out []map[string]interface{}
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}
		rowMap := make(map[string]interface{})
		for i, fd := range fieldDescriptions {
			rowMap[string(fd.Name)] = values[i]
		}
		out = append(out, rowMap)
	}
	return out, rows.Err()
}

// collectRowsWithColumns drains a pgx.Rows into a slice of column-keyed maps
// and returns both the data and the ordered column names. The caller is
// responsible for not using rows after this call returns.
func collectRowsWithColumns(rows pgx.Rows) (data []map[string]interface{}, columns []string, err error) {
	defer rows.Close()
	fds := rows.FieldDescriptions()
	columns = make([]string, len(fds))
	for i, fd := range fds {
		columns[i] = string(fd.Name)
	}
	for rows.Next() {
		values, vErr := rows.Values()
		if vErr != nil {
			return nil, nil, vErr
		}
		rowMap := make(map[string]interface{})
		for i, fd := range fds {
			rowMap[string(fd.Name)] = values[i]
		}
		data = append(data, rowMap)
	}
	return data, columns, rows.Err()
}
