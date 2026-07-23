package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrTargetTableEmpty is returned by ResolveTargetTable when the raw input is
// empty (after TrimSpace).
var ErrTargetTableEmpty = errors.New("target_table is required")

// ErrTargetTableQualified is returned by ResolveTargetTable when the raw input
// contains a '.' or '"', i.e. it looks schema-qualified or already quoted.
// ResolveTargetTable only accepts bare table names — the schema to search is
// supplied separately.
var ErrTargetTableQualified = errors.New("target_table must be a bare table name, not schema-qualified")

// ErrTargetTableNotFound is returned by ResolveTargetTable when no relation in
// the given schema matches the raw name, case-sensitively or case-insensitively.
var ErrTargetTableNotFound = errors.New("target table not found")

// ErrTargetTableAmbiguous is returned by ResolveTargetTable when two distinct
// relations in the schema case-insensitively match the raw name and neither is
// an exact (case-sensitive) match.
var ErrTargetTableAmbiguous = errors.New("target table name is ambiguous")

// ResolveTargetTable resolves raw to its canonical Postgres relation name
// (pg_class.relname) inside schema, via a single catalog lookup. Behaviour:
//  1. TrimSpace raw; empty -> ErrTargetTableEmpty.
//  2. raw containing '.' or '"' -> ErrTargetTableQualified.
//  3. One query against pg_catalog.pg_class / pg_catalog.pg_namespace, binding
//     schema and name as parameters (no string interpolation).
//  4. 0 rows -> ErrTargetTableNotFound, wrapped with the raw name and schema.
//  5. First row is an exact (case-sensitive) match -> return it.
//  6. Exactly 1 row (a case-insensitive match) -> return its relname.
//  7. 2 rows and no exact match -> ErrTargetTableAmbiguous, listing both
//     candidate names.
//
// Callers overwrite their stored target_table with the returned canonical name
// before capture and before persistence, so the stored value is byte-identical
// to pg_class.relname and quoting it with pgx.Identifier{}.Sanitize() always
// resolves.
func (r *PostgresRepository) ResolveTargetTable(ctx context.Context, schema, raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", ErrTargetTableEmpty
	}
	if strings.ContainsAny(name, ".\"") {
		return "", fmt.Errorf("%w: %q", ErrTargetTableQualified, raw)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT c.relname
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1
		  AND c.relkind IN ('r','p','v','m','f')
		  AND (c.relname = $2 OR lower(c.relname) = lower($2))
		ORDER BY (c.relname = $2) DESC, c.relname
		LIMIT 2`, schema, name)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var candidates []string
	for rows.Next() {
		var relname string
		if err := rows.Scan(&relname); err != nil {
			return "", err
		}
		candidates = append(candidates, relname)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("%w: %q in schema %q", ErrTargetTableNotFound, name, schema)
	case 1:
		return candidates[0], nil
	default:
		// 2 candidates. If the first is an exact (case-sensitive) match, the
		// ORDER BY guarantees it sorted first — return it.
		if candidates[0] == name {
			return candidates[0], nil
		}
		return "", fmt.Errorf("%w: %q in schema %q matches both %q and %q",
			ErrTargetTableAmbiguous, name, schema, candidates[0], candidates[1])
	}
}
