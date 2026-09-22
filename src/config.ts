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
