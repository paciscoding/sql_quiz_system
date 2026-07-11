# Design: .sql File Upload in Exam Schema Panel

**Date:** 2026-07-11
**Status:** Approved
**Scope:** `frontend/src/components/lecturer/ExamSchemaPanel.vue` (frontend only)

## Problem

Lecturers initialize an exam curriculum by pasting SQL into a `SqlEditor`
(`ExamSchemaPanel.vue`), then clicking Import. There is no way to load a whole
`.sql` file — the lecturer must open the file elsewhere, copy, and paste. Add a
file-upload option that loads a `.sql` file's contents into the existing editor,
alongside the current paste flow.

## Approach

Read the chosen file client-side and populate the existing `importSQL` model.
No backend change, no new endpoint. Upload fills the editor; the lecturer
reviews/edits and clicks Import, reusing the current
`importExamCurriculum(examId, sql, replace)` path and its 409 replace-confirm.

Rejected alternatives:
- *Upload imports immediately* — skips review; higher risk of importing the
  wrong file.
- *Send raw file to a new multipart backend endpoint* — more work; unnecessary
  since curriculum SQL is small text and the import endpoint already accepts a
  SQL string.

## UI

Add above the `SqlEditor`:
- A visible button labeled "Choose .sql file".
- A hidden `<input type="file" accept=".sql,.txt">`; the button proxies clicks
  to it via a `ref`.

The Import button, editor, and all existing markup stay unchanged.

## Behavior

`loadFile(file: File)`:
1. Reject if `file.size > MAX_SQL_FILE_BYTES` (2 MB) → `notify('File too large
   (max 2 MB)', 'error')`, return.
2. `const text = await file.text()`.
3. On success → `importSQL.value = text`, `notify('Loaded ' + file.name,
   'success')`.
4. On read failure → `notify('Could not read file', 'error')`.

Input `change` handler: pull `files[0]`, call `loadFile`, then reset
`input.value = ''` so re-picking the same file fires `change` again.

Empty file loads an empty editor — the Import button is already disabled on
empty/whitespace, so no extra guard needed.

## Constants

`MAX_SQL_FILE_BYTES = 2 * 1024 * 1024` — local const in the component.

## Testing

Unit test the `loadFile` behavior (extract as a small helper or test via the
component). Cases:
- Happy path: valid file → `importSQL` set to file text, success notify.
- Oversize: `size > MAX_SQL_FILE_BYTES` → editor unchanged, error notify.
- Read failure: `file.text()` rejects → error notify, editor unchanged.

## Out of Scope

- Backend changes.
- Drag-and-drop.
- Multi-file / concatenation.
- Server-side file storage.
