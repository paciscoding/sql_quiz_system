package controllers

// Exclusion guard (ARCH-006/ARCH-013): the SSE hub's internal map KEY stays a
// plain int, but the exam_id inside the SSE DATA payload emitted by
// buildExamStatusEvent is a genuine browser-consumed enumeration surface and
// MUST be an encoded, quoted string — never the bare numeric id.

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/publicid"
)

var sseDataLineRE = regexp.MustCompile(`(?m)^data: (.+)$`)

func extractSSEData(t *testing.T, event string) map[string]interface{} {
	t.Helper()
	m := sseDataLineRE.FindStringSubmatch(event)
	if m == nil {
		t.Fatalf("no data: line found in SSE event: %q", event)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(m[1]), &parsed); err != nil {
		t.Fatalf("data line is not valid JSON: %v\nline: %s", err, m[1])
	}
	return parsed
}

// TestBuildExamStatusEvent_ExamIDIsEncodedString is the happy path: exam_id
// in the SSE data payload is a string that decodes back to the original int
// under TypeExam.
func TestBuildExamStatusEvent_ExamIDIsEncodedString(t *testing.T) {
	event := buildExamStatusEvent(42, "active")

	data := extractSSEData(t, event)
	raw, ok := data["exam_id"]
	if !ok {
		t.Fatalf("exam_id missing from SSE payload: %v", data)
	}
	s, ok := raw.(string)
	if !ok {
		t.Fatalf("exam_id must be a JSON string, got %T (%v) — leaked a bare numeric id", raw, raw)
	}
	got, err := publicid.Decode(publicid.TypeExam, s)
	if err != nil {
		t.Fatalf("exam_id token failed to decode as TypeExam: %v", err)
	}
	if got != 42 {
		t.Fatalf("exam_id round-trip mismatch: want 42, got %d", got)
	}
}

// TestBuildExamStatusEvent_EffectiveStatePassedThrough is a lightweight
// non-id sanity check that the phase argument still flows through untouched.
func TestBuildExamStatusEvent_EffectiveStatePassedThrough(t *testing.T) {
	event := buildExamStatusEvent(1, "ended")
	data := extractSSEData(t, event)
	if data["effective_state"] != "ended" {
		t.Fatalf("effective_state: want %q, got %v", "ended", data["effective_state"])
	}
}

// TestBuildExamStatusEvent_ZeroIDStillEncodedString is the edge case: even
// exam id 0 must render as a (non-empty) encoded string, not as the raw
// number 0 / omitted field.
func TestBuildExamStatusEvent_ZeroIDStillEncodedString(t *testing.T) {
	event := buildExamStatusEvent(0, "active")
	data := extractSSEData(t, event)
	raw, ok := data["exam_id"]
	if !ok {
		t.Fatalf("exam_id missing for id=0: %v", data)
	}
	s, ok := raw.(string)
	if !ok || s == "" {
		t.Fatalf("exam_id for id=0 must be a non-empty string, got %T (%v)", raw, raw)
	}
	if !strings.HasPrefix(s, "E") {
		t.Fatalf("exam_id token must carry the TypeExam ('E') prefix, got %q", s)
	}
}
