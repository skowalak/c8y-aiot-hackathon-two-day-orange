// In-memory session registry — the entire "store". No database, no disk.
// Lifecycle: running -> stopped -> cleared. See §5 of the functional description.

import type {
  AlarmRecord,
  DiagnosticSession,
  EventRecord,
  MeasurementRecord,
} from './types'

const sessions = new Map<string, DiagnosticSession>()

/** Opens a session and stamps its start time. */
export function startSession(
  id: string,
  deviceId: string,
  deviceType?: string,
): DiagnosticSession {
  const now = new Date().toISOString()
  const session: DiagnosticSession = {
    id,
    deviceId,
    deviceType,
    createdAt: now,
    startedAt: now,
    status: 'running',
    measurements: [],
    alarms: [],
    events: [],
  }
  sessions.set(id, session)
  return session
}

export function getSession(id: string): DiagnosticSession | undefined {
  return sessions.get(id)
}

/** Throws if the session is missing — used by tools that must have live state. */
export function requireSession(id: string): DiagnosticSession {
  const session = sessions.get(id)
  if (!session) throw new Error(`Unknown diagnostic session: ${id}`)
  return session
}

export function appendMeasurements(id: string, rows: MeasurementRecord[]): void {
  requireSession(id).measurements.push(...rows)
}

export function appendAlarms(id: string, rows: AlarmRecord[]): void {
  requireSession(id).alarms.push(...rows)
}

export function appendEvents(id: string, rows: EventRecord[]): void {
  requireSession(id).events.push(...rows)
}

/** Marks the session stopped and stamps its stop time (verdict ready). */
export function stopSession(id: string): void {
  const session = sessions.get(id)
  if (!session) return
  session.stoppedAt = new Date().toISOString()
  session.status = 'stopped'
}

/** Final workflow step: wipes all buffered telemetry for GC. */
export function clearSession(id: string): boolean {
  return sessions.delete(id)
}

/** Total diagnosis duration in ms, if the session has both timestamps. */
export function sessionDurationMs(session: DiagnosticSession): number | undefined {
  if (!session.stoppedAt) return undefined
  return Date.parse(session.stoppedAt) - Date.parse(session.startedAt)
}
