export const UI_COPY = {
  // Auth
  LOGIN: 'Login',
  LOGIN_LOADING: 'Signing in',
  LOGOUT: 'Logout',

  // Generic actions
  SAVE: 'Save',
  SAVE_LOADING: 'Saving',
  CANCEL: 'Cancel',
  DELETE: 'Delete',
  DELETE_LOADING: 'Deleting',
  EDIT: 'Edit',
  CREATE: 'Create',
  IMPORT: 'Import',

  // SQL workflow
  SUBMIT_ANSWER: 'Submit Answer',
  SUBMIT_ANSWER_LOADING: 'Submitting',
  TEST_RUN: 'Test Run',
  TEST_RUN_LOADING: 'Running...',
  PREVIEW: 'Preview',
  FORMAT_SQL: 'Format SQL',

  // Exam admin
  ACTIVATE_EXAM: 'Activate Exam',
  DEACTIVATE_EXAM: 'Deactivate Exam',
  ASSIGN_STUDENTS: 'Assign Students',
  ENROLL_STUDENTS: 'Enroll Students',
  EXAM_ROSTER: 'Exam Roster',
  ACTIVATE_NOW: 'Activate Now',
  SCHEDULE_ACTIVATION: 'Schedule Activation',
  DEACTIVATE_NOW: 'Deactivate Now',
  SET_END_TIME: 'Set End Time',
  NEW_PRACTICE_QUESTION: 'New Practice Question',
  EXPORT_SCORES: 'Export Scores (CSV)',
  RENAME_EXAM: 'Rename',
  DELETE_EXAM: 'Delete',
  DELETE_EXAM_CONFIRM: 'This permanently removes the exam and its questions, roster, and submissions. Continue?',

  // Schema / curriculum import
  IMPORT_EXAM_SCHEMA: 'Import Schema & Data',
  REPLACE_EXAM_SCHEMA_CONFIRM: 'This exam already has a curriculum schema. Importing will drop the existing tables and replace them with the new data. An active exam cannot be re-imported. Continue?',

  // Practice categories
  MANAGE_CATEGORIES: 'Practices',
  NEW_CATEGORY: 'New Practice',
  RENAME_CATEGORY: 'Rename',
  DELETE_CATEGORY: 'Delete',
  IMPORT_CATEGORY_DB: 'Import Database',
  DELETE_CATEGORY_CONFIRM: 'This category still has practice questions. Deleting it will permanently remove those questions, their student attempts, and the category database. Continue?',
  REPLACE_CATEGORY_DB_CONFIRM: 'This category already has a database. Importing will drop the existing tables and replace them. Continue?',

  // Multi-select toolbar
  SELECT_ALL: 'Select All',
  SELECT_NONE: 'Select None',
} as const

export type UICopyKey = keyof typeof UI_COPY
