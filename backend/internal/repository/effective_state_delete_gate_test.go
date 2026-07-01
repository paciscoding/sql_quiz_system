package repository

// Regression tests for the ExaminationEffectiveState ActiveNow gate that
// controls DELETE 409 behaviour (TODO-001).
//
// effective_state_test.go already covers the Phase field comprehensively.
// These additional cases focus on the ActiveNow bool because that is the
// exact predicate used by DeleteExamination:
//
//	if repository.ExaminationEffectiveState(exam).ActiveNow { → 409 }
//
// We add only cases that are not already present in the existing file
// (open-ended window edge cases and the no-schedule / both-nil path).

import (
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

func TestActiveNow_DeleteGate(t *testing.T) {
	now := time.Now().UTC()
	past := now.Add(-1 * time.Hour)
	future := now.Add(1 * time.Hour)

	cases := []struct {
		name          string
		exam          models.Examination
		wantActiveNow bool
		desc          string // why this matters for the DELETE gate
	}{
		{
			name:          "inactive flag → never blocks delete",
			exam:          models.Examination{IsActive: false},
			wantActiveNow: false,
			desc:          "is_active=false always allows delete regardless of timestamps",
		},
		{
			name:          "active, no schedule → blocks delete",
			exam:          models.Examination{IsActive: true},
			wantActiveNow: true,
			desc:          "open-ended exam (no schedule) is always active → delete must be 409",
		},
		{
			name: "active, start in past, no end → blocks delete (open-ended)",
			exam: models.Examination{
				IsActive:         true,
				ScheduledStartAt: ptr(past),
				ScheduledEndAt:   nil,
			},
			wantActiveNow: true,
			desc:          "window opened, no end time → still active → 409",
		},
		{
			name: "active, window open [past, future] → blocks delete",
			exam: models.Examination{
				IsActive:         true,
				ScheduledStartAt: ptr(past),
				ScheduledEndAt:   ptr(future),
			},
			wantActiveNow: true,
			desc:          "current time within scheduled window → active → 409",
		},
		{
			name: "active, window closed (end in past) → allows delete",
			exam: models.Examination{
				IsActive:         true,
				ScheduledStartAt: ptr(now.Add(-3 * time.Hour)),
				ScheduledEndAt:   ptr(past),
			},
			wantActiveNow: false,
			desc:          "exam ended (now > end) → ActiveNow=false → delete allowed",
		},
		{
			name: "active, window not yet open (start in future) → allows delete",
			exam: models.Examination{
				IsActive:         true,
				ScheduledStartAt: ptr(future),
			},
			wantActiveNow: false,
			desc:          "exam scheduled but not started → ActiveNow=false → delete allowed",
		},
		{
			name: "active, zero-length window (start==end, both in past) → allows delete",
			exam: models.Examination{
				IsActive:         true,
				ScheduledStartAt: ptr(past),
				ScheduledEndAt:   ptr(past),
			},
			wantActiveNow: false,
			desc:          "degenerate window (length 0, now after it) → ended → delete allowed",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := ExaminationEffectiveState(tc.exam)
			if got.ActiveNow != tc.wantActiveNow {
				t.Errorf("ActiveNow = %v, want %v — %s", got.ActiveNow, tc.wantActiveNow, tc.desc)
			}
		})
	}
}
