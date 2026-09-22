// The primary autonomous flow: the AI agent asks "check these device types for
// anomalies" and the microservice discovers, records, checks (against the
// knowledge Markdown), validates, and returns the defective device.
//
// Exercised two ways: (1) the orchestrator directly, (2) end-to-end over a real
// in-memory MCP transport calling the check_device_types tool. Domain knowledge
// stays in sim_PowerNode.md; the generic reasoner drives the verdict.

import { describe, it, expect } from 'vitest'
import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js'

import { checkDeviceTypes } from '../src/agent/check-device-types'
import { buildDiagnosticMcpServer } from '../src/mcp/server'
import { POWER_NODE_TYPE, makeFleetFetcher } from './fixtures/power-node'
import { genericReasoner } from './fixtures/generic-reasoner'

describe('check_device_types — autonomous anomaly check by device type', () => {
  it('discovers all devices of the type and returns the defective one', async () => {
    const result = await checkDeviceTypes(undefined, {
      deviceTypes: [POWER_NODE_TYPE],
      pass2WaitSeconds: 0,
      fetch: makeFleetFetcher(),
      reasoner: genericReasoner,
    })

    const typeResult = result.results.find((r) => r.deviceType === POWER_NODE_TYPE)
    expect(typeResult?.discovered).toBe(6)

    // Exactly one defect, correctly attributed.
    expect(result.defects).toHaveLength(1)
    expect(result.defects[0]).toMatchObject({
      deviceType: POWER_NODE_TYPE,
      deviceId: 'dev-sim-node-01',
      faultDomain: 'event',
    })
    expect(result.summary).toContain('dev-sim-node-01')
  })

  it('works end-to-end over the MCP transport', async () => {
    const server = buildDiagnosticMcpServer({
      fetch: makeFleetFetcher(),
      reasoner: genericReasoner,
    })
    const [ct, st] = InMemoryTransport.createLinkedPair()
    const client = new Client({ name: 'test', version: '0.0.0' })
    await Promise.all([server.connect(st), client.connect(ct)])

    // check_device_types is advertised as a tool.
    const { tools } = await client.listTools()
    expect(tools.map((t) => t.name)).toContain('check_device_types')

    const res = await client.callTool({
      name: 'check_device_types',
      arguments: { deviceTypes: [POWER_NODE_TYPE], pass2WaitSeconds: 0 },
    })
    const payload = JSON.parse(
      (res as any).content.find((c: any) => c.type === 'text').text,
    )
    expect(payload.defects).toHaveLength(1)
    expect(payload.defects[0].deviceId).toBe('dev-sim-node-01')

    await client.close()
  })
})
