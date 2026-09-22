// The in-process diagnostic agent: record -> analyze -> re-sample -> conclude -> clear.
// Mirrors §7 of the functional description. The reasoning steps call Claude; the
// data steps call the diagnostic tool functions directly (same code the MCP
// server exposes).
//
// Dependencies (the Cumulocity fetcher and the reasoning/LLM function) are
// injectable so the whole loop can be driven in tests with simulated telemetry
// and a stub reasoner — no live Cumulocity or Anthropic key required.

import type { H3Event } from 'nitro/h3'
import type Anthropic from '@anthropic-ai/sdk'

import { fetchDeviceType, fetcherFromEvent, textFetcherFromEvent } from '../c8y/client'
import { resolveAnthropicConfig } from '../config'
import { getLogger } from '../log'
import type { C8yFetcher, C8yTextFetcher } from '../c8y/client'
import { recordWindow } from '../mcp/tools/record-window'
import { queryLocal } from '../mcp/tools/query-local'
import { getExpectations } from '../mcp/tools/get-expectations'
import { clearSession } from '../mcp/tools/clear-session'
import {
  requireSession,
  sessionDurationMs,
  startSession,
  stopSession,
} from '../store/session-store'
import type { DiagnosticVerdict } from '../store/types'
import { analyzePrompt, SYSTEM_PROMPT, verdictPrompt } from './prompts'
import { parseVerdict } from './verdict'

/** The two reasoning steps the agent needs. Backed by Claude in production. */
export interface Reasoner {
  /** Pass-1 hypotheses (free text). */
  hypothesize(expectationsMarkdown: string, pass1Summary: unknown): Promise<string>
  /** Final verdict — must return JSON parseable by parseVerdict. */
  conclude(
    expectationsMarkdown: string,
    bothPassesSummary: unknown,
    hypotheses: string,
  ): Promise<string>
}

export interface DiagnoseOptions {
  sessionId: string
  deviceId: string
  // Sampling windows (see open question §11.1). Defaults: last 30 min, wait 2 min,
  // then a 2-min live second window. Overridable per request.
  pass1WindowMinutes?: number
  pass2WaitSeconds?: number
  pass2WindowMinutes?: number
  // Injected dependencies (production builds these from the event + config).
  fetch?: C8yFetcher
  // Optional text fetcher for reading the knowledge Markdown from the Cumulocity
  // file repository (Dateiablage). When omitted, bundled knowledge files are used.
  text?: C8yTextFetcher
  reasoner?: Reasoner
}

function isoMinutesAgo(base: number, minutes: number): string {
  return new Date(base - minutes * 60_000).toISOString()
}

/** Claude-backed reasoner used in production. */
export function claudeReasoner(client: Anthropic, model: string): Reasoner {
  const ask = async (prompt: string): Promise<string> => {
    const res = await client.messages.create({
      model,
      max_tokens: 2048,
      system: SYSTEM_PROMPT,
      messages: [{ role: 'user', content: prompt }],
    })
    return res.content
      .filter((b): b is Anthropic.TextBlock => b.type === 'text')
      .map((b) => b.text)
      .join('\n')
  }
  return {
    hypothesize: (md, summary) => ask(analyzePrompt(md, summary)),
    conclude: (md, both, hyp) => ask(verdictPrompt(md, both, hyp)),
  }
}

/**
 * Build the production dependencies from the request event + runtime config.
 * Uses dynamic imports so the Nitro/Anthropic runtime modules load lazily and
 * are never pulled into a plain test process (which injects its own deps).
 */
async function productionDeps(
  event: H3Event,
): Promise<{ fetch: C8yFetcher; text: C8yTextFetcher; reasoner: Reasoner }> {
  const [{ useRuntimeConfig }, { default: Anthropic }] = await Promise.all([
    import('nitro/runtime-config'),
    import('@anthropic-ai/sdk'),
  ])
  // Resolve through src/config.ts rather than reading runtimeConfig directly:
  // the build-time value is usually '' and the platform setting may be under the
  // plain ANTHROPIC_API_KEY name, which Nitro itself does not pick up.
  const cfg = resolveAnthropicConfig(useRuntimeConfig())
  getLogger('config').info('anthropic config resolved', {
    source: cfg.source, // e.g. runtimeConfig | env:ANTHROPIC_API_KEY — never the key
    model: cfg.model,
  })
  const claude = new Anthropic({ apiKey: cfg.apiKey })
  return {
    fetch: fetcherFromEvent(event),
    text: textFetcherFromEvent(event),
    reasoner: claudeReasoner(claude, cfg.model),
  }
}

/**
 * Run the full two-pass diagnosis. Pass `fetch`/`reasoner` in opts to inject
 * dependencies (tests); otherwise they are built from the H3 event + config.
 */
export async function runDiagnosis(
  event: H3Event | undefined,
  opts: DiagnoseOptions,
): Promise<DiagnosticVerdict> {
  const {
    sessionId,
    deviceId,
    pass1WindowMinutes = 30,
    pass2WaitSeconds = 120,
    pass2WindowMinutes = 2,
  } = opts

  let fetch = opts.fetch
  let text = opts.text
  let reasoner = opts.reasoner
  if (!fetch || !reasoner) {
    if (!event) {
      throw new Error('runDiagnosis requires an H3 event or injected fetch+reasoner')
    }
    const deps = await productionDeps(event)
    fetch = fetch ?? deps.fetch
    text = text ?? deps.text
    reasoner = reasoner ?? deps.reasoner
  }

  // --- Resolve device type + open session ---
  const deviceType = await fetchDeviceType(fetch, deviceId)
  startSession(sessionId, deviceId, deviceType)

  try {
    // Prefer the knowledge Markdown from the Cumulocity file repository
    // (Dateiablage) when a text fetcher is available; else bundled files.
    const expectations = await getExpectations(
      { sessionId },
      text ? { json: fetch, text } : undefined,
    )

    // --- PASS 1: RECORD ---
    const now1 = Date.now()
    await recordWindow(fetch, {
      sessionId,
      deviceId,
      from: isoMinutesAgo(now1, pass1WindowMinutes),
      to: new Date(now1).toISOString(),
      pass: 1,
    })

    // --- ANALYZE ---
    const pass1Summary = queryLocal({ sessionId, pass: 1 })
    const hypotheses = await reasoner.hypothesize(
      expectations.markdown,
      pass1Summary,
    )

    // --- PASS 2: RE-SAMPLE ---
    if (pass2WaitSeconds > 0) {
      await new Promise((r) => setTimeout(r, pass2WaitSeconds * 1000))
    }
    const now2 = Date.now()
    await recordWindow(fetch, {
      sessionId,
      deviceId,
      from: isoMinutesAgo(now2, pass2WindowMinutes),
      to: new Date(now2).toISOString(),
      pass: 2,
    })

    // --- CONCLUDE ---
    const bothSummary = queryLocal({ sessionId })
    const verdictText = await reasoner.conclude(
      expectations.markdown,
      bothSummary,
      hypotheses,
    )
    const parsed = parseVerdict(verdictText)

    stopSession(sessionId)
    const session = requireSession(sessionId)

    const verdict: DiagnosticVerdict = {
      deviceId,
      deviceType,
      ...parsed,
      startedAt: session.startedAt,
      stoppedAt: session.stoppedAt,
      durationMs: sessionDurationMs(session),
    }
    return verdict
  } finally {
    // --- CLEANUP --- always wipe the buffered telemetry, even on error.
    clearSession({ sessionId })
  }
}
