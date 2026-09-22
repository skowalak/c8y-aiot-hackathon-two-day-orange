// Live SSE sessions, keyed by session id.
//
// The SSE transport is stateful: `GET /sse` opens a stream and `POST /sse?
// sessionid=…` must be routed back to it. Nitro runs a single Node process per
// microservice instance, so a module-level map is the right scope — but it does
// mean SSE sessions are instance-local. That is fine at one replica (our
// deployment); with several, the platform would need sticky sessions.
//
// Streamable HTTP on /mcp stays stateless and is unaffected by any of this.

import type { WebStandardSSEServerTransport } from './sse-transport'

const sessions = new Map<string, WebStandardSSEServerTransport>()

/** Abandoned streams (client vanished without closing) are reaped after this. */
const SESSION_TTL_MS = 10 * 60 * 1000

const lastSeen = new Map<string, number>()

export function addSession(transport: WebStandardSSEServerTransport): void {
  sessions.set(transport.sessionId, transport)
  lastSeen.set(transport.sessionId, Date.now())
  reap()
}

export function getSession(
  sessionId: string,
): WebStandardSSEServerTransport | undefined {
  const found = sessions.get(sessionId)
  if (found) lastSeen.set(sessionId, Date.now())
  return found
}

export function removeSession(sessionId: string): void {
  sessions.delete(sessionId)
  lastSeen.delete(sessionId)
}

export function sessionCount(): number {
  return sessions.size
}

/**
 * Drop sessions idle past the TTL. Called on open rather than on a timer so the
 * process has no background work when nothing is connected.
 */
function reap(): void {
  const cutoff = Date.now() - SESSION_TTL_MS
  for (const [id, seen] of lastSeen) {
    if (seen < cutoff) {
      const transport = sessions.get(id)
      void transport?.close()
      sessions.delete(id)
      lastSeen.delete(id)
    }
  }
}
