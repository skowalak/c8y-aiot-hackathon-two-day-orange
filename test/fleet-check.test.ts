// Fleet check across 6 Power Node devices: prove the orchestrator finds the one
// device that is not working properly, using ONLY recorded data + the
// sim_PowerNode.md knowledge base (via the generic, knowledge-driven reasoner).
//
// No device-specific facts live in the agent, the MCP server, or the reasoner —
// the 207 V band, the restart-event symptom, etc. exist only in the Markdown.

import { describe, it, expect } from 'vitest'

import { runFleetCheck } from '../src/agent/fleet-check'
import { FLEET, makeFleetFetcher } from './fixtures/power-node'
import { genericReasoner } from './fixtures/generic-reasoner'

describe('fleet check — 6 Power Nodes, find the faulty one', () => {
  it('identifies the single culprit and ranks it first', async () => {
    const result = await runFleetCheck(undefined, {
      deviceIds: FLEET.map((d) => d.id),
      pass2WaitSeconds: 0,
      fetch: makeFleetFetcher(),
      reasoner: genericReasoner,
    })

    expect(result.checked).toBe(6)

    // Exactly one device flagged faulty.
    const faulty = result.ranking.filter((r) => r.verdict?.faulty)
    expect(faulty).toHaveLength(1)

    // The culprit is the reported-faulty node, ranked first.
    expect(result.culprit?.deviceId).toBe('dev-sim-node-01')
    expect(result.ranking[0].deviceId).toBe('dev-sim-node-01')

    // Its fault is located in the event domain (unplanned restarts are the
    // knowledge-base fault signature) and the summary names the device.
    expect(result.culprit?.verdict?.faultDomain).toBe('event')
    expect(result.summary).toContain('dev-sim-node-01')

    // The five healthy nodes are not flagged.
    const healthy = result.ranking.filter((r) => r.deviceId !== 'dev-sim-node-01')
    expect(healthy.every((r) => r.verdict && !r.verdict.faulty)).toBe(true)
  })

  it('reports no culprit when the knowledge base is not violated (all healthy)', async () => {
    // Reuse the fetcher but only pass the healthy device ids.
    const healthyIds = FLEET.filter((d) => !d.faulty).map((d) => d.id)
    const result = await runFleetCheck(undefined, {
      deviceIds: healthyIds,
      pass2WaitSeconds: 0,
      fetch: makeFleetFetcher(),
      reasoner: genericReasoner,
    })
    expect(result.culprit).toBeUndefined()
    expect(result.summary).toContain('No faulty device')
  })
})
