// Runtime resolution of the Anthropic credentials.
//
// WHY THIS EXISTS: `runtimeConfig` in nitro.config.ts is evaluated on the BUILD
// host, so `process.env.ANTHROPIC_API_KEY` there is usually empty and '' gets
// baked into the image. Nitro can override it at runtime, but only from env vars
// under its own prefix — `NITRO_ANTHROPIC_API_KEY` (or `_ANTHROPIC_API_KEY`) —
// derived from the camelCase config key. A Cumulocity microservice setting named
// plain `ANTHROPIC_API_KEY` is therefore ignored, which is exactly how the
// deployed service ended up reporting "ANTHROPIC_API_KEY is not configured"
// while the key looked correctly set on the platform.
//
// So we resolve in precedence order and accept every name someone might
// reasonably use, including the plain one (and the same for the model).

export interface AnthropicConfig {
  apiKey: string
  model: string
  /** Which source supplied the key — logged (never the key itself) to debug provisioning. */
  source: string
}

const DEFAULT_MODEL = 'claude-opus-4-8'

/** manifest.settings requires a non-empty default; this value means "not configured". */
const PLACEHOLDER_KEY = 'unset'

const KEY_VARS = [
  'ANTHROPIC_API_KEY', // plain name: what a person naturally sets on the platform
  'NITRO_ANTHROPIC_API_KEY', // Nitro's runtimeConfig prefix
  '_ANTHROPIC_API_KEY', // Nitro's altPrefix
  'C8Y_ANTHROPIC_API_KEY', // some tenants prefix microservice settings
]

const MODEL_VARS = [
  'ANTHROPIC_MODEL',
  'NITRO_ANTHROPIC_MODEL',
  '_ANTHROPIC_MODEL',
  'C8Y_ANTHROPIC_MODEL',
]

function fromEnv(names: string[]): { value: string; source: string } | undefined {
  const env = globalThis.process?.env ?? {}
  for (const name of names) {
    const value = env[name]
    if (typeof value === 'string' && value.trim() !== '') {
      return { value: value.trim(), source: `env:${name}` }
    }
  }
  return undefined
}

/**
 * Resolve the Anthropic config. `runtimeConfig` (which Nitro has already
 * populated from its own prefixed env vars) wins; a plain env var is the
 * fallback that makes an `ANTHROPIC_API_KEY` platform setting work.
 *
 * Throws with an actionable message when no key is present anywhere.
 */
export function resolveAnthropicConfig(
  // Nitro types this as NitroRuntimeConfig, whose declared shape carries none of
  // our custom keys, so accept a loose record and read the two we care about.
  runtimeConfig?: Record<string, unknown>,
): AnthropicConfig {
  const readString = (key: string): string | undefined => {
    const value = runtimeConfig?.[key]
    return typeof value === 'string' ? value : undefined
  }

  const fromRuntime = readString('anthropicApiKey')?.trim()
  const key = fromRuntime
    ? { value: fromRuntime, source: 'runtimeConfig' }
    : fromEnv(KEY_VARS)

  if (!key) {
    throw new Error(
      'ANTHROPIC_API_KEY is not configured. Set it as a microservice setting ' +
        '(any of: ANTHROPIC_API_KEY, NITRO_ANTHROPIC_API_KEY) in Cumulocity, ' +
        'or export it locally in .env, then restart the microservice.',
    )
  }

  const runtimeModel = readString('anthropicModel')?.trim()
  const model = runtimeModel || fromEnv(MODEL_VARS)?.value || DEFAULT_MODEL

  return { apiKey: key.value, model, source: key.source }
}

/**
 * Read the key from the microservice's own Cumulocity settings
 * (`GET /application/currentApplication/settings`).
 *
 * This is how a deployed microservice receives secrets: the value is declared in
 * `manifest.settings` (see nitro.config.ts), stored as a tenant option under the
 * settings category, and decrypted by the platform for this service only — the
 * `credentials.` prefix keeps it out of the UI and the Options API for everyone
 * else. Unlike env vars it needs no redeploy to rotate.
 *
 * Returns undefined (never throws) when running outside Nitro, when the
 * platform call fails, or when nothing is configured, so the caller can fall
 * back to env vars.
 */
export async function anthropicConfigFromPlatform(): Promise<
  { apiKey: string; model?: string } | undefined
> {
  try {
    const { useDeployedTenantClient } = await import('c8y-nitro/utils')
    const client = await useDeployedTenantClient()
    const res = await client.core.fetch('/application/currentApplication/settings', {
      method: 'GET',
      headers: { Accept: 'application/json' },
    })
    if (!res.ok) {
      lastPlatformError = `GET /application/currentApplication/settings -> ${res.status}`
      return undefined
    }

    const settings = (await res.json()) as Record<string, unknown>
    const read = (key: string): string | undefined => {
      const value = settings?.[key]
      return typeof value === 'string' && value.trim() !== '' ? value.trim() : undefined
    }

    const apiKey = read('credentials.anthropicApiKey') ?? read('anthropicApiKey')
    // 'unset' is the manifest's mandatory placeholder default, not a real key.
    if (!apiKey || apiKey === PLACEHOLDER_KEY) {
      lastPlatformError = apiKey
        ? 'settings returned the placeholder value ("unset") — the key was never set on the platform'
        : `settings contained no anthropic key (keys seen: ${Object.keys(settings).join(', ') || 'none'})`
      return undefined
    }
    return { apiKey, model: read('anthropicModel') }
  } catch (error) {
    // Outside Nitro (tests/local) or the platform call failed — caller falls
    // back, but record why so the failure is diagnosable instead of silent.
    lastPlatformError = error instanceof Error ? error.message : String(error)
    return undefined
  }
}

/** Why the last platform lookup failed; surfaced in the thrown error. */
let lastPlatformError: string | undefined

/** Diagnostic detail from the most recent platform settings lookup. */
export function lastPlatformSettingsError(): string | undefined {
  return lastPlatformError
}

/**
 * Full resolution order for the deployed service: runtime config / env first
 * (cheap, no network), then the platform settings. Falls back to the platform
 * only when the synchronous sources yield nothing, so local runs stay offline.
 */
export async function resolveAnthropicConfigAsync(
  runtimeConfig?: Record<string, unknown>,
): Promise<AnthropicConfig> {
  try {
    return resolveAnthropicConfig(runtimeConfig)
  } catch (error) {
    const platform = await anthropicConfigFromPlatform()
    if (!platform) {
      const why = lastPlatformSettingsError()
      throw why
        ? new Error(`${(error as Error).message} [platform lookup: ${why}]`)
        : error
    }
    return {
      apiKey: platform.apiKey,
      model:
        platform.model ||
        (typeof runtimeConfig?.anthropicModel === 'string'
          ? runtimeConfig.anthropicModel
          : '') ||
        DEFAULT_MODEL,
      source: 'platform:currentApplication/settings',
    }
  }
}
