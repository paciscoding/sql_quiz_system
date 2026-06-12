import { reactive, computed, type ComputedRef } from 'vue'

export type Validator<T = any> = (value: T) => string | null

export interface FieldConfig<T = any> {
  rules: Validator<T>[]
  initial?: T
}

export interface UseFormValidationReturn<F extends Record<string, FieldConfig>> {
  errors: Record<keyof F, string>
  touched: Record<keyof F, boolean>
  values: Record<keyof F, any>
  isValid: ComputedRef<boolean>
  validateField(name: keyof F): boolean
  validateAll(): boolean
  reset(): void
  markTouched(name: keyof F): void
}

export function useFormValidation<F extends Record<string, FieldConfig>>(fields: F): UseFormValidationReturn<F> {
  const keys = Object.keys(fields) as Array<keyof F>

  const errors = reactive(
    Object.fromEntries(keys.map((k) => [k, '']))
  ) as Record<keyof F, string>

  const touched = reactive(
    Object.fromEntries(keys.map((k) => [k, false]))
  ) as Record<keyof F, boolean>

  const values = reactive(
    Object.fromEntries(keys.map((k) => [k, fields[k].initial ?? '']))
  ) as Record<keyof F, any>

  const isValid = computed(() =>
    keys.every((k) => errors[k] === '')
  )

  function validateField(name: keyof F): boolean {
    const fieldCfg = fields[name]
    const value = values[name]
    let msg: string | null = null
    for (const rule of fieldCfg.rules) {
      const result = rule(value)
      if (result !== null) {
        msg = result
        break
      }
    }
    errors[name] = msg ?? ''
    return msg === null
  }

  function validateAll(): boolean {
    let allValid = true
    for (const k of keys) {
      touched[k] = true
      if (!validateField(k)) {
        allValid = false
      }
    }
    return allValid
  }

  function reset(): void {
    for (const k of keys) {
      touched[k] = false
      errors[k] = ''
      values[k] = fields[k].initial ?? ''
    }
  }

  function markTouched(name: keyof F): void {
    touched[name] = true
  }

  return { errors, touched, values, isValid, validateField, validateAll, reset, markTouched }
}

// Built-in validators
export const required = (msg = 'Required'): Validator => (v) =>
  v == null || v === '' ? msg : null

export const minLength = (n: number, msg?: string): Validator<string> => (v) =>
  v && v.length < n ? (msg || `Min ${n} characters`) : null

export const maxLength = (n: number, msg?: string): Validator<string> => (v) =>
  v && v.length > n ? (msg || `Max ${n} characters`) : null
