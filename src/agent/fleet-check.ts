// Fleet check: diagnose several devices, rank them, and name the single device
// that is not working properly.
//
// Reuses the per-device two-pass loop (runDiagnosis). Each device is diagnosed
// independently; results are ranked so the worst-offending faulty device surfaces
// as the "culprit".

import type { H3Event } from 'nitro/h3'
import { runDiagnosis } from './loop'
import type { DiagnoseOptions } from './loop'
import type { DiagnosticVerdict } from '../store/types'

export interface FleetCheckOptions {
  deviceIds: string[]
  // Same sampling knobs as a single diagnosis; applied to every device.
  pass1WindowMinutes?: number
  pass2WaitSeconds?: number
  pass2WindowMinutes?: number
  // Injected dependencies (production builds these from the event + config).
  fetch?: DiagnoseOptions['fetch']
  reasoner?: DiagnoseOptions['reasoner']
}

export interface DeviceResult {
  deviceId: string
  verdict?: DiagnosticVerdict
  error?: string
}

export interface FleetCheckResult {
  checked: number
  ranking: DeviceResult[] // worst first
  culprit?: DeviceResult // the single device not working properly (if any)
  summary: string
}

// Higher = worse. Faulty devices sort above healthy ones; within faulty, higher
// confidence and a non-"none" domain rank first.
function severityScore(v?: DiagnosticVerdict): number {
  if (!v || !v.faulty) return 0
  const domainWeight = v.faultDomain === 'none' ? 0 : 1
  return 1_000 + domainWeight * 100 + Math.round(v.confidence * 100)
}

/**
 * Diagnose every device in `deviceIds`, sequentially (the in-memory session
 * store is per-session but device diagnoses reuse it one at a time), and return
 * a ranking with the culprit identified.
 */
export async function runFleetCheck(
  event: H3Event | undefined,
  opts: FleetCheckOptions,
): Promise<FleetCheckResult> {
  const results: DeviceResult[] = []

  for (const deviceId of opts.deviceIds) {
    try {
      const verdict = await runDiagnosis(event, {
        sessionId: `fleet-${deviceId}-${results.length}`,
        deviceId,
        pass1WindowMinutes: opts.pass1WindowMinutes,
        pass2WaitSeconds: opts.pass2WaitSeconds,
        pass2WindowMinutes: opts.pass2WindowMinutes,
        fetch: opts.fetch,
        reasoner: opts.reasoner,
      })
      results.push({ deviceId, verdict })
    } catch (err) {
      results.push({ deviceId, error: (err as Error).message })
    }
  }

  const ranking = [...results].sort(
    (a, b) => severityScore(b.verdict) - severityScore(a.verdict),
  )

  const top = ranking[0]
  const culprit = top && top.verdict?.faulty ? top : undefined

  const summary = culprit
    ? `Device ${culprit.deviceId} is not working properly: ` +
      `${culprit.verdict!.faultDomain}-domain fault — ${culprit.verdict!.rootCause} ` +
      `(confidence ${culprit.verdict!.confidence}).`
    : 'No faulty device found across the fleet.'

  return { checked: results.length, ranking, culprit, summary }
}
