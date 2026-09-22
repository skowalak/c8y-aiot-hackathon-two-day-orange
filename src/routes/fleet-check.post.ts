// HTTP entry point: check a fleet of devices and name the one not working properly.
//
// POST /service/diagnostic-agent/fleet-check
//   {
//     "deviceIds"?: ["1234", ...],           // explicit managed-object ids, OR
//     "serials"?:   ["sim-node-01", ...],    // resolved via external id, OR
//     "discoverType"?: "sim_PowerNode",      // discover all devices of a type
//     "pass1WindowMinutes"?: 60,
//     "pass2WaitSeconds"?: 0,
//     "pass2WindowMinutes"?: 5
//   }
//
// Runs the two-pass diagnosis on each device against its knowledge base, ranks
// the results, and reports the culprit. Uses live Cumulocity data via the
// authenticated request client.

import { defineHandler, readBody, createError } from 'nitro/h3'
import { useLogger } from 'c8y-nitro/utils'
import {
  fetcherFromEvent,
  listDeviceIdsByType,
  resolveDeviceIdByExternalId,
} from '../c8y/client'
import { runFleetCheck } from '../agent/fleet-check'

interface Body {
  deviceIds?: string[]
  serials?: string[]
  discoverType?: string
  pass1WindowMinutes?: number
  pass2WaitSeconds?: number
  pass2WindowMinutes?: number
}

export default defineHandler(async (event) => {
  const body = (await readBody<Body>(event)) ?? {}
  const fetch = fetcherFromEvent(event)
  const log = useLogger(event)
  log.info('fleet-check request received', {
    operation: 'fleet-check',
    selector: body.deviceIds
      ? 'deviceIds'
      : body.serials
        ? 'serials'
        : body.discoverType
          ? 'discoverType'
          : 'none',
  })

  // Resolve the device list from whichever selector was provided.
  let deviceIds: string[] = []
  if (body.deviceIds?.length) {
    deviceIds = body.deviceIds
  } else if (body.serials?.length) {
    const resolved = await Promise.all(
      body.serials.map((s) => resolveDeviceIdByExternalId(fetch, s)),
    )
    deviceIds = resolved.filter((id): id is string => !!id)
    if (deviceIds.length !== body.serials.length) {
      throw createError({
        statusCode: 404,
        statusMessage: 'One or more serials could not be resolved to a device id',
      })
    }
  } else if (body.discoverType) {
    deviceIds = await listDeviceIdsByType(fetch, body.discoverType)
  } else {
    throw createError({
      statusCode: 400,
      statusMessage: 'Provide deviceIds, serials, or discoverType',
    })
  }

  if (deviceIds.length === 0) {
    throw createError({ statusCode: 404, statusMessage: 'No devices to check' })
  }

  log.info('checking fleet', {
    operation: 'fleet-check',
    deviceCount: deviceIds.length,
  })
  const result = await runFleetCheck(event, {
    deviceIds,
    pass1WindowMinutes: body.pass1WindowMinutes,
    pass2WaitSeconds: body.pass2WaitSeconds,
    pass2WindowMinutes: body.pass2WindowMinutes,
  })
  log.info('fleet-check complete', {
    operation: 'fleet-check',
    checked: result.checked,
    culprit: result.culprit?.deviceId ?? null,
  })
  return result
})
