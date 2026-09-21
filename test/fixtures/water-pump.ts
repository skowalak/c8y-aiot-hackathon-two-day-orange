// Simulated Cumulocity telemetry for a faulty water pump.
//
// Failure scenario (from src/knowledge/c8y_Water_Pump.md, "Known Failure
// Signatures"): "Pressure normal but flow zero -> mechanical blockage
// (measurement domain)". The pump reports normal pressure (~4 bar) but flow rate
// reads 0 l/min, and this reproduces across BOTH sampling passes (persistent,
// not a transient blip).
//
// The fixture returns RAW Cumulocity REST JSON so the tests exercise the real
// parsing/flattening code in src/c8y/client.ts, not a pre-digested shape.

import type { C8yFetcher } from '../../src/c8y/client'

export const FAULTY_PUMP_DEVICE_ID = 'pump-4711'
export const PUMP_DEVICE_TYPE = 'c8y_Water_Pump'

function measurement(time: string, pressure: number, flow: number) {
  return {
    id: `${Date.parse(time)}`,
    self: 'https://example/measurement',
    time,
    type: PUMP_DEVICE_TYPE,
    source: { id: FAULTY_PUMP_DEVICE_ID },
    c8y_Pressure: { P: { value: pressure, unit: 'bar' } },
    c8y_FlowRate: { F: { value: flow, unit: 'l/min' } },
  }
}

// A window of samples: pressure healthy (2.0–6.0 normal), flow flat at 0.
function faultyWindow(baseIso: string, n = 6) {
  const base = Date.parse(baseIso)
  const rows = []
  for (let i = 0; i < n; i++) {
    const time = new Date(base + i * 60_000).toISOString()
    // Pressure normal ~4.0–4.3 bar; flow stuck at 0 (mechanical blockage).
    rows.push(measurement(time, 4.0 + (i % 3) * 0.1, 0))
  }
  return rows
}

// A healthy window for the control test (flow in normal 20–120 range).
function healthyWindow(baseIso: string, n = 6) {
  const base = Date.parse(baseIso)
  const rows = []
  for (let i = 0; i < n; i++) {
    const time = new Date(base + i * 60_000).toISOString()
    rows.push(measurement(time, 4.0 + (i % 3) * 0.1, 60 + (i % 5) * 2))
  }
  return rows
}

/**
 * Build a C8yFetcher that serves simulated pump telemetry. Routes by URL path:
 * inventory -> device type, measurement/alarm/event -> the given windows.
 *
 * `faulty` controls whether flow is stuck at 0 (measurement-domain fault) or
 * healthy. Alarms/events are routine in both cases so the fault is unambiguously
 * in the measurement domain.
 */
export function makePumpFetcher(opts: { faulty: boolean } = { faulty: true }): C8yFetcher {
  return async (path: string) => {
    if (path.startsWith('/inventory/managedObjects/')) {
      return { id: FAULTY_PUMP_DEVICE_ID, type: PUMP_DEVICE_TYPE, name: 'Pump 4711' }
    }
    if (path.startsWith('/measurement/measurements')) {
      // Both passes query recent windows; return the same faulty/healthy shape
      // each time so the condition reproduces across passes.
      const rows = opts.faulty
        ? faultyWindow('2026-09-21T09:00:00.000Z')
        : healthyWindow('2026-09-21T09:00:00.000Z')
      return { measurements: rows }
    }
    if (path.startsWith('/alarm/alarms')) {
      // Only a routine maintenance alarm — knowledge base says ignore it.
      return {
        alarms: [
          {
            time: '2026-09-21T09:01:00.000Z',
            type: 'c8y_MaintenanceDue',
            severity: 'MINOR',
            status: 'ACTIVE',
            text: 'Routine maintenance due',
            count: 1,
          },
        ],
      }
    }
    if (path.startsWith('/event/events')) {
      // Normal start/stop pair — nothing wrong in the event domain.
      return {
        events: [
          { time: '2026-09-21T09:00:00.000Z', type: 'c8y_PumpStarted', text: 'Pump started' },
        ],
      }
    }
    throw new Error(`Unexpected c8y path in test fetcher: ${path}`)
  }
}
