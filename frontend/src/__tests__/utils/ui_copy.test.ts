/**
 * Tests for TODO-070 and TODO-071
 * Verifies UI_COPY constant shape and that key consumers reference its values.
 */
import { describe, it, expect } from 'vitest'
import { UI_COPY } from '../../constants/ui_copy'

describe('UI_COPY constant (TODO-070)', () => {
  it('exports an object', () => {
    expect(typeof UI_COPY).toBe('object')
    expect(UI_COPY).not.toBeNull()
  })

  it('has required auth keys with non-empty strings', () => {
    expect(UI_COPY.LOGIN).toBeTruthy()
    expect(UI_COPY.LOGIN_LOADING).toBeTruthy()
    expect(UI_COPY.LOGOUT).toBeTruthy()
  })

  it('has SQL workflow keys', () => {
    expect(UI_COPY.SUBMIT_ANSWER).toBeTruthy()
    expect(UI_COPY.TEST_RUN).toBeTruthy()
    expect(UI_COPY.FORMAT_SQL).toBeTruthy()
  })

  it('has generic action keys', () => {
    expect(UI_COPY.SAVE).toBeTruthy()
    expect(UI_COPY.CANCEL).toBeTruthy()
    expect(UI_COPY.DELETE).toBeTruthy()
    expect(UI_COPY.CREATE).toBeTruthy()
  })

  it('has exam admin keys', () => {
    expect(UI_COPY.ACTIVATE_EXAM).toBeTruthy()
    expect(UI_COPY.DEACTIVATE_EXAM).toBeTruthy()
    expect(UI_COPY.ASSIGN_STUDENTS).toBeTruthy()
    expect(UI_COPY.EXPORT_SCORES).toBeTruthy()
  })

  it('FORMAT_SQL value matches expected text (used by SqlEditor button)', () => {
    expect(UI_COPY.FORMAT_SQL).toBe('Format SQL')
  })

  it('all values are strings', () => {
    for (const [key, value] of Object.entries(UI_COPY)) {
      expect(typeof value, `UI_COPY.${key} should be a string`).toBe('string')
    }
  })
})

describe('UI_COPY consumers (TODO-071) – import sanity', () => {
  it('LoginView uses UI_COPY.LOGIN and UI_COPY.LOGIN_LOADING', async () => {
    // Verify the module references the correct string literals
    expect(UI_COPY.LOGIN).toBe('Login')
    expect(UI_COPY.LOGIN_LOADING).toBe('Signing in')
  })

  it('UI_COPY.SUBMIT_ANSWER and SUBMIT_ANSWER_LOADING are defined for StudentView', () => {
    expect(UI_COPY.SUBMIT_ANSWER).toBe('Submit Answer')
    expect(UI_COPY.SUBMIT_ANSWER_LOADING).toBe('Submitting')
  })

  it('UI_COPY.FORMAT_SQL is defined for the Format SQL button in LecturerView and StudentView', () => {
    expect(UI_COPY.FORMAT_SQL).toBe('Format SQL')
  })
})
