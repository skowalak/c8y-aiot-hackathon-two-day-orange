// Data collection for the calling AI agent — no LLM in this service.
//
// WHY: the previous pipeline reasoned in-process by calling Anthropic directly,
// which required an ANTHROPIC_API_KEY inside the microservice. Cumulocity injects
// no custom environment variables into a microservice container (only its own
// C8Y_* / APPLICATION_* set), so provisioning that key meant tenant options and
// runtime decryption — a lot of machinery for a key we do not actually need.
//
// The Agent Manager already has a model. So this service now does what only it
// can do — discover devices, record their telemetry over two passes, and load
// the expected-behaviour knowledge — and hands that back verbatim. The caller's
// LLM does the reasoning and writes the verdict.
//
//   1. DISCOVER — inventory query for all devices of each type.
//   2. RECORD   — buffer telemetry into the session store (two passes).
//   3. LOAD     — the expected-behaviour Markdown for the type (Dateiablage).
//   4. RETURN   — per-device pass summaries + the expectations, unjudged.

import type { H3Event } from 'nitro/h3'
import {
  fetcherFromEvent,
  listDeviceIdsByType,
  textFetcherFromEvent,
} from '../c8y/client'
import type { C8yFetcher, C8yTextFetcher } from '../c8y/client'
import { recordWindow } from '../mcp/tools/record-window'
import { queryLocal } from '../mcp/tools/query-local'
import { getExpectations } from '../mcp/tools/get-expectations'
import { clearSession } from '../mcp/tools/clear-session'
import { startSession, stopSession } from '../store/session-store'
import { getLogger } from '../log'

export interface CollectDeviceTypesOptions {
  deviceTypes: string[]
  pass1WindowMinutes?: number
  pass2WaitSeconds?: number
  pass2WindowMinutes?: number
  /** Injected deps (production derives them from the H3 event). */
  fetch?: C8yFetcher
  text?: C8yTextFetcher
}

export interface DeviceObservation {
  deviceId: string
  /** Aggregated telemetry for the first sampling window. */
  pass1?: unknown
  /** Aggregated telemetry for the second (confirmation) window. */
  pass2?: unknown
  /** Both passes combined — what the caller's model should reason over. */
  combined?: unknown
  /** Set when this device could not be sampled; the others still return data. */
  error?: string
}

export interface TypeObservation {
  deviceType: string
  discovered: number
  /** Expected-behaviour Markdown for this type (file repository, else bundled). */
  expectations: string
  /** Where the knowledge came from: dateiablage | bundled | defaults. */
  expectationsSource: string
  devices: DeviceObservation[]
}

export interface CollectDeviceTypesResult {
  results: TypeObservation[]
  /**
   * Instructions for the calling agent. The tool deliberately returns no verdict:
   * saying so explicitly stops a model from reading "no findings" as "all good".
   */
  instructions: string
  sampling: {
    pass1WindowMinutes: number
    pass2WaitSeconds: number
    pass2WindowMinutes: number
  }
}

const INSTRUCTIONS =
  'This payload is raw observational data, NOT a diagnosis. Compare each ' +
  "device's pass1/pass2 summaries against the `expectations` Markdown for its " +
  'type, then state which device (if any) is faulty, the fault domain and the ' +
  'root cause. Use both passes: a deviation present in pass1 but absent in ' +
  'pass2 is likely transient. If a device has an `error`, say it could not be ' +
  'assessed rather than treating it as healthy.'

function isoMinutesAgo(base: number, minutes: number): string {
  return new Date(base - minutes * 60_000).toISOString()
}

/**
 * Record both sampling windows for one device and return the aggregates.
 * Session buffers are always cleared, so telemetry never outlives the call.
 */
async function observeDevice(
  fetch: C8yFetcher,
  deviceId: string,
  deviceType: string,
  windows: { pass1WindowMinutes: number; pass2WaitSeconds: number; pass2WindowMinutes: number },
): Promise<DeviceObservation> {
  const sessionId = `collect-${deviceId}-${Date.now()}`
  startSession(sessionId, deviceId, deviceType)
  try {
    const now1 = Date.now()
    await recordWindow(fetch, {
      sessionId,
      deviceId,
      from: isoMinutesAgo(now1, windows.pass1WindowMinutes),
      to: new Date(now1).toISOString(),
      pass: 1,
    })
    const pass1 = queryLocal({ sessionId, pass: 1 })

    if (windows.pass2WaitSeconds > 0) {
      await new Promise((r) => setTimeout(r, windows.pass2WaitSeconds * 1000))
    }

    const now2 = Date.now()
    await recordWindow(fetch, {
      sessionId,
      deviceId,
      from: isoMinutesAgo(now2, windows.pass2WindowMinutes),
      to: new Date(now2).toISOString(),
      pass: 2,
    })
    const pass2 = queryLocal({ sessionId, pass: 2 })

    stopSession(sessionId)
    return { deviceId, pass1, pass2, combined: queryLocal({ sessionId }) }
  } catch (error) {
    // One unreachable device must not sink the whole fleet check.
    return {
      deviceId,
      error: error instanceof Error ? error.message : String(error),
    }
  } finally {
    clearSession({ sessionId })
  }
}

/**
 * Discover, sample and return telemetry for every device of the given types,
 * together with the expected-behaviour knowledge. Performs no judgement.
 */
export async function collectDeviceTypes(
  event: H3Event | undefined,
  opts: CollectDeviceTypesOptions,
): Promise<CollectDeviceTypesResult> {
  const log = getLogger('collect-device-types')
  const windows = {
    pass1WindowMinutes: opts.pass1WindowMinutes ?? 30,
    pass2WaitSeconds: opts.pass2WaitSeconds ?? 120,
    pass2WindowMinutes: opts.pass2WindowMinutes ?? 2,
  }

  log.info('check_device_types invoked', {
    operation: 'check_device_types',
    deviceTypes: opts.deviceTypes,
  })

  let fetch = opts.fetch
  let text = opts.text
  if (!fetch) {
    if (!event) {
      throw new Error('collectDeviceTypes requires an H3 event or an injected fetch')
    }
    fetch = fetcherFromEvent(event)
    text = text ?? textFetcherFromEvent(event)
  }

  const results: TypeObservation[] = []

  for (const deviceType of opts.deviceTypes) {
    const deviceIds = await listDeviceIdsByType(fetch, deviceType)
    log.info('discovered devices of type', {
      operation: 'check_device_types',
      deviceType,
      discovered: deviceIds.length,
    })

    const knowledge = await getExpectations(
      { deviceType },
      text ? { json: fetch, text } : undefined,
    )

    // Sample the fleet concurrently: the pass-2 wait dominates the runtime, so
    // serial sampling would multiply it by the number of devices.
    const devices = await Promise.all(
      deviceIds.map((deviceId) => observeDevice(fetch!, deviceId, deviceType, windows)),
    )

    results.push({
      deviceType,
      discovered: deviceIds.length,
      expectations: knowledge.markdown,
      expectationsSource: knowledge.sourceKind ?? 'unknown',
      devices,
    })
  }

  log.info('check_device_types complete', {
    operation: 'check_device_types',
    types: results.length,
    devices: results.reduce((n, r) => n + r.devices.length, 0),
  })

  return { results, instructions: INSTRUCTIONS, sampling: windows }
}
