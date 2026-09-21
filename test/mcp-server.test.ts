// Tests accessing the diagnostic MCP server over a real in-memory MCP transport
// (the SDK's InMemoryTransport linked pair), and recording data through the
// record_window / query_local tools with simulated failing water-pump telemetry.

import { describe, it, expect, beforeEach } from 'vitest'
import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js'

import { buildDiagnosticMcpServer } from '../src/mcp/server'
import {
  clearSession,
  startSession,
} from '../src/store/session-store'
import {
  FAULTY_PUMP_DEVICE_ID,
  makePumpFetcher,
} from './fixtures/water-pump'

const SESSION = 'test-mcp-session'

/** Connect an MCP Client to the diagnostic server over a linked in-memory pair. */
async function connectClient(faulty = true): Promise<Client> {
  const server = buildDiagnosticMcpServer({ fetch: makePumpFetcher({ faulty }) })
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair()
  const client = new Client({ name: 'test-client', version: '0.0.0' })
  await Promise.all([
    server.connect(serverTransport),
    client.connect(clientTransport),
  ])
  return client
}

/** Pull the JSON payload out of an MCP tool result's text content. */
function toolJson(result: any): any {
  const text = result.content.find((c: any) => c.type === 'text')?.text
  return JSON.parse(text)
}

describe('diagnostic MCP server', () => {
  beforeEach(() => {
    clearSession(SESSION)
  })

  it('exposes the diagnostic tool surface', async () => {
    const client = await connectClient()
    const { tools } = await client.listTools()
    const names = tools.map((t) => t.name).sort()
    expect(names).toEqual(
      ['clear_session', 'get_expectations', 'query_local', 'record_window'].sort(),
    )
    await client.close()
  })

  it('records measurements/alarms/events for a device window (pass 1)', async () => {
    startSession(SESSION, FAULTY_PUMP_DEVICE_ID, 'c8y_Water_Pump')
    const client = await connectClient(true)

    const result = await client.callTool({
      name: 'record_window',
      arguments: {
        sessionId: SESSION,
        deviceId: FAULTY_PUMP_DEVICE_ID,
        from: '2026-09-21T09:00:00.000Z',
        to: '2026-09-21T09:06:00.000Z',
        pass: 1,
      },
    })

    const summary = toolJson(result)
    expect(summary.pass).toBe(1)
    // 6 samples x 2 series (pressure + flow) = 12 measurement records.
    expect(summary.counts.measurements).toBe(12)
    expect(summary.counts.alarms).toBe(1)
    expect(summary.counts.events).toBe(1)
    expect(summary.seriesSeen.sort()).toEqual([
      'c8y_FlowRate.F',
      'c8y_Pressure.P',
    ])

    await client.close()
  })

  it('query_local aggregates the recorded window (flow stuck at 0)', async () => {
    startSession(SESSION, FAULTY_PUMP_DEVICE_ID, 'c8y_Water_Pump')
    const client = await connectClient(true)

    await client.callTool({
      name: 'record_window',
      arguments: {
        sessionId: SESSION,
        deviceId: FAULTY_PUMP_DEVICE_ID,
        from: '2026-09-21T09:00:00.000Z',
        to: '2026-09-21T09:06:00.000Z',
        pass: 1,
      },
    })

    const stats = toolJson(
      await client.callTool({
        name: 'query_local',
        arguments: { sessionId: SESSION, pass: 1 },
      }),
    )

    const flow = stats.measurementStats.find(
      (s: any) => s.series === 'c8y_FlowRate.F',
    )
    const pressure = stats.measurementStats.find(
      (s: any) => s.series === 'c8y_Pressure.P',
    )
    expect(flow.min).toBe(0)
    expect(flow.max).toBe(0) // flow flat-lined at zero — the fault signal
    expect(pressure.min).toBeGreaterThanOrEqual(2.0)
    expect(pressure.max).toBeLessThanOrEqual(6.0) // pressure healthy

    await client.close()
  })

  it('get_expectations returns the water-pump knowledge base', async () => {
    const client = await connectClient()
    const expectations = toolJson(
      await client.callTool({
        name: 'get_expectations',
        arguments: { deviceType: 'c8y_Water_Pump' },
      }),
    )
    expect(expectations.source).toBe('c8y_Water_Pump.md')
    expect(expectations.markdown).toContain('c8y_FlowRate')
    await client.close()
  })

  it('clear_session wipes buffered telemetry from memory', async () => {
    startSession(SESSION, FAULTY_PUMP_DEVICE_ID, 'c8y_Water_Pump')
    const client = await connectClient(true)

    await client.callTool({
      name: 'record_window',
      arguments: {
        sessionId: SESSION,
        deviceId: FAULTY_PUMP_DEVICE_ID,
        from: '2026-09-21T09:00:00.000Z',
        to: '2026-09-21T09:06:00.000Z',
        pass: 1,
      },
    })

    const cleared = toolJson(
      await client.callTool({
        name: 'clear_session',
        arguments: { sessionId: SESSION },
      }),
    )
    expect(cleared.cleared).toBe(true)

    // A follow-up query must now fail: the session no longer exists.
    const after = await client.callTool({
      name: 'query_local',
      arguments: { sessionId: SESSION },
    })
    expect(after.isError).toBe(true)

    await client.close()
  })
})
