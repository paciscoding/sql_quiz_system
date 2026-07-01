# Multi-Exam Backend Design — Deferred Backend Work

> This document supersedes the lecturer-section notes formerly in NEW_ADDITION_REQUIREMENT.md (now removed). The implemented frontend/endpoint work is described in the project plan; this report covers the deeper backend changes that were intentionally deferred.

---

## 1. Per-Exam Curriculum Isolation

> **Status: IMPLEMENTED (backward-compatible).** Shipped as an additive `curriculum_schema TEXT NOT NULL DEFAULT 'public'` column on `app.examinations` plus a new per-exam import endpoint (`POST /api/v1/lecturer/exams/:id/curriculum/import`); see API_SPEC.md §9.5 and ARCHITECTURE.md § Schema Layout. The migration described below is unnecessary: legacy exams keep `curriculum_schema = 'public'` and read from `public` exactly as before.

### Problem

Today every examination shares a single curriculum source: the `public` schema. The `ImportSchema` endpoint (POST `/api/v1/lecturer/import-schema`) is gated with a hard 409 if **any** examination is currently active:

> *"`ImportSchema` → 409 (would invalidate every active exam's cache and any active sandbox clone)."*
> — ARCHITECTURE.md § Phase Lifecycle, Phase 1 Freeze

This means:

- A lecturer cannot import or prepare a different database schema for **Exam B** while **Exam A** is running. Even if the two exams cover completely different curricula (e.g., Exam A uses the Elmasri Company schema; Exam B is intended to use a Hospital schema), they are serialized: Exam B's import can only begin after Exam A is fully deactivated and the sandbox dropped.
- Two examinations cannot run simultaneously if they require different underlying table structures, because both would read from the same `public.*`.
- The global import gate is blunt. Even an import that adds an entirely new table that no active exam references would be blocked.

### Proposed Solution: Per-Exam Curriculum Schemas

Give each examination its own dedicated curriculum schema, named `curriculum_exam_{examID}`, parallel to the existing `practice_cat_{categoryID}` per-category schema pattern.

**Precedent:** The practice module already demonstrates this design is viable and production-ready. From ARCHITECTURE.md:

> *"Every category owns its own schema (`practice_cat_{id}`) into which a lecturer imports a database; all of that category's submissions (SELECT and DML) execute against that schema, fully isolated from the exam curriculum in `public`."*

Applying the same pattern to examinations:

| Concern | Current behavior | Proposed behavior |
|---|---|---|
| Curriculum import target | Always `public.*` | `curriculum_exam_{examID}.*` |
| Phase 0 Correct Result capture | Runs against a clone of `public` | Runs against a clone of `curriculum_exam_{examID}` |
| Phase 1 sandbox clone source | `public.*` → `sandbox_exam_{examID}` | `curriculum_exam_{examID}.*` → `sandbox_exam_{examID}` |
| Phase 2 SELECT read-path | `SET LOCAL search_path = public` | `SET LOCAL search_path = curriculum_exam_{examID}` |
| Import gate | Global: blocked if ANY exam active | Per-exam: blocked only if THIS exam is active |

**Import endpoint change.** The per-exam import would be a new endpoint scoped to an exam, e.g. `POST /api/v1/lecturer/exams/:id/import-schema`, replacing the global `POST /api/v1/lecturer/import-schema` for the new model. The existing global import endpoint would remain for backward compatibility with any workflows that use `public` as a shared starting point, but the per-exam path would be the recommended one going forward.

**Correct Result capture change.** Phase 0's capture step in `UpsertQuestion` currently runs the teacher SQL on a transient clone of `public`. Under the proposed model, it would instead clone `curriculum_exam_{examID}` (the exam's own schema) as the reference, then roll back. The capture logic itself (`BEGIN; CREATE SCHEMA clone; CREATE TABLE clone.x LIKE curriculum_exam_{examID}.x; INSERT…; run teacher SQL; ROLLBACK`) remains structurally identical.

**Phase 2 SELECT path change.** The `SET LOCAL search_path = public` in the `exam_student_readonly` connection would be replaced by `SET LOCAL search_path = curriculum_exam_{examID}`. The `exam_student_readonly` role would need `USAGE` on `curriculum_exam_{examID}` and `SELECT` on its tables (granted at exam creation time).

### Trade-offs vs. the Current Single-Template Model

| Factor | Single `public` template | Per-exam `curriculum_exam_{id}` |
|---|---|---|
| Storage | One copy of the curriculum data | One copy per exam; N exams = N copies |
| Import ergonomics | Simple: import once, all exams share it | Per-exam import step is required for each exam; more explicit but more setup |
| Concurrency | Exams must be serialized around imports | Exams are fully independent; parallel imports allowed |
| Migration | No migration needed for current exams | Existing single-`public` exam data must be copied to `curriculum_exam_{id}` schemas for each historical exam |
| Cleanup | No per-exam schema to drop | Each `curriculum_exam_{id}` must be dropped when the exam is deleted (or immediately after the sandbox is created, if the schema is no longer needed post-activation) |

**Storage concern:** For typical university exam curricula (dozens to low hundreds of tables, data in the megabyte range), duplicating the schema N times is negligible. This is the same trade-off the practice module already makes across categories.

**Migration path for existing data.** For any examinations that already exist (all currently referencing `public`), a one-time migration would: create `curriculum_exam_{id}` for each existing exam, copy `public.*` into it, and update any metadata. This can be done without downtime by doing the copy while the exam is inactive, which all historical (archived) exams are by definition.

---

## 2. Concurrent-Active-Exam Freeze Model

> **Status: IMPLEMENTED (backward-compatible).** The per-exam curriculum import endpoint is gated on that specific exam's active state rather than globally; the legacy global `ImportSchema` gate is unchanged. The relaxed freeze model described below applies to the new `POST /lecturer/exams/:id/curriculum/import` endpoint; see API_SPEC.md §9.5 and ARCHITECTURE.md § Phase 1 Freeze for the authoritative gate rules.

### Current Global Gate

`ARCHITECTURE.md § Phase Lifecycle` documents the current global freezes that trigger whenever any exam is active:

- `ImportSchema` → 409 (globally blocked)
- `UpsertQuestion` for any question of an active exam → 409 (per-exam, already correct)
- `AssignStudents` for an active exam → 409 (per-exam, already correct)

Only `ImportSchema` is globally blunt. The other two are already per-exam.

### Relaxed Model Once Curriculum is Per-Exam

Once each examination has its own `curriculum_exam_{id}` schema, the import gate can be narrowed from **"any active exam blocks"** to **"this exam's active state blocks"**:

| Operation | Current gate | Proposed gate |
|---|---|---|
| `ImportSchema` (global, affecting `public`) | BLOCKED if any exam active | DEPRECATED in favor of per-exam import |
| `POST /lecturer/exams/:id/import-schema` (new, per-exam) | N/A | BLOCKED if exam `:id` is active (409) |
| `UpsertQuestion` | Blocked if this exam is active (already per-exam) | No change |
| `AssignStudents` | Blocked if this exam is active (already per-exam) | No change |
| `ActivateExamination` | Blocked if any other exam active? | No — multiple exams can be simultaneously active |

**Per-exam freezes that remain:**
- Importing into `curriculum_exam_{id}` is blocked while exam `:id` is active (would invalidate the cached Correct Results and potentially invalidate the already-cloned sandbox).
- `UpsertQuestion` for a question belonging to exam `:id` is blocked while exam `:id` is active.
- `AssignStudents` for exam `:id` is blocked while exam `:id` is active.

**Freezes that become unnecessary globally:**
- The blanket `ImportSchema` 409 on `public.*` becomes irrelevant if no active exam reads from `public`. The endpoint can either be removed or limited to pre-exam setup workflows that don't interact with any active exam's curriculum schema.

**What this enables:**
- Exam A (Database Systems) and Exam B (Advanced SQL) run simultaneously with completely different schemas.
- While Exam A is running, a lecturer can import the curriculum for Exam C and define its questions, as long as Exam C itself is not yet activated.
- The submission queue and sandbox machinery are already per-exam (`sandbox_exam_{examID}`), so the DML serialisation path requires no changes.

---

## 3. Optional Results-Aggregate Endpoint

### Current Situation

The lecturer's results drill-down view is assembled client-side from three separate endpoints:

1. `GET /api/v1/lecturer/exams/:id/questions` — question list with metadata
2. `GET /api/v1/lecturer/exams/:id/submissions` — all submission rows
3. `GET /api/v1/lecturer/exams/:id/students` — enrolled student list

The frontend then computes per-question difficulty metrics (attempt count, correct count, average score) and derives the non-submitter list by diffing the enrolled student set against the set of students who have at least one submission.

For 90–150 students across an exam with 10–20 questions, this means the frontend may receive several hundred to a few thousand submission rows per page load and perform the aggregation in JavaScript. That is manageable today but grows linearly with both student count and submission count (students who retry DML questions generate multiple rows per question).

### Proposed Server-Side Aggregate: `GET /api/v1/lecturer/exams/:id/stats`

A single endpoint that returns pre-aggregated per-question difficulty data and the non-submitter list, computed with SQL `GROUP BY` at the database level.

**Response shape:**

```json
{
  "exam_id": 4,
  "per_question": [
    {
      "question_id": 1,
      "question_number": 1,
      "attempts": 142,
      "correct": 98,
      "avg_score": 7.3,
      "distinct_students_attempted": 140
    }
  ],
  "non_submitters": [
    {
      "student_id": 17,
      "student_number": "2021-00042",
      "first_name": "Ana",
      "last_name": "Reyes"
    }
  ],
  "total_enrolled": 148,
  "total_submitted": 143
}
```

**SQL sketch — per-question aggregates:**

```sql
SELECT
    q.id                                    AS question_id,
    q.question_number,
    COUNT(s.id)                             AS attempts,
    COUNT(s.id) FILTER (WHERE s.is_correct) AS correct,
    ROUND(AVG(s.score)::numeric, 1)         AS avg_score,
    COUNT(DISTINCT s.student_id)            AS distinct_students_attempted
FROM app.questions q
LEFT JOIN app.submissions s
    ON s.question_id = q.id
WHERE q.exam_id = $1
GROUP BY q.id, q.question_number
ORDER BY q.question_number;
```

**SQL sketch — non-submitters:**

```sql
SELECT
    u.id            AS student_id,
    u.student_number,
    u.first_name,
    u.last_name
FROM app.exam_students es
JOIN app.system_users u ON u.id = es.student_id
WHERE es.exam_id = $1
  AND es.student_id NOT IN (
      SELECT DISTINCT student_id
      FROM app.submissions
      WHERE exam_id = $1
  )
ORDER BY u.last_name, u.first_name;
```

Both queries hit indexed foreign keys (`submissions.exam_id`, `submissions.question_id`, `exam_students.exam_id`) and should execute in well under 100 ms for the expected data volume.

**Auth:** ADMIN | LECTURER. The endpoint does not require the exam to be active; lecturers review stats both during and after an exam.

**Implementation priority:** This endpoint is optional for the initial delivery. The three-endpoint client-side approach works correctly for current scale. The server-side aggregate becomes worth implementing if the student count exceeds ~300 or if the results page becomes noticeably slow in production load testing.

---

## 4. Rename / Delete Semantics

### New Endpoints Being Added

Two new lifecycle endpoints are being added to the examination resource:

- `PUT /api/v1/lecturer/exams/:id` — rename an examination
- `DELETE /api/v1/lecturer/exams/:id` — hard-delete an examination and all its dependent data

### Hard-Delete Cascade Order

A hard delete of an examination must remove all dependent rows and schemas in foreign-key-safe order to avoid constraint violations:

1. `DELETE FROM app.submissions WHERE exam_id = :id` — remove all student attempt records.
2. `DELETE FROM app.exam_students WHERE exam_id = :id` — remove the student roster junction rows.
3. Delete intro images associated with exam questions (stored objects / rows referencing the exam's questions, if any are tracked in a dedicated table). Intro image blobs should be removed from storage before the rows are deleted to avoid orphaned files.
4. `DELETE FROM app.questions WHERE exam_id = :id` — remove all question definitions and their cached Correct Results.
5. If a sandbox exists: `DROP SCHEMA sandbox_exam_{id} CASCADE` — drop the DML sandbox schema.
6. `DELETE FROM app.exam_sandboxes WHERE exam_id = :id` — remove the sandbox tracking row.
7. `DELETE FROM app.examinations WHERE id = :id` — finally remove the examination row itself.

Under the per-exam curriculum schema proposal (Section 1), step 5 would be extended to also `DROP SCHEMA curriculum_exam_{id} CASCADE` before deleting the examination row.

**Active exam guard:** Before executing any of the above, the delete endpoint must check `app.examinations.is_active`. If the exam is currently active, return 409 (`{"error": "deactivate the exam before deleting"}`). Deleting an active exam would destroy a live sandbox and invalidate in-flight submissions.

The entire cascade should run inside a single database transaction so that a mid-cascade failure leaves the database in a consistent state (either fully deleted or fully intact).

### Soft-Archive Alternative

An alternative to hard deletion is a soft-archive approach: set a `deleted_at` timestamp on the `app.examinations` row and hide the exam from all list and submission endpoints. This preserves submission history for audit purposes and is reversible.

**Contrast with current `archived_at` semantics.** This is the key caveat: `app.examinations.archived_at` is currently set by `DeactivateExamination` (Phase 3) to mean *"this exam has ended"*, not *"this exam has been deleted"*. It is a deactivation timestamp, not a deletion marker. Every deactivated exam already has `archived_at` populated. Repurposing `archived_at` as a soft-delete flag would corrupt this existing semantics — any deactivated exam would appear "deleted" under a naive filter.

A true soft-archive feature would therefore require a **separate column**, e.g. `app.examinations.deleted_at TIMESTAMPTZ DEFAULT NULL`. Exams with `deleted_at IS NOT NULL` would be excluded from list endpoints, submission ingestion, and activation, while their `app.submissions` rows remain queryable for historical reporting. The `deleted_at` approach is also cleaner for the score-export endpoint (`GET /api/v1/lecturer/export-scores`), which generates dynamic columns from all examinations: it would simply skip exams where `deleted_at IS NOT NULL`.

**Recommendation for initial delivery:** Implement hard delete (simpler, no schema change needed, matches user expectations for a "delete" action). If audit retention becomes a requirement, add `deleted_at` as an additive migration and switch the delete endpoint from physical removal to a timestamp write.
