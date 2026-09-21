// record_window — fetch alarms + measurements + events for a device/time window
// and buffer them in the in-memory session store under the given pass.
//
// Takes a C8yFetcher (injectable) rather than the raw event so it can be driven
// with simulated telemetry in tests.

import { fetchAlarms, fetchEvents, fetchMeasurements } from '../../c8y/client'
import type { C8yFetcher } from '../../c8y/client'
import {
  appendAlarms,
  appendEvents,
  appendMeasurements,
  requireSession,
} from '../../store/session-store'
import type { Pass } from '../../store/types'

export interface RecordWindowInput {
  sessionId: string
  deviceId: string
  from: string // ISO
  to: string // ISO
  pass: Pass
}

export interface RecordWindowSummary {
  pass: Pass
  window: { from: string; to: string }
  counts: { measurements: number; alarms: number; events: number }
  seriesSeen: string[]
}

export async function recordWindow(
  fetch: C8yFetcher,
  input: RecordWindowInput,
): Promise<RecordWindowSummary> {
  const { sessionId, deviceId, from, to, pass } = input
  requireSession(sessionId) // fail fast if session was cleared/never started

  const window = { from, to }
  const [measurements, alarms, events] = await Promise.all([
    fetchMeasurements(fetch, deviceId, window, pass),
    fetchAlarms(fetch, deviceId, window, pass),
    fetchEvents(fetch, deviceId, window, pass),
  ])

  appendMeasurements(sessionId, measurements)
  appendAlarms(sessionId, alarms)
  appendEvents(sessionId, events)

  return {
    pass,
    window,
    counts: {
      measurements: measurements.length,
      alarms: alarms.length,
      events: events.length,
    },
    seriesSeen: [...new Set(measurements.map((m) => m.series))],
  }
}
