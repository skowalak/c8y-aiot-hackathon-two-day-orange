// HTTP mirror of the primary MCP tool, for REST callers / the OpenAPI surface.
//
// POST /service/diagnostic-agent/check-device-types
//   { "deviceTypes": ["sim_PowerNode"], "pass2WaitSeconds"?: 0 }
//
// Autonomously discovers all devices of each type, records + checks them against
// the knowledge Markdown in the Cumulocity file repository, validates over two
// passes, and returns the defective device(s) with the error.

import { defineHandler, readBody, createError } from 'nitro/h3'
import { useLogger } from 'c8y-nitro/utils'
import { checkDeviceTypes } from '../agent/check-device-types'

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

  const result = await checkDeviceTypes(event, {
    deviceTypes: body.deviceTypes,
    pass1WindowMinutes: body.pass1WindowMinutes,
    pass2WaitSeconds: body.pass2WaitSeconds,
    pass2WindowMinutes: body.pass2WindowMinutes,
  })

  log.info('check-device-types complete', {
    operation: 'check_device_types',
    defectCount: result.defects.length,
  })
  return result
})
