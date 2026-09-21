// Aggregations over the in-memory session store. Backs the query_local MCP tool.
// Pure functions over recorded telemetry — no c8y round-trips.

import type { DiagnosticSession, Pass } from './types'

export interface SeriesStat {
  series: string
  unit?: string
  count: number
  min: number
  max: number
  avg: number
  firstTime: string
  lastTime: string
}

export interface AlarmTally {
  type: string
  severity: string
  status: string
  count: number
}

export interface EventTally {
  type: string
  count: number
}

export interface PassSummary {
  pass: Pass
  measurementStats: SeriesStat[]
  alarmTallies: AlarmTally[]
  eventTallies: EventTally[]
}

export function summarizePass(session: DiagnosticSession, pass: Pass): PassSummary {
  const measurements = session.measurements.filter((m) => m.pass === pass)
  const alarms = session.alarms.filter((a) => a.pass === pass)
  const events = session.events.filter((e) => e.pass === pass)

  // Per-series min/max/avg.
  const bySeries = new Map<string, typeof measurements>()
  for (const m of measurements) {
    const arr = bySeries.get(m.series) ?? []
    arr.push(m)
    bySeries.set(m.series, arr)
  }
  const measurementStats: SeriesStat[] = [...bySeries.entries()].map(([series, rows]) => {
    const values = rows.map((r) => r.value)
    const times = rows.map((r) => r.time).sort()
    return {
      series,
      unit: rows[0]?.unit,
      count: rows.length,
      min: Math.min(...values),
      max: Math.max(...values),
      avg: values.reduce((s, v) => s + v, 0) / values.length,
      firstTime: times[0],
      lastTime: times[times.length - 1],
    }
  })

  // Alarm tallies keyed by type+severity+status.
  const alarmMap = new Map<string, AlarmTally>()
  for (const a of alarms) {
    const key = `${a.type}|${a.severity}|${a.status}`
    const t = alarmMap.get(key) ?? {
      type: a.type,
      severity: a.severity,
      status: a.status,
      count: 0,
    }
    t.count += a.count
    alarmMap.set(key, t)
  }

  // Event tallies keyed by type.
  const eventMap = new Map<string, EventTally>()
  for (const e of events) {
    const t = eventMap.get(e.type) ?? { type: e.type, count: 0 }
    t.count += 1
    eventMap.set(e.type, t)
  }

  return {
    pass,
    measurementStats,
    alarmTallies: [...alarmMap.values()],
    eventTallies: [...eventMap.values()],
  }
}

/** Both passes, side by side — what the agent reasons over before concluding. */
export function summarizeSession(session: DiagnosticSession) {
  return {
    sessionId: session.id,
    deviceId: session.deviceId,
    deviceType: session.deviceType,
    status: session.status,
    counts: {
      measurements: session.measurements.length,
      alarms: session.alarms.length,
      events: session.events.length,
    },
    pass1: summarizePass(session, 1),
    pass2: summarizePass(session, 2),
  }
}
