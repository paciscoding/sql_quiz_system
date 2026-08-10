# Option B — drop `public` from the DML `search_path` (scope note)

**Status:** scope only. Not a spec, not a plan, nothing implemented.
**Date:** 2026-08-10. **Closes:** the *unqualified targets* half of the
cross-schema seed hazard, recorded as STILL OPEN in `feedback.md` OPEN-1 and in
the *Known limitations* of `2026-08-09-practice-dml-schema-gate-design.md`.

## The hazard

A lecturer writes `INSERT INTO employee …`, not `INSERT INTO public.employee …`.
If `employee` is absent from the question's own schema, `search_path` carries it
through to `public` — a table shared by every category. Two categories graded
concurrently whose seeds touch the same shared tables in opposite order deadlock
with `40P01`, and the student is blamed. The ARCH-058 gate cannot see it: the
gate keys on the execution schema, and the lock is taken on a table in another
one. Option A (shipped 2026-08-10) closed only the *schema-qualified* form; a
denylist has nothing to match on when the name is bare.

This is the more likely of the two halves in practice.

## The change

Drop `, public` from four `search_path` statements:

| Site | Function |
|---|---|
| `postgres_evaluation.go:301` | `evaluateDML` — student submit |
| `postgres_evaluation.go:597` | `evaluateDMLPreview` — lecturer/student preview |
| `postgres_questions.go:129` | exam capture (`captureExpectedResult`) |
| `postgres_practice.go:461` | practice capture |

**They must move in one commit.** Capture and grading must agree about name
resolution; changing evaluation alone would let a question capture green against
`public` and then fail at grade time — the exact shape of the shipped
`target_table` case bug, where a lecturer-side error surfaced as student failure.

`pg_temp` stays LAST at the two evaluation sites (ARCH-023 — naming it
explicitly forces it out of first position, which is what stops a
`CREATE TEMP TABLE <target>` from shadowing the real table during post-image
capture). The two capture sites have no `pg_temp` today and should not gain one.

## Evidence it is safe

1. **The QUERY branch already does this and works.** `evaluateQuery:209` and
   `evaluateQueryPreview:509` run `SET LOCAL search_path TO <schema>, pg_temp` —
   no `public`. QUERY and DML questions in one category resolve the same tables,
   so a category whose tables live in its own schema is already proven to work
   without the fallback. The DML `public` is the outlier, not the norm.
2. **Every other schema-scoped site already omits it**, deliberately and with a
   comment saying so: category import (`postgres_schema.go:373`), preview build
   (`postgres_preview_schema.go:287`, "no public fallback"), exam category
   (`postgres_exam_categories.go:587`, `:711`).
3. **No extension-function risk on this deployment.** `search_path` governs
   function resolution too, so an extension installed into `public` would break
   unqualified calls. Checked the live database: the only extension is `plpgsql`,
   in `pg_catalog`. Nothing resolves through `public` for functions.
4. **Legacy exams are unaffected.** `curriculum_schema` resolves via
   `COALESCE(NULLIF(curriculum_schema,''),'public')` (`postgres_questions.go:80`,
   `:468`), so an exam with no per-exam import has execution schema `public`
   itself — the statement becomes `public, public, pg_temp` and dropping the
   duplicate is a no-op.
5. **`postgres_core.go`'s four `, public` sites are NOT on this path.**
   `queryWithTx` / `queryWithTxCols` / the two session helpers also append
   `public`, but `evaluateDML` calls none of them (verified: no `queryWithTx`
   reference in `postgres_evaluation.go`). They serve lecturer/admin paths and
   must be left alone — touching them widens the change enormously.

## What actually breaks

Exactly one class: **a question whose SQL names a table that exists only in
`public` while its execution schema is something else.** Today that silently
succeeds and writes the shared table. After the change it fails with `relation
… does not exist`.

Where the failure lands is the whole point: at **capture time**, when the
lecturer saves — a lecturer-facing error about their own SQL, at the moment they
can fix it. That is strictly better than the current behaviour, where the same
question grades every student against a table it should never have touched.

## The unresolved risk, and why an audit is mandatory

Already-captured questions do not re-validate until someone re-saves them. A
question captured against `public` before the change keeps
`expected_result_status='captured'` and a stale `expected_result`, then starts
failing at grade time after deploy. That is the same detection gap recorded for
option A in `feedback.md` — activation pre-flight only checks capture *status*,
not whether the capture is still reachable.

So this ships the same way option A did: **audit first, and the audit gates the
change.** Unlike option A's, this audit cannot be a pure text scan — it must
resolve names against the catalog. Sketch:

> for each DML question, resolve its execution schema (practice category
> `schema_name`, or exam sandbox / curriculum via the COALESCE above), then
> report any question whose `target_table` — and any unqualified table named in
> `correct_sql` / `seed_sql` — is absent from that schema but present in
> `public`.

`target_table` alone is cheap and catches most of it; full `correct_sql` parsing
is not worth building — the existing `pg_class` resolution used for
`target_table` (the ARCH-001/002 fix) is the precedent to reuse.

**This machine's database cannot validate any of it.** It holds only `app` (15
tables) and `public` (6 tables) — no `practice_cat_*`, no `sandbox_exam_*`, no
exams at all. Same limitation that made option A's audit vacuous. Fixtures will
have to carry the test burden, and the audit must be re-run wherever real
lecturer content lives.

## Proposed task shape

1. **Audit script** — `scripts/audit_public_fallback_questions.sql`, read-only,
   house style (`BEGIN; SET TRANSACTION READ ONLY;`). Gate: non-zero means stop
   and decide before changing behaviour.
2. **Failing test first** — a DML question in a category whose target resolves
   only in `public`: assert it refuses at capture instead of mutating `public`.
   Then the concurrency case the gate cannot currently cover: two categories,
   unqualified seeds hitting one shared table, asserting both grade `success`.
3. **The four-line change** plus comment updates (the ARCH-023 `pg_temp`
   comments describe the statement's shape and will go stale).
4. **Docs** — close the unqualified half in the spec's *Known limitations* and
   `feedback.md` OPEN-1; `CHANGELOG.md` with the re-run-the-audit deploy note;
   check `ARCHITECTURE.md` / `API_SPEC.md` for any `search_path` description
   this makes false.

## Open question for the human

Does a question captured before this change get **re-captured automatically**, or
does the lecturer re-save it? Auto re-capture is the kinder behaviour but runs
lecturer SQL unattended at deploy time; leaving it manual means the audit output
is a worklist someone must actually work through. Option A chose manual, but its
audit came back empty, so the choice was never really tested.

## Recommendation

Worth doing, and the residual it closes is the likelier one. It is a small,
well-bounded code change sitting behind a genuinely load-bearing audit — the
risk is entirely in existing data, not in the four lines. Do not start it
without a database that has real practice categories and exams on it, or the
verification will be as vacuous as option A's was.

## Corrections (2026-08-10, added after implementation and review)

This section is appended, not merged into the body above. The body is what
was reviewed before the branch started and is kept intact as that record;
these are the three claims in it that turned out to be wrong, established by
reading the code before planning the actual change
(`docs/superpowers/plans/2026-08-10-option-b-drop-public-from-dml-search-path.md`,
recorded in `.superpowers/sdd/progress.md`).

1. **`target_table` was never in the blast radius.** The "What actually
   breaks" section above, and proposed task 2's failing test, both treat
   `target_table` as something this change could newly refuse. It cannot:
   `ResolveTargetTable` (`postgres_target_table.go:54-62`) resolves the
   lecturer's raw table name against `pg_class` **in the question's own
   schema only**, and both capture paths call it **before** capture runs
   (`postgres_practice.go:394`, `postgres_questions.go:94`). A target that
   exists only in `public` is already refused today, independent of this
   branch — that refusal is the pre-existing `target_table` case-bug fix, not
   this change.

2. **The real residual is non-target tables**, not `target_table`: unqualified
   references inside `correct_sql` — `JOIN`/`FROM`/subquery targets — and
   unqualified `INSERT` targets inside `seed_sql`. Nothing pre-resolves those
   the way `ResolveTargetTable` pre-resolves the target, so they were the only
   things this change actually needed to close. The proposed audit sketch
   above ("resolve `target_table` … and any unqualified table named in
   `correct_sql` / `seed_sql`") already accounted for this correctly for
   `correct_sql`/`seed_sql`; the correction is that `target_table` did not
   need to be in that sketch at all.

3. **Evidence item 5 reached the right conclusion by the wrong route.** It
   claims `evaluateDML` calls none of `postgres_core.go`'s
   `queryWithTx`/`queryWithTxCols`, "verified: no `queryWithTx` reference in
   `postgres_evaluation.go`". That verification method was too narrow: the
   **capture sites** (`captureExpectedResult` and its practice equivalent) DO
   call `queryWithTxCols`, which re-sets `search_path` to `<schema>, public`
   after `evaluateDML`'s own narrower `SET LOCAL search_path` has already run
   earlier in the same transaction. `postgres_core.go` is genuinely on a DML
   capture path — the claim that it is "NOT on this path" is false as
   written. The conclusion (leave `postgres_core.go` alone) is still correct,
   but for a different reason: the *only* DML statement capture ever routes
   through `queryWithTxCols` is the post-image `SELECT * FROM target_table`
   after the lecturer's own SQL has already run, and finding 1 above
   guarantees `target_table` already resolves in the owning schema — so the
   re-widened `search_path` on that one `SELECT` never has an unqualified name
   left for `public` to catch. Safe by construction, not by absence of the
   call.

**The proposed concurrency test in task 2 above no longer describes real
behaviour.** It suggested asserting that two categories with unqualified
seeds hitting one shared `public` table "both grade success" — i.e. that the
schema gate alone was insufficient to stop the deadlock, so both would
proceed to grade (successfully) against the shared table with no mutual
exclusion at all. After this change, an unqualified seed naming a table
absent from its own category schema fails to resolve — there is no shared
table left to reach through `search_path`, so the seed errors at capture or
replay instead of executing. The test that shipped
(`postgres_public_fallback_test.go`, added in `9113c24`/`df57eef`/`2fd72cf`)
asserts the opposite of the original proposal: that the reverted (buggy)
`, public` path lets the shared table be reached, and the fixed path refuses
it — a revert-check, not a "both succeed" concurrency assertion.
