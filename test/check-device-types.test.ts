// The primary flow: the AI agent asks "check these device types for anomalies"
// and the microservice discovers every device of the type, records two sampling
// passes, and returns that evidence together with the expected-behaviour
// knowledge. It deliberately returns NO verdict — the calling agent's model
// reasons over the payload (see src/agent/collect-device-types.ts).
//
// Exercised two ways: (1) the collector directly, (2) end-to-end over a real
// in-memory MCP transport calling the check_device_types tool.

import { describe, it, expect } from 'vitest'
import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js'

import { collectDeviceTypes } from '../src/agent/collect-device-types'
import { buildDiagnosticMcpServer } from '../src/mcp/server'
import { POWER_NODE_TYPE, makeFleetFetcher } from './fixtures/power-node'

describe('check_device_types — evidence collection by device type', () => {
  it('discovers every device of the type and returns per-device samples', async () => {
    const result = await collectDeviceTypes(undefined, {
      deviceTypes: [POWER_NODE_TYPE],
      pass2WaitSeconds: 0,
      fetch: makeFleetFetcher(),
    })

    const typeResult = result.results.find((r) => r.deviceType === POWER_NODE_TYPE)
    expect(typeResult?.discovered).toBe(6)
    expect(typeResult?.devices).toHaveLength(6)

    // Every device carries both sampling passes and no error.
    for (const device of typeResult!.devices) {
      expect(device.error).toBeUndefined()
      expect(device.pass1).toBeDefined()
      expect(device.pass2).toBeDefined()
      expect(device.combined).toBeDefined()
    }

    // The knowledge base travels with the data so the caller can judge it.
    expect(typeResult?.expectations).toBeTruthy()
    expect(typeResult?.expectationsSource).toBeTruthy()
  })

  it('states that the payload is evidence rather than a verdict', async () => {
    const result = await collectDeviceTypes(undefined, {
      deviceTypes: [POWER_NODE_TYPE],
      pass2WaitSeconds: 0,
      fetch: makeFleetFetcher(),
    })

    // Guards the failure mode that caused "No anomalies found" to read as a
    // clean bill of health when the pipeline had in fact not run.
    expect(result.instructions).toMatch(/NOT a diagnosis/i)
    expect(result.instructions).toMatch(/could not be assessed/i)
    expect(result).not.toHaveProperty('defects')
    expect(result.sampling.pass1WindowMinutes).toBeGreaterThan(0)
  })

  it('reports an unreachable device without failing the whole fleet', async () => {
    const base = makeFleetFetcher()
    const fetch: typeof base = async (path, init) => {
      if (path.includes('dev-sim-node-03')) throw new Error('device unreachable')
      return base(path, init)
    }

    const result = await collectDeviceTypes(undefined, {
      deviceTypes: [POWER_NODE_TYPE],
      pass2WaitSeconds: 0,
      fetch,
    })

    const devices = result.results[0].devices
    const failed = devices.filter((d) => d.error)
    expect(failed).toHaveLength(1)
    expect(failed[0].deviceId).toBe('dev-sim-node-03')
    // The remaining five still produced usable evidence.
    expect(devices.filter((d) => !d.error)).toHaveLength(5)
  })

  it('needs no Anthropic key — the caller supplies the reasoning', async () => {
    const saved = process.env.ANTHROPIC_API_KEY
    delete process.env.ANTHROPIC_API_KEY
    try {
      const result = await collectDeviceTypes(undefined, {
        deviceTypes: [POWER_NODE_TYPE],
        pass2WaitSeconds: 0,
        fetch: makeFleetFetcher(),
      })
      expect(result.results[0].devices).toHaveLength(6)
    } finally {
      if (saved !== undefined) process.env.ANTHROPIC_API_KEY = saved
    }
  })

  it('works end-to-end over the MCP transport', async () => {
    const server = buildDiagnosticMcpServer({ fetch: makeFleetFetcher() })
    const [ct, st] = InMemoryTransport.createLinkedPair()
    const client = new Client({ name: 'test', version: '0.0.0' })
    await Promise.all([server.connect(st), client.connect(ct)])

    const { tools } = await client.listTools()
    expect(tools.map((t) => t.name)).toContain('check_device_types')

    const res = await client.callTool({
      name: 'check_device_types',
      arguments: { deviceTypes: [POWER_NODE_TYPE], pass2WaitSeconds: 0 },
    })
    const payload = JSON.parse(
      (res as any).content.find((c: any) => c.type === 'text').text,
    )
    expect(payload.results[0].discovered).toBe(6)
    expect(payload.results[0].devices).toHaveLength(6)
    expect(payload.instructions).toMatch(/NOT a diagnosis/i)

    await client.close()
  })
})
