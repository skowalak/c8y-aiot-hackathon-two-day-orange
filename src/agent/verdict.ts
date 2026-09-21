// Parse + validate the model's final verdict JSON.

import { z } from 'zod'

export const verdictSchema = z.object({
  faulty: z.boolean(),
  faultDomain: z.enum(['measurement', 'alarm', 'event', 'none']),
  rootCause: z.string(),
  confidence: z.number().min(0).max(1),
  evidence: z.array(
    z.object({
      pass: z.union([z.literal(1), z.literal(2)]),
      signal: z.string(),
      observed: z.string(),
      expected: z.string(),
      verdict: z.string(),
    }),
  ),
  recommendation: z.string(),
})

export type ParsedVerdict = z.infer<typeof verdictSchema>

/** Extract the first JSON object from a model response and validate it. */
export function parseVerdict(text: string): ParsedVerdict {
  const start = text.indexOf('{')
  const end = text.lastIndexOf('}')
  if (start === -1 || end === -1 || end < start) {
    throw new Error('No JSON object found in model verdict response')
  }
  const json = JSON.parse(text.slice(start, end + 1))
  return verdictSchema.parse(json)
}
