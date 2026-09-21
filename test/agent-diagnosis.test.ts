// Tests the full diagnostic agent loop (record -> analyze -> re-sample ->
// conclude -> clear) end to end, with injected dependencies:
//   - a simulated Cumulocity fetcher serving faulty water-pump telemetry
//   - a deterministic stub reasoner standing in for Claude
//
// The key assertion: given a pump whose flow is stuck at 0 while pressure is
// normal, the agent must conclude the device is FAULTY and locate the cause in
// the MEASUREMENT domain — reproduced across both sampling passes.

import { describe, it, expect } from 'vitest'

import { runDiagnosis } from '../src/agent/loop'
import { getSession } from '../src/store/session-store'
import {
  FAULTY_PUMP_DEVICE_ID,
  makePumpFetcher,
} from './fixtures/water-pump'
import { stubReasoner } from './fixtures/stub-reasoner'

describe('diagnostic agent — faulty water pump', () => {
  it('identifies a measurement-domain fault (flow zero, pressure normal)', async () => {
    const verdict = await runDiagnosis(undefined, {
      sessionId: 'agent-faulty',
      deviceId: FAULTY_PUMP_DEVICE_ID,
      pass2WaitSeconds: 0, // no real wait in tests
      fetch: makePumpFetcher({ faulty: true }),
      reasoner: stubReasoner,
    })

    expect(verdict.faulty).toBe(true)
    expect(verdict.faultDomain).toBe('measurement')
    expect(verdict.deviceType).toBe('c8y_Water_Pump')
    expect(verdict.rootCause.toLowerCase()).toContain('flow')
    expect(verdict.confidence).toBeGreaterThan(0.5)

    // Evidence must cite the reproduced flow violation across both passes.
    const flowEvidence = verdict.evidence.filter((e) =>
      e.signal.includes('FlowRate'),
    )
    expect(flowEvidence.map((e) => e.pass).sort()).toEqual([1, 2])
    expect(flowEvidence.every((e) => e.verdict.includes('violation'))).toBe(true)

    // Lifecycle: start/stop stamped, duration present.
    expect(verdict.startedAt).toBeTruthy()
    expect(verdict.stoppedAt).toBeTruthy()
    expect(verdict.durationMs).toBeGreaterThanOrEqual(0)
  })

  it('wipes the session from memory after the diagnosis (cleanup)', async () => {
    const sessionId = 'agent-cleanup'
    await runDiagnosis(undefined, {
      sessionId,
      deviceId: FAULTY_PUMP_DEVICE_ID,
      pass2WaitSeconds: 0,
      fetch: makePumpFetcher({ faulty: true }),
      reasoner: stubReasoner,
    })
    expect(getSession(sessionId)).toBeUndefined()
  })

  it('reports a healthy pump as not faulty (control)', async () => {
    const verdict = await runDiagnosis(undefined, {
      sessionId: 'agent-healthy',
      deviceId: FAULTY_PUMP_DEVICE_ID,
      pass2WaitSeconds: 0,
      fetch: makePumpFetcher({ faulty: false }),
      reasoner: stubReasoner,
    })

    expect(verdict.faulty).toBe(false)
    expect(verdict.faultDomain).toBe('none')
  })

  it('still wipes the session if reasoning throws (cleanup on error)', async () => {
    const sessionId = 'agent-error'
    const throwingReasoner = {
      hypothesize: async () => {
        throw new Error('boom')
      },
      conclude: async () => '{}',
    }

    await expect(
      runDiagnosis(undefined, {
        sessionId,
        deviceId: FAULTY_PUMP_DEVICE_ID,
        pass2WaitSeconds: 0,
        fetch: makePumpFetcher({ faulty: true }),
        reasoner: throwingReasoner,
      }),
    ).rejects.toThrow('boom')

    // The finally block must have cleared the session even on failure.
    expect(getSession(sessionId)).toBeUndefined()
  })
})
