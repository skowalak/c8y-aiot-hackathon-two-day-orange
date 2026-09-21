// Diagnostic MCP server wiring.
//
// Exposes the compact diagnostic tool surface from §4 of the functional
// description. `record_window` needs the authenticated Cumulocity client, which
// c8y-nitro derives from the H3 request event — so the server is constructed
// per request with the event captured in scope.
//
// NOTE: The `codemode` tool (sandboxed-JS execution over typed c8y namespaces,
// the mc8yp pattern) is intentionally left as a stub here. It requires the
// generated OpenAPI namespaces + a JS sandbox and is tracked as follow-up work;
// the four diagnostic tools below are sufficient to drive the two-pass loop.

import type { H3Event } from 'nitro/h3'
import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js'
import { z } from 'zod'

import { fetcherFromEvent } from '../c8y/client'
import type { C8yFetcher } from '../c8y/client'
import { recordWindow } from './tools/record-window'
import { queryLocal } from './tools/query-local'
import { getExpectations } from './tools/get-expectations'
import { clearSession } from './tools/clear-session'

const passSchema = z.union([z.literal(1), z.literal(2)])

/**
 * Build the diagnostic MCP server. In production the caller passes an H3Event and
 * the Cumulocity fetcher is derived from it; tests pass a fetcher directly to
 * feed simulated telemetry.
 */
export function buildDiagnosticMcpServer(
  source: H3Event | { fetch: C8yFetcher },
): McpServer {
  const fetch: C8yFetcher =
    'fetch' in source ? source.fetch : fetcherFromEvent(source)
  const server = new McpServer({
    name: 'c8y-diagnostic-agent',
    version: '0.1.0',
  })

  server.tool(
    'record_window',
    'Fetch alarms + measurements + events for a device/time window from ' +
      'Cumulocity and buffer them in the in-memory session store under the given pass.',
    {
      sessionId: z.string(),
      deviceId: z.string(),
      from: z.string().describe('ISO start of window'),
      to: z.string().describe('ISO end of window'),
      pass: passSchema.describe('1 = first sampling, 2 = second sampling'),
    },
    async (args) => {
      const summary = await recordWindow(fetch, args)
      return { content: [{ type: 'text', text: JSON.stringify(summary) }] }
    },
  )

  server.tool(
    'query_local',
    'Aggregate the in-memory session store (per-series min/max/avg, alarm/event ' +
      'tallies). Omit `pass` for a both-passes summary.',
    {
      sessionId: z.string(),
      pass: passSchema.optional(),
    },
    async (args) => {
      const result = queryLocal(args)
      return { content: [{ type: 'text', text: JSON.stringify(result) }] }
    },
  )

  server.tool(
    'get_expectations',
    "Load the Markdown expected-behaviour spec for the device's type " +
      '(falls back to defaults).',
    {
      sessionId: z.string().optional(),
      deviceType: z.string().optional(),
    },
    async (args) => {
      const result = await getExpectations(args)
      return { content: [{ type: 'text', text: JSON.stringify(result) }] }
    },
  )

  server.tool(
    'clear_session',
    'Drop all buffered telemetry for the session from memory. Mandatory final ' +
      'step once the diagnosis is complete.',
    {
      sessionId: z.string(),
    },
    async (args) => {
      const result = clearSession(args)
      return { content: [{ type: 'text', text: JSON.stringify(result) }] }
    },
  )

  return server
}
