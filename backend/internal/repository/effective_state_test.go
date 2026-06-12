package repository

import (
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

func ptr(t time.Time) *time.Time { return &t }

func TestExaminationEffectiveState(t *testing.T) {
	now := time.Now().UTC()
	past := now.Add(-2 * time.Hour)
	future := now.Add(2 * time.Hour)
	farPast := now.Add(-4 * time.Hour)

	tests := []struct {
		name          string
		exam          models.Examination
		wantPhase     string
		wantActiveNow bool
	}{
		{
			name:          "is_active=false → inactive regardless of schedule",
			exam:          models.Examination{IsActive: false},
			wantPhase:     "inactive",
			wantActiveNow: false,
		},
		{
			name:          "is_active=true, no schedule → active",
			exam:          models.Examination{IsActive: true},
			wantPhase:     "active",
			wantActiveNow: true,
		},
		{
			name: "is_active=true, start in future → scheduled (not yet active)",
			exam: models.Examination{
				IsActive:         true,
				ScheduledStartAt: ptr(future),
			},
			wantPhase:     "scheduled",
			wantActiveNow: false,
		},
		{
			name: "is_active=true, now within [start, end) → active",
			exam: models.Examination{
				IsActive:         true,
				ScheduledStartAt: ptr(past),
				ScheduledEndAt:   ptr(future),
			},
			wantPhase:     "active",
			wantActiveNow: true,
		},
		{
			name: "is_active=true, now >= end → ended",
			exam: models.Examination{
				IsActive:         true,
				ScheduledStartAt: ptr(farPast),
				ScheduledEndAt:   ptr(past),
			},
			wantPhase:     "ended",
			wantActiveNow: false,
		},
		{
			name: "end <= start (invalid range) — start in past, end also in past before start: ended",
			// farPast < past, so window is [farPast, past) with now > past → ended
			exam: models.Examination{
				IsActive:         true,
				ScheduledStartAt: ptr(farPast),
				ScheduledEndAt:   ptr(past),
			},
			wantPhase:     "ended",
			wantActiveNow: false,
		},
		{
			name: "end == start exactly (degenerate: zero-length window, now > end) → ended",
			exam: models.Examination{
				IsActive:         true,
				ScheduledStartAt: ptr(past),
				ScheduledEndAt:   ptr(past), // same instant — window length 0, now is after
			},
			wantPhase:     "ended",
			wantActiveNow: false,
		},
		{
			name: "null end with past start → active (open-ended window)",
			exam: models.Examination{
				IsActive:         true,
				ScheduledStartAt: ptr(past),
				ScheduledEndAt:   nil,
			},
			wantPhase:     "active",
			wantActiveNow: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := ExaminationEffectiveState(tc.exam)
			if got.Phase != tc.wantPhase {
				t.Errorf("Phase = %q, want %q", got.Phase, tc.wantPhase)
			}
			if got.ActiveNow != tc.wantActiveNow {
				t.Errorf("ActiveNow = %v, want %v", got.ActiveNow, tc.wantActiveNow)
			}
		})
	}
}
