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

## Fleet check — find the faulty device among many

`POST /service/diagnostic-agent/fleet-check` diagnoses several devices, ranks them
and names the single device not working properly. Select the devices by one of:

```jsonc
// explicit managed-object ids:
{ "deviceIds": ["1234", "1235", ...], "pass2WaitSeconds": 0 }
// serials resolved via external id:
{ "serials": ["sim-node-01", "sim-node-02", ...] }
// or discover a whole device type:
{ "discoverType": "sim_PowerNode" }
```

Response: `{ checked, ranking: [worst-first], culprit, summary }`. Each device is
diagnosed against **its knowledge-base file only** — no device rules live in the
agent or MCP code.

## Domain knowledge lives ONLY in the knowledge base

The expected-behaviour of each device class is written in `src/knowledge/*.md`
(e.g. `sim_PowerNode.md` from `TICKET.md`). Neither the agent, the MCP server, nor
the tests hard-code thresholds — Claude (or, in tests, a generic
knowledge-driven reasoner) reads the recorded data + the Markdown and derives the
verdict. Change the `.md` and the diagnosis changes.

## Layout

```
nitro.config.ts            # c8y-nitro module + manifest (contextPath, roles)
Dockerfile                 # standalone multi-stage image (see below)
src/
  routes/
    diagnose.post.ts        # POST /service/diagnostic-agent/diagnose (one device)
    fleet-check.post.ts     # POST /service/diagnostic-agent/fleet-check (many)
    mcp/[...].ts            # MCP (Streamable HTTP / SSE) transport
    health.get.ts
  agent/                    # loop.ts, fleet-check.ts, prompts.ts, verdict.ts
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
**generic, knowledge-driven reasoner** (it parses ranges out of the knowledge
Markdown; it holds no device facts). Coverage:

- `test/mcp-server.test.ts` — connects an MCP client to the diagnostic server over
  the SDK's in-memory transport; exercises `record_window`, `query_local`,
  `get_expectations`, `clear_session`.
- `test/agent-diagnosis.test.ts` — drives the full agent loop against a simulated
  **failing water pump** (flow stuck at 0, below the 20 l/min the knowledge base
  expects) and asserts `faulty: true`, `faultDomain: "measurement"`, flow
  violation reproduced across both passes. Healthy control + cleanup-on-error.
- `test/fleet-check.test.ts` — **6 Power Nodes** (`TICKET.md` envelope); asserts the
  orchestrator flags exactly one culprit (`sim-node-01`), ranks it first, and
  locates the fault in the **event** domain (unplanned restarts) — derived only
  from data + `sim_PowerNode.md`.
- `test/fixtures/` — raw-c8y-shaped telemetry (`water-pump.ts`, `power-node.ts`)
  and the generic reasoner (`generic-reasoner.ts`).

## Try it

Single device:

```sh
curl -X POST http://localhost:3000/diagnose \
  -H 'content-type: application/json' \
  -d '{ "deviceId": "12345", "pass2WaitSeconds": 0 }'
```

Fleet (find the faulty Power Node among the simulator's 6 devices, live):

```sh
curl -X POST http://localhost:3000/fleet-check \
  -H 'content-type: application/json' \
  -d '{ "discoverType": "sim_PowerNode", "pass2WaitSeconds": 0 }'
```

Set `pass2WaitSeconds: 0` for a fast demo (skips the wait between passes).

## Build & package

Two options.

**c8y-nitro native** (produces the deployable zip + cumulocity.json; needs Docker
on the build host):

```sh
npm run build
```

Upload the zip in Cumulocity Application Management and subscribe. The MCP
endpoint is then reachable at `/service/diagnostic-agent/mcp`.

**Standalone Dockerfile** (build the container directly):

```sh
docker build -t c8y-diagnostic-agent .
docker run --rm -p 8080:80 --env-file .env c8y-diagnostic-agent
```

The image builds the Nitro `.output/` and runs it on port 80. Secrets
(`ANTHROPIC_API_KEY`, and in dev the `C8Y_*` vars) are supplied at run time via
`--env-file` / the platform — never baked into the image.

## Known follow-ups

- `codemode` sandbox tool (mc8yp-style typed c8y namespaces) is stubbed out in
  `src/mcp/server.ts`; the four diagnostic tools drive the loop today.
- HTML/Markdown briefing publishing to Application Hosting (verdict `reportUrl`)
  is not yet implemented.
- Neighbor-asset correlation is out of scope for v1.
