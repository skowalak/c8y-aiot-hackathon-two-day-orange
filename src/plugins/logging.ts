// Microservice logging plugin.
//
// Logs the service lifecycle (startup / ready-and-waiting / shutdown) and every
// incoming HTTP call. Domain-specific ingestion logging (events / alarms /
// measurements / MCP messages) is emitted closer to the data — see
// src/mcp/tools/record-window.ts and src/mcp/server.ts.
//
// Auto-registered because it lives in <serverDir>/plugins (serverDir = src).
//
// Nitro v3 renamed defineNitroPlugin -> definePlugin, imported from 'nitro'.

import { definePlugin } from 'nitro'
import { getLogger } from '../log'
import { registeredToolNames } from '../mcp/server'

const SERVICE = 'c8y-diagnostic-agent'

export default definePlugin((nitroApp) => {
  const log = getLogger('lifecycle')

  // --- STARTUP ---
  log.info('diagnostic-agent microservice starting up', {
    service: SERVICE,
    phase: 'startup',
    node: process.version,
    pid: process.pid,
  })
  // --- MCP TOOL REGISTRATION ---
  // The MCP server is built per request (it needs the authenticated Cumulocity
  // client from the H3 event), so nothing would otherwise confirm at boot that
  // the tools are wired. Enumerate them once here.
  //
  // This one line goes to stdout directly rather than through getLogger: the
  // platform logger is resolved lazily and emits asynchronously, and during
  // plugin init (before any request/tenant context exists) its output does not
  // reach the container log — which is why the lifecycle lines below are
  // invisible in `docker logs`. process.stdout is the same stream that carries
  // Nitro's own "Listening on:" banner, so this always shows up.
  const mcpLog = getLogger('mcp')
  try {
    const tools = registeredToolNames()
    if (tools.length > 0) {
      process.stdout.write(
        `\u2713 MCP tools registered (${tools.length}) at /mcp: ${tools.join(', ')}\n`,
      )
      mcpLog.info(`MCP tools registered: ${tools.join(', ')}`, {
        service: SERVICE,
        phase: 'startup',
        endpoint: '/mcp',
        toolCount: tools.length,
        tools,
      })
    } else {
      process.stdout.write(
        '\u2717 MCP server exposes NO tools \u2014 registration failed\n',
      )
      mcpLog.warn('MCP server exposes NO tools — registration failed', {
        service: SERVICE,
        phase: 'startup',
        endpoint: '/mcp',
      })
    }
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    process.stdout.write(`\u2717 MCP tool registration check failed: ${message}\n`)
    mcpLog.error('MCP tool registration check failed', {
      service: SERVICE,
      phase: 'startup',
      endpoint: '/mcp',
      error: message,
    })
  }

  // Emitted once the app is wired; the HTTP server is now accepting connections
  // and the agent is idle, waiting for diagnostic requests / MCP calls.
  log.info('diagnostic-agent ready — waiting for incoming calls', {
    service: SERVICE,
    phase: 'waiting',
    endpoints: ['/diagnose', '/fleet-check', '/mcp', '/health'],
  })

  // --- INCOMING CALLS ---
  nitroApp.hooks.hook('request', (event) => {
    const url = event.req?.url ?? ''
    // Skip the platform probes so they don't drown the log.
    if (url.includes('/_c8y_nitro/') || url.endsWith('/health')) return
    log.info('incoming call', {
      service: SERVICE,
      phase: 'request',
      method: event.req?.method,
      path: url,
    })
  })

  // --- SHUTDOWN ---
  nitroApp.hooks.hook('close', () => {
    log.info('diagnostic-agent shutting down', {
      service: SERVICE,
      phase: 'shutdown',
    })
  })
})
