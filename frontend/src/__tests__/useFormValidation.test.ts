import { describe, it, expect } from 'vitest'
import { useFormValidation, required, minLength, maxLength } from '../composables/useFormValidation'

// ---------------------------------------------------------------------------
// Built-in validators
// ---------------------------------------------------------------------------
describe('required validator', () => {
  const v = required('Field is required')

  it('returns error on empty string', () => {
    expect(v('')).toBe('Field is required')
  })

  it('returns error on null', () => {
    expect(v(null)).toBe('Field is required')
  })

  it('returns error on undefined', () => {
    expect(v(undefined)).toBe('Field is required')
  })

  it('passes on non-empty string', () => {
    expect(v('hello')).toBeNull()
  })

  it('passes on a string of spaces (required does NOT trim — whitespace is a value)', () => {
    // Per the implementation: v == null || v === '' triggers error; '   ' is not ''
    expect(v('   ')).toBeNull()
  })

  it('uses default message when none supplied', () => {
    expect(required()('')).toBe('Required')
  })
})

describe('minLength validator', () => {
  const v = minLength(5)

  it('returns error when value is shorter than n', () => {
    expect(v('abc')).toBe('Min 5 characters')
  })

  it('passes when value length equals n', () => {
    expect(v('abcde')).toBeNull()
  })

  it('passes when value is longer than n', () => {
    expect(v('abcdefg')).toBeNull()
  })

  it('passes (returns null) when value is empty/null/undefined (not the minLength validator\'s job to require)', () => {
    // Implementation: `v && v.length < n` — falsy v short-circuits to null
    expect(v('')).toBeNull()
    expect(v(null as any)).toBeNull()
    expect(v(undefined as any)).toBeNull()
  })

  it('uses custom message', () => {
    expect(minLength(3, 'Too short')('ab')).toBe('Too short')
  })
})

describe('maxLength validator', () => {
  const v = maxLength(5)

  it('returns error when value is longer than n', () => {
    expect(v('abcdef')).toBe('Max 5 characters')
  })

  it('passes when value length equals n', () => {
    expect(v('abcde')).toBeNull()
  })

  it('passes when value is shorter than n', () => {
    expect(v('abc')).toBeNull()
  })

  it('passes (returns null) on empty/null/undefined', () => {
    expect(v('')).toBeNull()
    expect(v(null as any)).toBeNull()
  })

  it('uses custom message', () => {
    expect(maxLength(3, 'Too long')('abcd')).toBe('Too long')
  })
})

// ---------------------------------------------------------------------------
// useFormValidation composable
// ---------------------------------------------------------------------------
describe('useFormValidation composable', () => {
  function makeForm() {
    return useFormValidation({
      username: { rules: [required('Username required')], initial: '' },
      email: { rules: [required('Email required'), minLength(5, 'Email too short')], initial: '' },
    })
  }

  describe('initial state', () => {
    it('errors start as empty strings', () => {
      const form = makeForm()
      expect(form.errors.username).toBe('')
      expect(form.errors.email).toBe('')
    })

    it('touched starts as false', () => {
      const form = makeForm()
      expect(form.touched.username).toBe(false)
      expect(form.touched.email).toBe(false)
    })

    it('values start at initial values', () => {
      const form = makeForm()
      expect(form.values.username).toBe('')
      expect(form.values.email).toBe('')
    })
  })

  describe('validateField', () => {
    it('returns false and sets errors[key] when validation fails', () => {
      const form = makeForm()
      const ok = form.validateField('username')
      expect(ok).toBe(false)
      expect(form.errors.username).toBe('Username required')
    })

    it('returns true and clears errors[key] when validation passes', () => {
      const form = makeForm()
      form.values.username = 'alice'
      const ok = form.validateField('username')
      expect(ok).toBe(true)
      expect(form.errors.username).toBe('')
    })

    it('returns first failing validator message (stops at first failure)', () => {
      const form = makeForm()
      // email is '' — both required and minLength would fail, but required fires first
      const ok = form.validateField('email')
      expect(ok).toBe(false)
      expect(form.errors.email).toBe('Email required')
    })

    it('shows second validator message when first passes but second fails', () => {
      const form = makeForm()
      form.values.email = 'ab' // not empty but too short
      const ok = form.validateField('email')
      expect(ok).toBe(false)
      expect(form.errors.email).toBe('Email too short')
    })
  })

  describe('validateAll', () => {
    it('marks touched=true for every key', () => {
      const form = makeForm()
      form.validateAll()
      expect(form.touched.username).toBe(true)
      expect(form.touched.email).toBe(true)
    })

    it('returns false when any field is invalid', () => {
      const form = makeForm()
      expect(form.validateAll()).toBe(false)
    })

    it('returns true when all fields are valid', () => {
      const form = makeForm()
      form.values.username = 'alice'
      form.values.email = 'alice@x.com'
      expect(form.validateAll()).toBe(true)
    })

    it('populates errors for all failing fields', () => {
      const form = makeForm()
      form.validateAll()
      expect(form.errors.username).toBe('Username required')
      expect(form.errors.email).toBe('Email required')
    })
  })

  describe('markTouched', () => {
    it('sets touched[key]=true without revalidating', () => {
      const form = makeForm()
      expect(form.touched.username).toBe(false)
      expect(form.errors.username).toBe('')

      form.markTouched('username')

      expect(form.touched.username).toBe(true)
      // errors must still be empty — markTouched does NOT validate
      expect(form.errors.username).toBe('')
    })
  })

  describe('reset', () => {
    it('clears touched, errors, and restores initial values', () => {
      const form = makeForm()
      form.values.username = 'alice'
      form.validateAll()

      form.reset()

      expect(form.touched.username).toBe(false)
      expect(form.touched.email).toBe(false)
      expect(form.errors.username).toBe('')
      expect(form.errors.email).toBe('')
      expect(form.values.username).toBe('')
      expect(form.values.email).toBe('')
    })
  })

  describe('isValid computed', () => {
    it('is false when any error string is non-empty', () => {
      const form = makeForm()
      form.validateAll()
      expect(form.isValid.value).toBe(false)
    })

    it('is true when all errors are empty strings', () => {
      const form = makeForm()
      form.values.username = 'alice'
      form.values.email = 'alice@x.com'
      form.validateAll()
      expect(form.isValid.value).toBe(true)
    })
  })

  describe('touched-then-input flow', () => {
    it('after markTouched, calling validateField reflects current value', () => {
      const form = makeForm()
      form.markTouched('username')
      expect(form.errors.username).toBe('') // not yet validated

      // Now validate — should report error since value is still empty
      form.validateField('username')
      expect(form.errors.username).toBe('Username required')

      // Update value and re-validate — error should clear
      form.values.username = 'alice'
      form.validateField('username')
      expect(form.errors.username).toBe('')
    })
  })
})
