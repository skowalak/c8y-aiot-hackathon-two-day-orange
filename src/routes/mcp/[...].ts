// MCP transport endpoint — exposed to the Cumulocity AI Agent Manager / MCP
// clients at /service/diagnostic-agent/mcp.
//
// Transport: the MCP spec deprecated the standalone HTTP+SSE transport in favour
// of "Streamable HTTP", which still uses SSE for server->client streaming. h3 v2
// is web-standard (srvx): the event carries a web `Request` (event.req) and the
// handler returns a web `Response`. We therefore use the SDK's
// WebStandardStreamableHTTPServerTransport, which maps Request -> Response
// directly — no Node req/res bridging required.
//
// Stateless mode (sessionIdGenerator: undefined): a fresh server+transport per
// request. Simplest correct option for a diagnostic tool server.
//
// The diagnostic tools need the authenticated Cumulocity client, which c8y-nitro
// derives from the H3 request event — so we build the server with the event in
// scope for every request (see buildDiagnosticMcpServer).

import { defineHandler } from 'nitro/h3'
import { WebStandardStreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/webStandardStreamableHttp.js'
import { buildDiagnosticMcpServer } from '../../mcp/server'

export default defineHandler(async (event) => {
  const server = buildDiagnosticMcpServer(event)
  const transport = new WebStandardStreamableHTTPServerTransport({
    sessionIdGenerator: undefined, // stateless
  })

  // The transport signals onclose once the response body has finished streaming
  // (or the client disconnected); tear the server down then so we don't cut off
  // an in-flight SSE stream.
  transport.onclose = () => {
    void server.close()
  }

  await server.connect(transport)

  // event.req is a web-standard Request; handleRequest returns a web Response
  // (JSON or an SSE ReadableStream) that h3 streams back to the client.
  return transport.handleRequest(event.req)
})
