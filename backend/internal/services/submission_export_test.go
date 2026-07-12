package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

type fakeExportRepo struct {
	mu        sync.Mutex
	state     models.ExportState
	subs      []models.SubmissionExportRow
	pracs     []models.PracticeAttemptExportRow
	advanced  []time.Time
	failQuery bool
}

func (f *fakeExportRepo) GetExportState(ctx context.Context) (models.ExportState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, nil
}
func (f *fakeExportRepo) AdvanceExportWatermark(ctx context.Context, t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.advanced = append(f.advanced, t)
	f.state.LastExportAt = t
	return nil
}
func (f *fakeExportRepo) ExportSubmissions(ctx context.Context, from, to time.Time) ([]models.SubmissionExportRow, error) {
	if f.failQuery {
		return nil, errors.New("boom")
	}
	return f.subs, nil
}
func (f *fakeExportRepo) ExportPracticeAttempts(ctx context.Context, from, to time.Time) ([]models.PracticeAttemptExportRow, error) {
	if f.failQuery {
		return nil, errors.New("boom")
	}
	return f.pracs, nil
}

type fakeExportSender struct {
	mu       sync.Mutex
	calls    int
	lastAtts []ExportAttachment
	failSend bool
}

func (f *fakeExportSender) SendExport(to, subject, body string, attachments []ExportAttachment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failSend {
		return errors.New("send failed")
	}
	f.calls++
	f.lastAtts = attachments
	return nil
}

func newSvc(repo *fakeExportRepo, sender *fakeExportSender) *SubmissionExportService {
	s := NewSubmissionExportService(repo, sender, ExportConfig{Interval: time.Hour, Recipient: "admin@x.test"})
	s.now = func() time.Time { return time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC) }
	return s
}

func TestExportNow_SendsAndAdvancesOnData(t *testing.T) {
	sid := 5
	repo := &fakeExportRepo{
		state: models.ExportState{Enabled: true, LastExportAt: time.Unix(0, 0).UTC()},
		subs:  []models.SubmissionExportRow{{ID: 1, StudentID: &sid, Username: "a", SubmittedAt: time.Unix(1, 0).UTC()}},
	}
	sender := &fakeExportSender{}
	res, err := newSvc(repo, sender).ExportNow(context.Background())
	if err != nil {
		t.Fatalf("ExportNow: %v", err)
	}
	if !res.Sent || sender.calls != 1 {
		t.Fatalf("expected one send, got calls=%d sent=%v", sender.calls, res.Sent)
	}
	if len(sender.lastAtts) != 2 {
		t.Fatalf("attachments = %d, want 2", len(sender.lastAtts))
	}
	if len(repo.advanced) != 1 {
		t.Fatalf("watermark advanced %d times, want 1", len(repo.advanced))
	}
}

func TestExportNow_EmptyWindowAdvancesNoSend(t *testing.T) {
	repo := &fakeExportRepo{state: models.ExportState{Enabled: true}}
	sender := &fakeExportSender{}
	res, err := newSvc(repo, sender).ExportNow(context.Background())
	if err != nil {
		t.Fatalf("ExportNow: %v", err)
	}
	if res.Sent || sender.calls != 0 {
		t.Fatal("must not send on empty window")
	}
	if len(repo.advanced) != 1 {
		t.Fatal("empty window must still advance watermark")
	}
}

func TestExportNow_DisabledIsNoOp(t *testing.T) {
	repo := &fakeExportRepo{state: models.ExportState{Enabled: false}}
	sender := &fakeExportSender{}
	res, _ := newSvc(repo, sender).ExportNow(context.Background())
	if res.Enabled || sender.calls != 0 || len(repo.advanced) != 0 {
		t.Fatal("disabled must be a pure no-op")
	}
}

func TestExportNow_EmptyRecipientHoldsWatermark(t *testing.T) {
	sid := 5
	repo := &fakeExportRepo{
		state: models.ExportState{Enabled: true},
		subs:  []models.SubmissionExportRow{{ID: 1, StudentID: &sid, SubmittedAt: time.Unix(1, 0).UTC()}},
	}
	sender := &fakeExportSender{}
	s := NewSubmissionExportService(repo, sender, ExportConfig{Interval: time.Hour, Recipient: ""})
	s.now = func() time.Time { return time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC) }
	_, err := s.ExportNow(context.Background())
	if err == nil {
		t.Fatal("expected error for empty recipient")
	}
	if sender.calls != 0 {
		t.Fatalf("sender.calls = %d, want 0", sender.calls)
	}
	if len(repo.advanced) != 0 {
		t.Fatal("watermark must NOT advance when recipient is unconfigured")
	}
}

func TestExportNow_SendFailureHoldsWatermark(t *testing.T) {
	sid := 5
	repo := &fakeExportRepo{
		state: models.ExportState{Enabled: true},
		subs:  []models.SubmissionExportRow{{ID: 1, StudentID: &sid, SubmittedAt: time.Unix(1, 0).UTC()}},
	}
	sender := &fakeExportSender{failSend: true}
	_, err := newSvc(repo, sender).ExportNow(context.Background())
	if err == nil {
		t.Fatal("expected send error")
	}
	if len(repo.advanced) != 0 {
		t.Fatal("watermark must NOT advance on send failure")
	}
}
