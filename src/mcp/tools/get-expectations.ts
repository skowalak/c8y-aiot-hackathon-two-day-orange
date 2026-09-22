// get_expectations — load the Markdown expectation spec for a device type.
// Resolves the type from the session if not passed explicitly. When c8y fetchers
// are provided, the knowledge file is read from the Cumulocity file repository
// (Dateiablage) first, falling back to the bundled files.

import {
  getExpectations as loadExpectations,
  type KnowledgeFetchers,
} from '../../knowledge'
import { getSession } from '../../store/session-store'

export interface GetExpectationsInput {
  sessionId?: string
  deviceType?: string
}

export async function getExpectations(
  input: GetExpectationsInput,
  fetchers?: KnowledgeFetchers,
) {
  const deviceType =
    input.deviceType ??
    (input.sessionId ? getSession(input.sessionId)?.deviceType : undefined)
  return loadExpectations(deviceType, fetchers)
}
