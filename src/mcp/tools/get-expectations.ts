// get_expectations — load the Markdown expectation spec for a device type.
// Resolves the type from the session if not passed explicitly.

import { getExpectations as loadExpectations } from '../../knowledge'
import { getSession } from '../../store/session-store'

export interface GetExpectationsInput {
  sessionId?: string
  deviceType?: string
}

export async function getExpectations(input: GetExpectationsInput) {
  const deviceType =
    input.deviceType ??
    (input.sessionId ? getSession(input.sessionId)?.deviceType : undefined)
  return loadExpectations(deviceType)
}
