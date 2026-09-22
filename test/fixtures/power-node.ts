// Simulated Cumulocity telemetry for a 6-device Power Node fleet, matching the
// "Expected behaviour" envelope in TICKET.md (SIM-PN-100).
//
// One device (the culprit) shows the fault signature: voltage sagging below the
// 207 V under-voltage threshold, self-clearing sim_UnderVoltage alarms, and
// repeated sim_BrownoutReset events (unplanned restarts). The other five stay
// inside the healthy band. A firmware-update event is present on the culprit as a
// red herring.

import type { C8yFetcher } from '../../src/c8y/client'

export const POWER_NODE_TYPE = 'sim_PowerNode'

export interface FleetDevice {
  id: string
  faulty: boolean
}

// 6 devices: node-01 is the reported-faulty primary; the rest are healthy here
// (the fixture keeps the demo unambiguous — exactly one culprit to find).
export const FLEET: FleetDevice[] = [
  { id: 'dev-sim-node-01', faulty: true },
  { id: 'dev-sim-node-02', faulty: false },
  { id: 'dev-sim-node-03', faulty: false },
  { id: 'dev-sim-node-04', faulty: false },
  { id: 'dev-sim-node-05', faulty: false },
  { id: 'dev-sim-node-06', faulty: false },
]

function measurement(time: string, voltage: number, current: number, temp: number) {
  return {
    time,
    type: 'sim_PowerNodeTelemetry',
    sim_Voltage: { U: { value: voltage, unit: 'V' } },
    sim_Current: { I: { value: current, unit: 'A' } },
    sim_Temperature: { T: { value: temp, unit: 'C' } },
  }
}

function healthyWindow(baseIso: string, n = 8) {
  const base = Date.parse(baseIso)
  const rows = []
  for (let i = 0; i < n; i++) {
    const time = new Date(base + i * 60_000).toISOString()
    // Voltage 228–234 V (in band), current 10–13 A, temp 38–42 °C.
    rows.push(measurement(time, 230 + (i % 3) - 1, 11 + (i % 3), 40 + (i % 3) - 1))
  }
  return rows
}

function faultyWindow(baseIso: string, n = 8) {
  const base = Date.parse(baseIso)
  const rows = []
  for (let i = 0; i < n; i++) {
    const time = new Date(base + i * 60_000).toISOString()
    // Brownout sag: voltage drops to ~185–200 V (below 207 threshold); current
    // spikes (constant-power load); temp stays in band (rules out thermal).
    const sag = i % 2 === 0
    rows.push(
      sag
        ? measurement(time, 188 + (i % 3), 17 + (i % 2), 41)
        : measurement(time, 231, 12, 40),
    )
  }
  return rows
}

/** Build a C8yFetcher serving telemetry for one device of the fleet. */
export function makeFleetFetcher(): C8yFetcher {
  return async (path: string) => {
    // Which device is being queried? c8y filters carry source=<id>.
    const sourceMatch = /[?&]source=([^&]+)/.exec(path)
    const source = sourceMatch ? decodeURIComponent(sourceMatch[1]) : ''
    const device = FLEET.find((d) => d.id === source)

    // Inventory list query — device discovery by type (listDeviceIdsByType).
    if (path.startsWith('/inventory/managedObjects?')) {
      if (path.includes(encodeURIComponent(POWER_NODE_TYPE)) || path.includes(POWER_NODE_TYPE)) {
        return { managedObjects: FLEET.map((d) => ({ id: d.id, type: POWER_NODE_TYPE })) }
      }
      return { managedObjects: [] }
    }

    // Single managed object — device type resolution.
    if (path.startsWith('/inventory/managedObjects/')) {
      return { id: source || 'unknown', type: POWER_NODE_TYPE, name: 'Power Node' }
    }

    if (path.startsWith('/measurement/measurements')) {
      const rows =
        device?.faulty
          ? faultyWindow('2026-09-21T09:00:00.000Z')
          : healthyWindow('2026-09-21T09:00:00.000Z')
      return { measurements: rows }
    }

    if (path.startsWith('/alarm/alarms')) {
      if (device?.faulty) {
        // Self-clearing under-voltage alarms — the fault signature.
        return {
          alarms: [
            {
              time: '2026-09-21T09:00:00.000Z',
              type: 'sim_UnderVoltage',
              severity: 'MAJOR',
              status: 'CLEARED',
              text: 'Voltage below 207 V',
              count: 3,
            },
            {
              time: '2026-09-21T09:04:00.000Z',
              type: 'sim_UnderVoltage',
              severity: 'MAJOR',
              status: 'ACTIVE',
              text: 'Voltage below 207 V',
              count: 1,
            },
          ],
        }
      }
      return { alarms: [] }
    }

    if (path.startsWith('/event/events')) {
      if (device?.faulty) {
        return {
          events: [
            // Red herring firmware update:
            {
              time: '2026-09-14T02:00:00.000Z',
              type: 'sim_FirmwareUpdated',
              text: 'Firmware updated to 1.4.2',
            },
            // The real symptom — unplanned restarts coinciding with the dips:
            {
              time: '2026-09-21T09:00:30.000Z',
              type: 'sim_BrownoutReset',
              text: 'Controller brown-out reset',
            },
            {
              time: '2026-09-21T09:02:30.000Z',
              type: 'sim_BrownoutReset',
              text: 'Controller brown-out reset',
            },
          ],
        }
      }
      return { events: [] }
    }

    throw new Error(`Unexpected c8y path in fleet fetcher: ${path}`)
  }
}
