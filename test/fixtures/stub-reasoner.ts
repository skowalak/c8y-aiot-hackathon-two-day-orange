// A deterministic stub Reasoner that stands in for Claude in tests.
//
// It does NOT hard-code the answer: it inspects the real pass summaries produced
// by queryLocal (min/max/avg per series, alarm/event tallies) and derives the
// verdict from the numbers — the same signal a real LLM would key on. This lets
// the agent test prove the *recorded data* drives the diagnosis, deterministically
// and without an API key.

import type { Reasoner } from '../../src/agent/loop'
import type { PassSummary, SeriesStat } from '../../src/store/analysis'

interface SessionSummary {
  pass1: PassSummary
  pass2: PassSummary
}

function findSeries(summary: PassSummary, series: string): SeriesStat | undefined {
  return summary.measurementStats.find((s) => s.series === series)
}

// Expected ranges parsed from the water-pump knowledge base.
const FLOW_MIN = 20
const PRESSURE_MIN = 2.0
const PRESSURE_MAX = 6.0

export const stubReasoner: Reasoner = {
  async hypothesize(_md, pass1Summary) {
    const p1 = pass1Summary as PassSummary
    const flow = findSeries(p1, 'c8y_FlowRate.F')
    if (flow && flow.max === 0) {
      return 'Hypothesis: c8y_FlowRate.F is 0 while pressure looks normal — ' +
        'suspected mechanical blockage (measurement domain, confidence 0.7). ' +
        'Confirm flow stays at 0 in pass 2.'
    }
    return 'No clear violation in pass 1.'
  },

  async conclude(_md, bothPassesSummary, _hypotheses) {
    const s = bothPassesSummary as SessionSummary
    const flow1 = findSeries(s.pass1, 'c8y_FlowRate.F')
    const flow2 = findSeries(s.pass2, 'c8y_FlowRate.F')
    const pressure1 = findSeries(s.pass1, 'c8y_Pressure.P')

    const flowZeroBothPasses =
      !!flow1 && !!flow2 && flow1.max === 0 && flow2.max === 0
    const pressureNormal =
      !!pressure1 && pressure1.min >= PRESSURE_MIN && pressure1.max <= PRESSURE_MAX

    if (flowZeroBothPasses && pressureNormal) {
      return JSON.stringify({
        faulty: true,
        faultDomain: 'measurement',
        rootCause:
          'Flow rate reads 0 l/min while pump pressure is normal — suspected ' +
          'mechanical blockage or flow-sensor failure.',
        confidence: 0.85,
        evidence: [
          {
            pass: 1,
            signal: 'c8y_FlowRate.F',
            observed: `${flow1!.avg} l/min avg`,
            expected: `>= ${FLOW_MIN} l/min`,
            verdict: 'violation',
          },
          {
            pass: 2,
            signal: 'c8y_FlowRate.F',
            observed: `${flow2!.avg} l/min avg`,
            expected: `>= ${FLOW_MIN} l/min`,
            verdict: 'violation (reproduced)',
          },
          {
            pass: 1,
            signal: 'c8y_Pressure.P',
            observed: `${pressure1!.avg.toFixed(2)} bar avg`,
            expected: `${PRESSURE_MIN}-${PRESSURE_MAX} bar`,
            verdict: 'normal',
          },
        ],
        recommendation:
          'Dispatch technician to inspect pump impeller and flow sensor.',
      })
    }

    return JSON.stringify({
      faulty: false,
      faultDomain: 'none',
      rootCause: 'All monitored signals within expected ranges across both passes.',
      confidence: 0.8,
      evidence: [],
      recommendation: 'No action required.',
    })
  },
}
