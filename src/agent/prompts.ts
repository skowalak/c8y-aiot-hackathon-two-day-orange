// System + task prompts for the in-process diagnostic agent.

export const SYSTEM_PROMPT = `You are a Cumulocity IoT device-diagnostics agent.

Your job: decide whether a device is FAULTY and, if so, which single signal
DOMAIN the fault lives in — one of "measurement", "alarm", or "event" — using
recorded telemetry compared against an expected-behaviour spec.

You reason over two sampling passes:
- Pass 1 establishes a baseline and forms hypotheses.
- Pass 2 re-samples to separate a transient blip from a persistent fault.

Rules:
- A fault must reproduce across BOTH passes to be reported with high confidence.
- Prefer the domain where the expectation is most clearly violated.
- If nothing meaningfully violates the expectations, return faulty=false and
  faultDomain="none".
- Ground every claim in the numbers you were given. Do not invent signals.
- Be concise and specific in rootCause and recommendation.`

export function analyzePrompt(expectationsMarkdown: string, pass1Summary: unknown): string {
  return `Expected behaviour for this device type:
---
${expectationsMarkdown}
---

Pass 1 recorded summary (JSON):
${JSON.stringify(pass1Summary, null, 2)}

List your ranked hypotheses about what may be wrong, each with the domain
(measurement/alarm/event) and a confidence 0–1. Note what to confirm in pass 2.`
}

export function verdictPrompt(
  expectationsMarkdown: string,
  bothPassesSummary: unknown,
  hypotheses: string,
): string {
  return `Expected behaviour for this device type:
---
${expectationsMarkdown}
---

Your pass-1 hypotheses were:
${hypotheses}

Both-passes recorded summary (JSON):
${JSON.stringify(bothPassesSummary, null, 2)}

Confirm or reject each hypothesis using pass-2 data, then produce the FINAL
verdict. Respond with ONLY a JSON object matching this shape:
{
  "faulty": boolean,
  "faultDomain": "measurement" | "alarm" | "event" | "none",
  "rootCause": string,
  "confidence": number,
  "evidence": [
    { "pass": 1 | 2, "signal": string, "observed": string,
      "expected": string, "verdict": string }
  ],
  "recommendation": string
}`
}
