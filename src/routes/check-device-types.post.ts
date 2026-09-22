// HTTP mirror of the primary MCP tool, for REST callers / the OpenAPI surface.
//
// POST /service/diagnostic-agent/check-device-types
//   { "deviceTypes": ["sim_PowerNode"], "pass2WaitSeconds"?: 0 }
//
// Discovers all devices of each type, records their telemetry over two sampling
// passes, and returns those summaries plus the expected-behaviour knowledge.
// Returns evidence, not a verdict — the calling agent's model does the reasoning
// (see src/agent/collect-device-types.ts for why).

import { defineHandler, readBody, createError } from 'nitro/h3'
import { useLogger } from 'c8y-nitro/utils'
import { collectDeviceTypes } from '../agent/collect-device-types'

interface Body {
  deviceTypes?: string[]
  pass1WindowMinutes?: number
  pass2WaitSeconds?: number
  pass2WindowMinutes?: number
}

export default defineHandler(async (event) => {
  const body = (await readBody<Body>(event)) ?? {}
  if (!body.deviceTypes?.length) {
    throw createError({
      statusCode: 400,
      statusMessage: 'deviceTypes (non-empty array) is required',
    })
  }

  const log = useLogger(event)
  log.info('check-device-types request received', {
    operation: 'check_device_types',
    deviceTypes: body.deviceTypes,
  })

  const result = await collectDeviceTypes(event, {
    deviceTypes: body.deviceTypes,
    pass1WindowMinutes: body.pass1WindowMinutes,
    pass2WaitSeconds: body.pass2WaitSeconds,
    pass2WindowMinutes: body.pass2WindowMinutes,
  })

  log.info('check-device-types complete', {
    operation: 'check_device_types',
    types: result.results.length,
    devices: result.results.reduce((n, r) => n + r.devices.length, 0),
  })
  return result
})
