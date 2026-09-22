// Pins the credential resolution that caused "ANTHROPIC_API_KEY is not
// configured" in the deployed microservice: runtimeConfig carried the empty
// build-time default while the platform setting used the plain env var name
// that Nitro does not read.

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { resolveAnthropicConfig } from '../src/config'

const VARS = [
  'ANTHROPIC_API_KEY',
  'NITRO_ANTHROPIC_API_KEY',
  '_ANTHROPIC_API_KEY',
  'C8Y_ANTHROPIC_API_KEY',
  'ANTHROPIC_MODEL',
  'NITRO_ANTHROPIC_MODEL',
  '_ANTHROPIC_MODEL',
  'C8Y_ANTHROPIC_MODEL',
]

let saved: Record<string, string | undefined>

beforeEach(() => {
  saved = Object.fromEntries(VARS.map((v) => [v, process.env[v]]))
  for (const v of VARS) delete process.env[v]
})

afterEach(() => {
  for (const [k, v] of Object.entries(saved)) {
    if (v === undefined) delete process.env[k]
    else process.env[k] = v
  }
})

describe('resolveAnthropicConfig', () => {
  it('prefers runtimeConfig when Nitro populated it', () => {
    process.env.ANTHROPIC_API_KEY = 'from-env'
    const cfg = resolveAnthropicConfig({
      anthropicApiKey: 'from-runtime',
      anthropicModel: 'claude-x',
    })
    expect(cfg.apiKey).toBe('from-runtime')
    expect(cfg.source).toBe('runtimeConfig')
  })

  // The regression: '' baked in at build time + key set under the plain name.
  it('falls back to plain ANTHROPIC_API_KEY when runtimeConfig is empty', () => {
    process.env.ANTHROPIC_API_KEY = 'platform-setting'
    const cfg = resolveAnthropicConfig({ anthropicApiKey: '', anthropicModel: '' })
    expect(cfg.apiKey).toBe('platform-setting')
    expect(cfg.source).toBe('env:ANTHROPIC_API_KEY')
  })

  it("accepts Nitro's own prefixed name", () => {
    process.env.NITRO_ANTHROPIC_API_KEY = 'prefixed'
    const cfg = resolveAnthropicConfig({ anthropicApiKey: '' })
    expect(cfg.apiKey).toBe('prefixed')
    expect(cfg.source).toBe('env:NITRO_ANTHROPIC_API_KEY')
  })

  it('treats a whitespace-only key as missing', () => {
    expect(() => resolveAnthropicConfig({ anthropicApiKey: '   ' })).toThrow(
      /not configured/,
    )
  })

  it('throws an actionable error when no key exists anywhere', () => {
    expect(() => resolveAnthropicConfig({})).toThrow(/microservice setting/)
  })

  it('defaults the model but lets env override it', () => {
    process.env.ANTHROPIC_API_KEY = 'k'
    expect(resolveAnthropicConfig({}).model).toBe('claude-opus-4-8')
    process.env.ANTHROPIC_MODEL = 'claude-custom'
    expect(resolveAnthropicConfig({}).model).toBe('claude-custom')
  })

  it('never leaks the key through `source`', () => {
    process.env.ANTHROPIC_API_KEY = 'sk-ant-secret-value'
    const cfg = resolveAnthropicConfig({})
    expect(cfg.source).not.toContain('secret')
  })
})
