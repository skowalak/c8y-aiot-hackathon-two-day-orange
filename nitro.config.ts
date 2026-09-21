import { defineNitroConfig } from 'nitro/config'
import c8y from 'c8y-nitro'

// c8y-nitro handles the Cumulocity microservice bootstrap, credential/tenant
// helpers, and generates the Docker image + cumulocity.json + deployable zip
// as part of `nitro build`.
export default defineNitroConfig({
  preset: 'node-server',
  builder: 'rolldown',
  // App code lives under src/ — scan src/routes/ for file-based routes.
  serverDir: 'src',
  modules: [c8y()],

  c8y: {
    manifest: {
      // Exposed to the AI Agent Manager / clients at /service/<contextPath>
      contextPath: 'diagnostic-agent',
      // Read-only access to the device data domains we diagnose.
      requiredRoles: [
        'ROLE_ALARM_READ',
        'ROLE_MEASUREMENT_READ',
        'ROLE_EVENT_READ',
        'ROLE_INVENTORY_READ',
      ],
    },
  },

  runtimeConfig: {
    // Provisioned via env / tenant options. See .env.example.
    anthropicApiKey: process.env.ANTHROPIC_API_KEY ?? '',
    anthropicModel: process.env.ANTHROPIC_MODEL ?? 'claude-opus-4-8',
  },
})
