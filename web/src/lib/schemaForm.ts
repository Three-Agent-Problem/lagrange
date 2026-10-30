// Structured input (a2aext/structuredinput) schemas as forms. A port of the console's
// formFields/formData: flat objects with primitive properties, which covers the
// approval-style questions agents ask.
import type { JSONSchema } from '@/lib/api'

export type Field = {
  name: string
  title: string
  description?: string
  /** "decision" is a lone boolean, shown as two buttons. */
  kind: 'decision' | 'checkbox' | 'select' | 'number' | 'integer' | 'text'
  options: string[]
  required: boolean
}

export function schemaType(s: JSONSchema | undefined): string {
  if (!s?.type) return ''
  if (typeof s.type === 'string') return s.type
  return s.type.find((t) => t !== 'null') ?? ''
}

export function formFields(schema: JSONSchema | undefined): Field[] {
  const props = schema?.properties ?? {}
  const names = Object.keys(props).sort()
  const booleans = names.filter((n) => schemaType(props[n]) === 'boolean').length
  const fields = names.map((name): Field => {
    const p = props[name]
    const t = schemaType(p)
    const f: Field = { name, title: p.title || name, description: p.description, kind: 'text', options: [], required: schema?.required?.includes(name) ?? false }
    if (t === 'boolean') f.kind = booleans === 1 ? 'decision' : 'checkbox'
    else if (p.enum?.length) {
      f.kind = 'select'
      f.options = p.enum.map(String)
    } else if (t === 'integer') f.kind = 'integer'
    else if (t === 'number') f.kind = 'number'
    return f
  })
  // Decision buttons submit the form, so they go last.
  return [...fields.filter((f) => f.kind !== 'decision'), ...fields.filter((f) => f.kind === 'decision')]
}

/** Converts form values to JSON that matches the schema, or returns an error message. */
export function formData(schema: JSONSchema, values: Record<string, string | boolean>): { data: Record<string, unknown> } | { error: string } {
  const data: Record<string, unknown> = {}
  for (const [name, p] of Object.entries(schema.properties ?? {})) {
    const v = values[name]
    const required = schema.required?.includes(name) ?? false
    switch (schemaType(p)) {
      case 'boolean':
        data[name] = v === true || v === 'true' || v === 'on'
        break
      case 'integer':
      case 'number': {
        if (v === undefined || v === '') {
          if (required) return { error: `${p.title || name} is required` }
          break
        }
        const n = Number(v)
        if (!Number.isFinite(n) || (schemaType(p) === 'integer' && !Number.isInteger(n))) {
          return { error: `${p.title || name} must be a ${schemaType(p) === 'integer' ? 'whole number' : 'number'}` }
        }
        data[name] = n
        break
      }
      default:
        if ((v !== undefined && v !== '') || required) data[name] = v ?? ''
    }
  }
  return { data }
}
