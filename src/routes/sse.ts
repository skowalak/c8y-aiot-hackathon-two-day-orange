// Legacy MCP "HTTP+SSE" transport endpoint, served at /service/diagnostic-agent/sse.
//
// WHY: the Cumulocity AI Agent Manager connects over this older transport rather
// than Streamable HTTP, so a service exposing only /mcp is invisible to it — its
// tool list comes back empty. This route makes our tools discoverable there.
// /mcp (Streamable HTTP, stateless) remains the preferred endpoint and is
// unchanged; both are served side by side.
//
// Two methods on one path (hence a catch-all filename rather than .get/.post):
//   GET  -> open the event stream; first frame carries the POST-back URL
//   POST -> deliver a JSON-RPC message into the session named by ?sessionid=

import { defineHandler, getQuery, readBody } from 'nitro/h3'
import { buildDiagnosticMcpServer } from '../mcp/server'
import { WebStandardSSEServerTransport } from '../mcp/sse-transport'
import { addSession, getSession, removeSession } from '../mcp/sse-sessions'
import { getLogger } from '../log'

// Path the client is told to POST to. Cumulocity exposes the microservice under
// /service/<contextPath>, and the Agent Manager resolves this against the URL it
// connected to, so the absolute service path is what must be advertised.
const POST_ENDPOINT = '/service/diagnostic-agent/sse'

export default defineHandler(async (event) => {
  const log = getLogger('mcp-sse')
  const method = event.req.method

  // ---- POST: client -> server message ---------------------------------------
  if (method === 'POST') {
    const sessionId = String(getQuery(event).sessionid ?? '')
    if (!sessionId) {
      event.res.status = 400
      return 'sessionid must be provided'
    }

    const transport = getSession(sessionId)
    if (!transport) {
      // Stream already gone (instance restarted, or TTL reaped it).
      event.res.status = 404
      return 'session not found'
    }

    const body = await readBody(event)
    const error = await transport.handlePostMessage(body)
    if (error) {
      event.res.status = 400
      return error
    }

    // The JSON-RPC reply goes out over the event stream, not this response.
    event.res.status = 202
    return 'accepted'
  }

  // ---- GET: open the event stream -------------------------------------------
  const sessionId = crypto.randomUUID().replace(/-/g, '')
  const transport = new WebStandardSSEServerTransport(POST_ENDPOINT, sessionId)
  // Register before connect(): start() emits the endpoint frame, after which the
  // client may POST immediately.
  addSession(transport)

  const server = buildDiagnosticMcpServer(event)
  transport.onclose = () => {
    removeSession(sessionId)
    void server.close()
  }
  transport.onerror = (error) => {
    log.warn('sse transport error', { sessionId, error: error.message })
  }

  await server.connect(transport)
  log.info('sse session opened', { sessionId, endpoint: '/sse' })

  event.res.headers.set('Content-Type', 'text/event-stream')
  event.res.headers.set('Cache-Control', 'no-cache, no-transform')
  event.res.headers.set('Connection', 'keep-alive')
  // Disable proxy buffering so frames reach the client as they are written.
  event.res.headers.set('X-Accel-Buffering', 'no')

  return transport.stream
})
