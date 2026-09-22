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

import {
  fetcherFromEvent,
  textFetcherFromEvent,
} from '../c8y/client'
import type { C8yFetcher, C8yTextFetcher } from '../c8y/client'
import { recordWindow } from './tools/record-window'
import { queryLocal } from './tools/query-local'
import { getExpectations } from './tools/get-expectations'
import { clearSession } from './tools/clear-session'
import { checkDeviceTypes } from '../agent/check-device-types'
import type { Reasoner } from '../agent/loop'
import { getLogger } from '../log'

// Replaced at build time (see `replace` in nitro.config.ts); falls back to the
// literal when running under plain Node/vitest, where no build step applies.
declare const __SERVER_VERSION__: string | undefined
const SERVER_VERSION =
  typeof __SERVER_VERSION__ === 'string' ? __SERVER_VERSION__ : '0.0.0-dev'

const passSchema = z.union([z.literal(1), z.literal(2)])

/**
 * Build the diagnostic MCP server. In production the caller passes an H3Event and
 * the Cumulocity fetchers are derived from it; tests pass fetchers directly to
 * feed simulated telemetry. `text` is used to read knowledge files from the
 * Cumulocity file repository (Dateiablage).
 */
export function buildDiagnosticMcpServer(
  source:
    | H3Event
    | { fetch: C8yFetcher; text?: C8yTextFetcher; reasoner?: Reasoner },
): McpServer {
  const isInjected = 'fetch' in source
  const event: H3Event | undefined = isInjected ? undefined : source
  const fetch: C8yFetcher = isInjected ? source.fetch : fetcherFromEvent(source)
  const text: C8yTextFetcher | undefined = isInjected
    ? source.text
    : textFetcherFromEvent(source)
  const reasoner: Reasoner | undefined = isInjected ? source.reasoner : undefined
  const log = getLogger('mcp')
  const server = new McpServer({
    name: 'c8y-diagnostic-agent',
    // Build-time constant injected by nitro.config.ts from package.json, so the
    // MCP `initialize` handshake reports the deployed build — a hardcoded string
    // here makes it impossible to tell which version is actually running.
    version: SERVER_VERSION,
  })

  // ---- PRIMARY TOOL --------------------------------------------------------
  // The internal AI agent's entry point: "check these device types for
  // anomalies". Runs the whole pipeline (discover -> record -> check against the
  // knowledge file in the Cumulocity file repository -> two-pass validate) and
  // returns the defective device(s) of each type with the error.
  server.tool(
    'check_device_types',
    'Autonomously check one or more device TYPES for anomalies. Discovers all ' +
      'devices of each type, records their telemetry, applies the expected-behaviour ' +
      'checks from the knowledge Markdown in the Cumulocity file repository, ' +
      'validates over two sampling passes, and returns the defective device(s) with ' +
      'the fault domain and root cause.',
    {
      deviceTypes: z
        .array(z.string())
        .min(1)
        .describe("Cumulocity device types, e.g. ['sim_PowerNode']"),
      pass1WindowMinutes: z.number().optional(),
      pass2WaitSeconds: z.number().optional(),
      pass2WindowMinutes: z.number().optional(),
    },
    async (args) => {
      log.info('mcp call: check_device_types', {
        tool: 'check_device_types',
        deviceTypes: args.deviceTypes,
      })
      const result = await checkDeviceTypes(event, {
        deviceTypes: args.deviceTypes,
        pass1WindowMinutes: args.pass1WindowMinutes,
        pass2WaitSeconds: args.pass2WaitSeconds,
        pass2WindowMinutes: args.pass2WindowMinutes,
        // For injected (test) servers, forward the fetchers + reasoner so no
        // event / API key is needed.
        ...(isInjected ? { fetch, text, reasoner } : {}),
      })
      log.info('check_device_types result', {
        tool: 'check_device_types',
        defectCount: result.defects.length,
      })
      return { content: [{ type: 'text', text: JSON.stringify(result) }] }
    },
  )

  // ---- GRANULAR / INTERNAL TOOLS -------------------------------------------
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
      log.info('mcp call: record_window', {
        tool: 'record_window',
        deviceId: args.deviceId,
        pass: args.pass,
      })
      const summary = await recordWindow(fetch, args)
      // Log incoming telemetry counts (measurements / alarms / events).
      log.info('recorded telemetry window', {
        tool: 'record_window',
        deviceId: args.deviceId,
        pass: args.pass,
        measurements: summary.counts.measurements,
        alarms: summary.counts.alarms,
        events: summary.counts.events,
      })
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
      const result = await getExpectations(
        args,
        text ? { json: fetch, text } : undefined,
      )
      log.info('loaded knowledge base', {
        tool: 'get_expectations',
        deviceType: result.deviceType,
        source: result.source,
        sourceKind: result.sourceKind, // dateiablage | bundled | defaults
      })
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

/**
 * Names of the tools registered on the diagnostic MCP server, in registration
 * order. Built by instantiating the server with a no-op fetcher — registration
 * is pure wiring, nothing is called — so the list can be logged at startup
 * without a request context.
 */
export function registeredToolNames(): string[] {
  const noop: C8yFetcher = async () => {
    throw new Error('not called during tool enumeration')
  }
  const server = buildDiagnosticMcpServer({ fetch: noop })
  // The SDK keeps its tool registry in a private field; read it defensively so a
  // future SDK rename degrades to an empty list instead of crashing startup.
  const registry = (server as unknown as { _registeredTools?: Record<string, unknown> })
    ._registeredTools
  return registry ? Object.keys(registry) : []
}
