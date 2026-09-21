// clear_session — drop all buffered telemetry for the session from memory.
// Mandatory final workflow step; nothing diagnostic outlives the diagnosis.

import { clearSession as wipe } from '../../store/session-store'

export interface ClearSessionInput {
  sessionId: string
}

export function clearSession(input: ClearSessionInput): { cleared: boolean } {
  return { cleared: wipe(input.sessionId) }
}
