package services

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
)

type capExportSender struct {
	mu    sync.Mutex
	calls int
	atts  []ExportAttachment
}

func (c *capExportSender) SendExport(to, subject, body string, attachments []ExportAttachment) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.atts = attachments
	return nil
}

func TestSubmissionExport_Integration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	repo, err := repository.NewPostgresRepository(dsn)
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	ctx := context.Background()
	if err := repo.CreateRestrictedUser(ctx); err != nil {
		t.Fatalf("CreateRestrictedUser: %v", err)
	}
	if err := repo.InitializeSystemTables(ctx); err != nil {
		t.Fatalf("InitializeSystemTables: %v", err)
	}

	// Fresh watermark well before any seeded rows.
	if err := repo.EnsureExportState(ctx, true); err != nil {
		t.Fatalf("EnsureExportState: %v", err)
	}
	if err := repo.AdvanceExportWatermark(ctx, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("AdvanceExportWatermark: %v", err)
	}
	if err := repo.SetExportEnabled(ctx, true); err != nil {
		t.Fatalf("SetExportEnabled: %v", err)
	}

	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsernameSvc("exp_int_stu"),
		Password: "hash",
		Role:     models.RoleStudent,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := repo.Pool().Exec(ctx,
		`INSERT INTO app.submissions (student_id, sql_text, is_correct, score, submitted_at)
		 VALUES ($1, 'SELECT 1', true, 1.0, now())`, uid); err != nil {
		t.Fatalf("seed submission: %v", err)
	}

	sender := &capExportSender{}
	svc := NewSubmissionExportService(repo, sender, ExportConfig{Interval: time.Hour, Recipient: "admin@x.test"})

	res, err := svc.ExportNow(ctx)
	if err != nil {
		t.Fatalf("ExportNow: %v", err)
	}
	if !res.Sent || sender.calls != 1 || len(sender.atts) != 2 {
		t.Fatalf("first run: sent=%v calls=%d atts=%d", res.Sent, sender.calls, len(sender.atts))
	}

	// Second immediate run: watermark advanced past the seeded row → nothing sent.
	res2, err := svc.ExportNow(ctx)
	if err != nil {
		t.Fatalf("ExportNow 2: %v", err)
	}
	if res2.Sent || sender.calls != 1 {
		t.Fatalf("second run should send nothing: sent=%v calls=%d", res2.Sent, sender.calls)
	}
}

func uniqueUsernameSvc(prefix string) string {
	return prefix + "_" + time.Now().Format("20060102150405.000000000")
}
