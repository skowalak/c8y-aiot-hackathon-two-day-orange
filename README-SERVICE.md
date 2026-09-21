# c8y Diagnostic Agent — Nitro Microservice

AI diagnostic agent + diagnostic MCP server for Cumulocity IoT, built on
[c8y-nitro](https://schplitt.github.io/c8y-nitro/). See
[`docs/FUNCTIONAL_DESCRIPTION.md`](docs/FUNCTIONAL_DESCRIPTION.md) for the full design.

## What it does

Given a device id, the agent runs a two-pass diagnosis and reports whether the
device is faulty and in which domain (**measurement / alarm / event**):

1. **Record** — pull alarms/measurements/events into an in-memory session store.
2. **Analyze** — compare against the Markdown knowledge base (expected behaviour).
3. **Re-sample** — record a second window to rule out transients.
4. **Conclude** — Claude produces a structured verdict.
5. **Clear** — the in-memory telemetry is wiped (always, even on error).

## Layout

```
nitro.config.ts            # c8y-nitro module + manifest (contextPath, roles)
src/
  routes/
    diagnose.post.ts        # POST /service/diagnostic-agent/diagnose
    mcp/[...].ts            # MCP (Streamable HTTP / SSE) transport
    health.get.ts
  agent/                    # loop.ts, prompts.ts, verdict.ts (Claude)
  mcp/
    server.ts               # MCP server wiring
    tools/                  # record-window, query-local, get-expectations, clear-session
  c8y/client.ts             # read-only c8y REST via useUserClient
  store/                    # in-memory session store, types, analysis
  knowledge/                # *.md expected-behaviour specs + loader
```

## Credentials — where things go

Everything lives in a single **`.env`** file (copy from `.env.example`). It is
gitignored. Two groups:

| What | Env var(s) | Used by |
|---|---|---|
| **Anthropic API key** (the AI agent) | `ANTHROPIC_API_KEY`, `ANTHROPIC_MODEL` | `nitro.config.ts` → `runtimeConfig.anthropicApiKey`; read in `src/agent/loop.ts` |
| **Cumulocity tenant creds** (dev only) | `C8Y_BASEURL`, `C8Y_DEVELOPMENT_TENANT`, `C8Y_DEVELOPMENT_USER`, `C8Y_DEVELOPMENT_PASSWORD` | c8y-nitro dev bootstrap; the authenticated client behind `useUserClient` |

In a **deployed** microservice the Cumulocity credentials are injected by the
platform (service user) — you do **not** ship the `C8Y_DEVELOPMENT_*` values.

## Setup

```sh
npm install
cp .env.example .env        # fill in tenant creds + ANTHROPIC_API_KEY
npm run dev
```

## Tests

```sh
npm test
```

No credentials required — the tests inject a simulated Cumulocity fetcher and a
deterministic stub reasoner. Coverage:

- `test/mcp-server.test.ts` — connects an MCP client to the diagnostic server over
  the SDK's in-memory transport; exercises `record_window`, `query_local`,
  `get_expectations`, `clear_session`.
- `test/agent-diagnosis.test.ts` — drives the full agent loop against a simulated
  **failing water pump** (flow stuck at 0, pressure normal) and asserts the
  verdict is `faulty: true`, `faultDomain: "measurement"`, with the flow violation
  reproduced across both passes. Includes a healthy control and cleanup-on-error.
- `test/fixtures/` — the raw-c8y-shaped pump telemetry and the stub reasoner.

## Try it

```sh
curl -X POST http://localhost:3000/diagnose \
  -H 'content-type: application/json' \
  -d '{ "deviceId": "12345", "pass2WaitSeconds": 0 }'
```

Set `pass2WaitSeconds: 0` for a fast demo (skips the wait between passes).

## Build & package

```sh
pnpm build     # produces Docker image, cumulocity.json, deployable zip (c8y-nitro)
```

Upload the zip in Cumulocity Application Management and subscribe. The MCP
endpoint is then reachable at `/service/diagnostic-agent/mcp`.

## Known follow-ups

- `codemode` sandbox tool (mc8yp-style typed c8y namespaces) is stubbed out in
  `src/mcp/server.ts`; the four diagnostic tools drive the loop today.
- HTML/Markdown briefing publishing to Application Hosting (verdict `reportUrl`)
  is not yet implemented.
- Neighbor-asset correlation is out of scope for v1.
