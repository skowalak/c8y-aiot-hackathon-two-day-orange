// query_local — aggregate the in-memory session store so the agent reasons over
// recorded data without re-hitting Cumulocity.

import { requireSession } from '../../store/session-store'
import { summarizePass, summarizeSession } from '../../store/analysis'
import type { Pass } from '../../store/types'

export interface QueryLocalInput {
  sessionId: string
  pass?: Pass // omit for a both-passes summary
}

export function queryLocal(input: QueryLocalInput) {
  const session = requireSession(input.sessionId)
  if (input.pass) return summarizePass(session, input.pass)
  return summarizeSession(session)
}
