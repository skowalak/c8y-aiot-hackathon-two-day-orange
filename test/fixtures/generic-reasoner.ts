// A GENERIC, knowledge-driven stand-in for Claude used in tests.
//
// It contains NO device-specific facts. It reads the same two inputs the real
// agent gives the LLM — the recorded pass summaries and the knowledge-base
// Markdown — and derives the verdict purely from them:
//
//   1. Parse numeric expected ranges out of the Markdown (e.g. "207–253 V",
//      ">= 20 l/min", "35–45 °C", "below the 52 °C limit").
//   2. Match each parsed range to a recorded series by fuzzy name overlap
//      (the Markdown mentions "sim_Voltage.U" / "c8y_FlowRate"; the summary has
//      series keys like "sim_Voltage.U").
//   3. Flag a violation when a series' min/max falls outside its range, and only
//      keep it if it reproduces in BOTH passes.
//   4. Classify the fault domain by where the violations landed: measurement
//      (series out of range), alarm (self-clearing / active alarms present) or
//      event (unexpected event types the Markdown calls out as symptoms).
//
// If the knowledge file changes, the behaviour changes — which is exactly the
// property we want to prove: the MD drives the diagnosis, not the code.

import type { Reasoner } from '../../src/agent/loop'
import type { PassSummary, SeriesStat } from '../../src/store/analysis'

interface SessionSummary {
  pass1: PassSummary
  pass2: PassSummary
}

interface ParsedRange {
  // A human token from the MD used to match against series names, lowercased.
  token: string
  min?: number
  max?: number
}

// Normalise numbers written with unicode minus / en-dash / thin spaces.
function num(s: string): number {
  return Number(s.replace(/[−]/g, '-').replace(/[, ]/g, ''))
}

/**
 * Extract expected numeric ranges from the knowledge Markdown. Handles the two
 * shapes used in the knowledge files:
 *   "<name> ... 207–253 V"          -> min/max band
 *   "<name> ... >= 20 l/min"        -> min only
 *   "... below the 52 °C limit"     -> max only (attached to nearest name)
 * The `token` is the first `word_with.dots` or `word` on the line, which is what
 * we later fuzzy-match to a series key.
 */
function parseRanges(md: string): ParsedRange[] {
  const ranges: ParsedRange[] = []
  for (const rawLine of md.split('\n')) {
    const line = rawLine.trim()
    if (!line.startsWith('-')) continue

    // Candidate matching token: a backticked identifier if present, else the
    // first identifier-looking word.
    const idMatch =
      /`([A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)?)`/.exec(line) ??
      /([A-Za-z][A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)?)/.exec(line)
    if (!idMatch) continue
    const token = idMatch[1].toLowerCase()

    // Band "a–b" / "a-b" (allow decimals).
    const band = /(-?\d+(?:\.\d+)?)\s*[–—-]\s*(-?\d+(?:\.\d+)?)/.exec(line)
    if (band) {
      ranges.push({ token, min: num(band[1]), max: num(band[2]) })
      continue
    }
    // ">= a" / "at least a" / "min a".
    const minOnly = /(?:>=|≥|at least|min)\s*(-?\d+(?:\.\d+)?)/i.exec(line)
    if (minOnly) {
      ranges.push({ token, min: num(minOnly[1]) })
      continue
    }
    // "below the a limit" / "<= a" / "max a".
    const maxOnly = /(?:below the|<=|≤|max)\s*(-?\d+(?:\.\d+)?)/i.exec(line)
    if (maxOnly) {
      ranges.push({ token, max: num(maxOnly[1]) })
    }
  }
  return ranges
}

// Fuzzy match: does the MD token refer to this series? Compare on the
// dot-joined and underscore-stripped forms so "sim_voltage.u" matches
// "sim_Voltage.U" and "c8y_flowrate" matches "c8y_FlowRate.F".
function matchesSeries(token: string, series: string): boolean {
  const s = series.toLowerCase()
  if (s === token) return true
  if (s.startsWith(token) || token.startsWith(s)) return true
  const base = s.split('.')[0]
  return base === token || token.startsWith(base) || base.startsWith(token)
}

function findStat(summary: PassSummary, token: string): SeriesStat | undefined {
  return summary.measurementStats.find((st) => matchesSeries(token, st.series))
}

function violates(stat: SeriesStat, r: ParsedRange): boolean {
  if (r.min !== undefined && stat.min < r.min) return true
  if (r.max !== undefined && stat.max > r.max) return true
  return false
}

// Event/alarm types the MD explicitly frames as symptoms ("NOT expected",
// "self-clear", "unplanned", "fault signature"). Generic keyword scan over the
// lines that mention a given type token — no device names baked in.
function symptomTypes(md: string): string[] {
  const out = new Set<string>()
  const lines = md.split('\n')
  for (const line of lines) {
    const low = line.toLowerCase()
    const flagged =
      low.includes('not expected') ||
      low.includes('unplanned') ||
      low.includes('self-clear') ||
      low.includes('fault signature') ||
      low.includes('symptom')
    if (!flagged) continue
    for (const m of line.matchAll(/`([A-Za-z0-9_]+)`/g)) {
      out.add(m[1].toLowerCase())
    }
  }
  return [...out]
}

function buildVerdict(md: string, s: SessionSummary): string {
  const ranges = parseRanges(md)
  const symptoms = symptomTypes(md)

  const evidence: any[] = []
  let measurementViolation = false

  for (const r of ranges) {
    const st1 = findStat(s.pass1, r.token)
    const st2 = findStat(s.pass2, r.token)
    if (!st1 || !st2) continue
    const v1 = violates(st1, r)
    const v2 = violates(st2, r)
    if (v1 && v2) {
      measurementViolation = true
      evidence.push({
        pass: 1,
        signal: st1.series,
        observed: `min ${st1.min}, max ${st1.max}`,
        expected: `${r.min ?? '-'}..${r.max ?? '-'}`,
        verdict: 'violation',
      })
      evidence.push({
        pass: 2,
        signal: st2.series,
        observed: `min ${st2.min}, max ${st2.max}`,
        expected: `${r.min ?? '-'}..${r.max ?? '-'}`,
        verdict: 'violation (reproduced)',
      })
    } else if (v1 || v2) {
      evidence.push({
        pass: v1 ? 1 : 2,
        signal: (st1 ?? st2).series,
        observed: 'out of range in one pass only',
        expected: `${r.min ?? '-'}..${r.max ?? '-'}`,
        verdict: 'transient (not reproduced)',
      })
    }
  }

  // Symptom events/alarms present in the recorded data?
  const eventTypes = [
    ...s.pass1.eventTallies.map((e) => e.type.toLowerCase()),
    ...s.pass2.eventTallies.map((e) => e.type.toLowerCase()),
  ]
  const alarmActiveOrClearing =
    [...s.pass1.alarmTallies, ...s.pass2.alarmTallies].length > 0
  const symptomEventPresent = symptoms.some((sym) =>
    eventTypes.some((et) => et.includes(sym) || sym.includes(et)),
  )
  const symptomAlarmPresent =
    alarmActiveOrClearing &&
    symptoms.some((sym) =>
      [...s.pass1.alarmTallies, ...s.pass2.alarmTallies].some((a) =>
        a.type.toLowerCase().includes(sym),
      ),
    )

  // Decide domain by where the strongest evidence is. Preference order reflects
  // the MD framing: a symptom event (unplanned restart) is the primary symptom;
  // then alarms; then raw measurement violations.
  let faulty = false
  let faultDomain: 'measurement' | 'alarm' | 'event' | 'none' = 'none'
  let rootCause = 'All monitored signals within expected ranges across both passes.'
  let confidence = 0.8

  if (symptomEventPresent) {
    faulty = true
    faultDomain = 'event'
    rootCause =
      'Unexpected symptom events recorded together with out-of-range readings — ' +
      'the knowledge base flags these as the fault signature.'
    confidence = 0.85
    for (const e of s.pass1.eventTallies) {
      if (symptoms.some((sym) => e.type.toLowerCase().includes(sym))) {
        evidence.push({
          pass: 1,
          signal: e.type,
          observed: `${e.count} occurrence(s)`,
          expected: 'not expected in healthy operation',
          verdict: 'violation',
        })
      }
    }
  } else if (symptomAlarmPresent) {
    faulty = true
    faultDomain = 'alarm'
    rootCause = 'Alarms matching the knowledge-base fault signature are present.'
    confidence = 0.8
  } else if (measurementViolation) {
    faulty = true
    faultDomain = 'measurement'
    rootCause =
      'A measured series stayed outside its expected range across both passes.'
    confidence = 0.8
  }

  return JSON.stringify({
    faulty,
    faultDomain,
    rootCause,
    confidence,
    evidence,
    recommendation: faulty
      ? 'Investigate the flagged domain; see evidence.'
      : 'No action required.',
  })
}

export const genericReasoner: Reasoner = {
  async hypothesize(md, pass1Summary) {
    const ranges = parseRanges(md)
    const p1 = pass1Summary as PassSummary
    const flagged = ranges
      .map((r) => {
        const st = findStat(p1, r.token)
        return st && violates(st, r) ? st.series : undefined
      })
      .filter(Boolean)
    return flagged.length
      ? `Pass-1 candidates out of range: ${flagged.join(', ')}. Confirm in pass 2.`
      : 'No range violations in pass 1; check alarms/events in pass 2.'
  },

  async conclude(md, bothPassesSummary) {
    return buildVerdict(md, bothPassesSummary as SessionSummary)
  },
}
