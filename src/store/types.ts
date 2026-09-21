// In-memory session model. Mirrors §5 of docs/FUNCTIONAL_DESCRIPTION.md.
// Recorded telemetry is transient: it lives only in process memory and is
// wiped after the final diagnosis (see clearSession in session-store.ts).

export type Pass = 1 | 2 // 1 = first sampling, 2 = second sampling

export type SessionStatus = 'running' | 'stopped'

export interface MeasurementRecord {
  pass: Pass
  time: string // ISO timestamp from c8y
  type: string // e.g. c8y_Temperature
  series: string // fragment.series
  unit?: string
  value: number
}

export interface AlarmRecord {
  pass: Pass
  time: string
  type: string
  severity: 'CRITICAL' | 'MAJOR' | 'MINOR' | 'WARNING'
  status: 'ACTIVE' | 'ACKNOWLEDGED' | 'CLEARED'
  text: string
  count: number
}

export interface EventRecord {
  pass: Pass
  time: string
  type: string
  text: string
}

export interface DiagnosticSession {
  id: string // diagnostic session id
  deviceId: string
  deviceType?: string
  createdAt: string
  startedAt: string // ISO — when diagnosis started (first pass began)
  stoppedAt?: string // ISO — when the final diagnosis was produced; unset while running
  status: SessionStatus
  measurements: MeasurementRecord[]
  alarms: AlarmRecord[]
  events: EventRecord[]
}

// The three fault domains the agent must discriminate between.
export type FaultDomain = 'measurement' | 'alarm' | 'event' | 'none'

export interface EvidenceItem {
  pass: Pass
  signal: string
  observed: string
  expected: string
  verdict: string
}

export interface DiagnosticVerdict {
  deviceId: string
  deviceType?: string
  faulty: boolean
  faultDomain: FaultDomain
  rootCause: string
  confidence: number
  evidence: EvidenceItem[]
  recommendation: string
  reportUrl?: string
  startedAt?: string
  stoppedAt?: string
  durationMs?: number
}
