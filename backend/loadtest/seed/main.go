// Command seed provisions (or removes) the fixed pool of accounts the email-auth
// load test drives. Every account gets a UNIQUE username and a UNIQUE, non-empty
// email — both are mandatory for the forgot-password flow: it silently no-ops for
// accounts with no email (auth_service.beginForgotReset), and the 5-minute
// per-account throttle means each account can only be exercised once per run, so
// N samples require N distinct accounts.
//
// The initial password is irrelevant to the load test: the flow REGENERATES the
// password at verify time and emails the new one, and the driver logs in with
// that. We still store a valid bcrypt hash so the row is well-formed.
//
// Usage (run from the repo host, against the db port exposed by the loadtest
// compose override):
//
//	DATABASE_URL='postgres://sqlquiz:PASS@localhost:5432/sqlquiz?sslmode=disable' \
//	  go run ./loadtest/seed -n 150
//	  go run ./loadtest/seed -teardown        # delete all loadtest%% accounts
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// UsernamePrefix is shared with the driver: it builds the same names from -n.
const UsernamePrefix = "loadtest"

// EmailDomain is a reserved, non-routable domain (RFC 2606) so a stray real send
// can never reach a live inbox.
const EmailDomain = "example.com"

func main() {
	n := flag.Int("n", 150, "number of accounts to seed")
	teardown := flag.Bool("teardown", false, "delete all loadtest accounts instead of seeding")
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "postgres DSN (default $DATABASE_URL)")
	flag.Parse()

	if *dsn == "" {
		log.Fatal("no DSN: set DATABASE_URL or pass -dsn")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	if *teardown {
		tag, err := pool.Exec(ctx,
			`DELETE FROM app.system_users WHERE username LIKE $1`, UsernamePrefix+"%")
		if err != nil {
			log.Fatalf("teardown: %v", err)
		}
		log.Printf("deleted %d loadtest accounts", tag.RowsAffected())
		return
	}

	// One bcrypt hash reused for every seed row — cost is spent once, not 150x.
	hash, err := bcrypt.GenerateFromPassword([]byte("loadtest-initial-unused"), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("bcrypt: %v", err)
	}

	inserted := 0
	for i := 1; i <= *n; i++ {
		username := fmt.Sprintf("%s%04d", UsernamePrefix, i)
		email := fmt.Sprintf("%s%04d@%s", UsernamePrefix, i, EmailDomain)
		// created_by is nullable (no self-referential row required). ON CONFLICT
		// keeps the command idempotent across re-runs.
		tag, err := pool.Exec(ctx,
			`INSERT INTO app.system_users (username, password_hash, role, email)
			 VALUES ($1, $2, 'STUDENT', $3)
			 ON CONFLICT (username) DO NOTHING`,
			username, string(hash), email)
		if err != nil {
			log.Fatalf("insert %s: %v", username, err)
		}
		inserted += int(tag.RowsAffected())
	}
	log.Printf("seeded %d new accounts (%d requested; existing ones skipped)", inserted, *n)
}
