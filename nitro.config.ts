import { defineNitroConfig } from 'nitro/config'
import c8y from 'c8y-nitro'
import pkg from './package.json' with { type: 'json' }

// c8y-nitro handles the Cumulocity microservice bootstrap, credential/tenant
// helpers, and generates the Docker image + cumulocity.json + deployable zip
// as part of `nitro build`.
export default defineNitroConfig({
  preset: 'node-server',
  // Inject the package version so the MCP `initialize` handshake reports the
  // actual deployed build (src/mcp/server.ts reads __SERVER_VERSION__).
  replace: {
    __SERVER_VERSION__: JSON.stringify(pkg.version),
  },
  builder: 'rolldown',
  // App code lives under src/ — scan src/routes/ for file-based routes.
  serverDir: 'src',
  modules: [c8y()],

  c8y: {
    // Cast: openApiSpec / exposeMcpServers are valid Cumulocity manifest fields
    // that c8y-nitro passes through verbatim (it spreads `...options` into the
    // generated cumulocity.json), but they are not yet in its TS option type.
    manifest: {
      // Exposed to the AI Agent Manager / clients at /service/<contextPath>
      contextPath: 'diagnostic-agent',
      // Read-only access to the device data domains we diagnose. Includes
      // ROLE_INVENTORY_READ, which also covers reading the knowledge Markdown
      // from the Cumulocity file repository (Inventory Binaries / Dateiablage).
      requiredRoles: [
        'ROLE_ALARM_READ',
        'ROLE_MEASUREMENT_READ',
        'ROLE_EVENT_READ',
        'ROLE_INVENTORY_READ',
      ],
      // Published OpenAPI spec route (served by Nitro) so the AI Agent Manager
      // and mc8yp can discover this microservice's HTTP surface.
      openApiSpec: 'openapi.json',
      // MCP servers advertised to the Cumulocity AI Agent Manager so it can
      // discover and call their tools.
      exposeMcpServers: [
        {
          // THIS microservice's own MCP server — exposes the diagnostic tools,
          // primarily `check_device_types` ("check these device types for
          // anomalies"). This is what makes our tool findable by the AI agent.
          name: 'diagnostic-agent',
          description:
            'Autonomous device-anomaly diagnostic MCP server. Primary tool: ' +
            'check_device_types(deviceTypes[]) — discovers all devices of the ' +
            'given types, records their telemetry, checks them against the ' +
            'expected-behaviour knowledge files in the Cumulocity file ' +
            'repository, validates over two sampling passes, and returns the ' +
            'defective device(s) with the fault domain and root cause.',
          url: '/service/diagnostic-agent/mcp',
          type: 'http',
          sendAuthentication: true,
        },
      ],
    } as Record<string, unknown>,
  },

  runtimeConfig: {
    // NOTE: these are BUILD-TIME defaults only. process.env here is the build
    // host's environment, so on a CI/dev machine without the key exported the
    // baked value is '' — which is what caused "ANTHROPIC_API_KEY is not
    // configured" in the deployed microservice.
    //
    // At runtime Nitro overrides these from env vars under its own prefix
    // (NITRO_ANTHROPIC_API_KEY / _ANTHROPIC_API_KEY), NOT the plain name. To
    // keep the obvious `ANTHROPIC_API_KEY` working as a Cumulocity microservice
    // setting, src/config.ts falls back to the plain names at request time.
    anthropicApiKey: process.env.ANTHROPIC_API_KEY ?? '',
    anthropicModel: process.env.ANTHROPIC_MODEL ?? 'claude-opus-4-8',
  },
})
