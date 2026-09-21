// HTTP entry point: start a diagnostic session for a device and return the verdict.
//
// POST /service/diagnostic-agent/diagnose
//   { "deviceId": "12345", "pass1WindowMinutes"?: 30, "pass2WaitSeconds"?: 120,
//     "pass2WindowMinutes"?: 2 }
//
// The whole two-pass loop runs in-process; buffered telemetry is wiped before
// the response returns (see runDiagnosis' finally block).

import { defineHandler, readBody, createError } from 'nitro/h3'
import { runDiagnosis } from '../agent/loop'

let counter = 0
function newSessionId(deviceId: string): string {
  // Unique enough for concurrent diagnoses within one process.
  counter += 1
  return `diag-${deviceId}-${Date.now()}-${counter}`
}

export default defineHandler(async (event) => {
  const body = await readBody<{
    deviceId?: string
    pass1WindowMinutes?: number
    pass2WaitSeconds?: number
    pass2WindowMinutes?: number
  }>(event)

  if (!body?.deviceId) {
    throw createError({ statusCode: 400, statusMessage: 'deviceId is required' })
  }

  const verdict = await runDiagnosis(event, {
    sessionId: newSessionId(body.deviceId),
    deviceId: body.deviceId,
    pass1WindowMinutes: body.pass1WindowMinutes,
    pass2WaitSeconds: body.pass2WaitSeconds,
    pass2WindowMinutes: body.pass2WindowMinutes,
  })

  return verdict
})
