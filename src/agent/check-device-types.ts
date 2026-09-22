// High-level autonomous entry point for the internal AI agent.
//
// The agent calls this with just a list of device TYPES ("check these device
// types for anomalies"). The microservice then does everything itself:
//
//   1. DISCOVER  — query the inventory for all devices of each type.
//   2. REGISTER  — take them under observation (read-only: we read their
//                  alarms/measurements/events; no writes to the device).
//   3. RECORD    — buffer telemetry into the in-memory session store (two passes).
//   4. CHECK     — apply the expectations from the knowledge Markdown that lives
//                  in the Cumulocity file repository (Dateiablage) for that type.
//   5. VALIDATE  — the second sampling pass confirms/rejects each hypothesis.
//   6. RETURN    — the defective device(s) of each type with the error.
//
// Reasoning is in-process (Claude) unless a reasoner is injected (tests).

import type { H3Event } from 'nitro/h3'
import {
  fetcherFromEvent,
  listDeviceIdsByType,
  textFetcherFromEvent,
} from '../c8y/client'
import type { C8yFetcher, C8yTextFetcher } from '../c8y/client'
import { runFleetCheck } from './fleet-check'
import type { DeviceResult } from './fleet-check'
import type { Reasoner } from './loop'
import { getLogger } from '../log'

export interface CheckDeviceTypesOptions {
  deviceTypes: string[]
  pass1WindowMinutes?: number
  pass2WaitSeconds?: number
  pass2WindowMinutes?: number
  // Injected deps (production builds these from the event + config).
  fetch?: C8yFetcher
  text?: C8yTextFetcher
  reasoner?: Reasoner
}

export interface TypeCheckResult {
  deviceType: string
  discovered: number
  ranking: DeviceResult[]
  defectiveDevice?: DeviceResult // the faulty device of this type, if any
  summary: string
}

export interface CheckDeviceTypesResult {
  results: TypeCheckResult[]
  // Flat list of every defective device found across all requested types.
  defects: Array<{
    deviceType: string
    deviceId: string
    faultDomain: string
    rootCause: string
    confidence: number
  }>
  summary: string
}

export async function checkDeviceTypes(
  event: H3Event | undefined,
  opts: CheckDeviceTypesOptions,
): Promise<CheckDeviceTypesResult> {
  const log = getLogger('check-device-types')
  log.info('check_device_types invoked', {
    operation: 'check_device_types',
    deviceTypes: opts.deviceTypes,
  })

  // Resolve the Cumulocity fetchers once (needed for discovery + knowledge).
  let fetch = opts.fetch
  let text = opts.text
  if (!fetch) {
    if (!event) {
      throw new Error(
        'checkDeviceTypes requires an H3 event or an injected fetch',
      )
    }
    fetch = fetcherFromEvent(event)
    text = text ?? textFetcherFromEvent(event)
  }

  const results: TypeCheckResult[] = []

  for (const deviceType of opts.deviceTypes) {
    // 1) DISCOVER + 2) REGISTER (read-observe): find all devices of this type.
    const deviceIds = await listDeviceIdsByType(fetch, deviceType)
    log.info('discovered devices of type', {
      operation: 'check_device_types',
      deviceType,
      discovered: deviceIds.length,
    })

    if (deviceIds.length === 0) {
      results.push({
        deviceType,
        discovered: 0,
        ranking: [],
        summary: `No devices of type ${deviceType} found.`,
      })
      continue
    }

    // 3) RECORD + 4) CHECK + 5) VALIDATE: two-pass diagnosis per device against
    // the type's knowledge base (loaded from the Dateiablage inside runDiagnosis).
    const fleet = await runFleetCheck(event, {
      deviceIds,
      pass1WindowMinutes: opts.pass1WindowMinutes,
      pass2WaitSeconds: opts.pass2WaitSeconds,
      pass2WindowMinutes: opts.pass2WindowMinutes,
      fetch,
      text,
      reasoner: opts.reasoner,
    })

    results.push({
      deviceType,
      discovered: deviceIds.length,
      ranking: fleet.ranking,
      defectiveDevice: fleet.culprit,
      summary: fleet.summary,
    })
  }

  // 6) RETURN: flatten defects across all types.
  const defects = results
    .filter((r) => r.defectiveDevice?.verdict?.faulty)
    .map((r) => {
      const v = r.defectiveDevice!.verdict!
      return {
        deviceType: r.deviceType,
        deviceId: r.defectiveDevice!.deviceId,
        faultDomain: v.faultDomain,
        rootCause: v.rootCause,
        confidence: v.confidence,
      }
    })

  const summary = defects.length
    ? `Found ${defects.length} defective device(s): ` +
      defects
        .map((d) => `${d.deviceId} (${d.deviceType}) — ${d.faultDomain}: ${d.rootCause}`)
        .join('; ')
    : `No anomalies found across ${opts.deviceTypes.join(', ')}.`

  log.info('check_device_types complete', {
    operation: 'check_device_types',
    defectCount: defects.length,
  })

  return { results, defects, summary }
}
