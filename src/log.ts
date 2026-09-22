// Shared structured logger for the microservice, outside request context
// (plugins, MCP tool handlers, the agent loop). Wraps c8y-nitro's createLogger
// (evlog) so output matches the platform's JSON log format. Within an HTTP
// handler prefer useLogger(event) for per-request correlation.
//
// c8y-nitro/utils is imported LAZILY: its module chain (c8y-nitro/runtime) is a
// virtual only present in the Nitro build, so a static import breaks plain
// Node/test processes. getLogger returns a thin proxy that resolves the real
// logger on first use and no-ops gracefully if it cannot be loaded (tests).

const SERVICE = 'c8y-diagnostic-agent'

export interface Logger {
  info(message: string, context?: Record<string, unknown>): void
  warn(message: string, context?: Record<string, unknown>): void
  error(message: string, context?: Record<string, unknown>): void
  debug(message: string, context?: Record<string, unknown>): void
}

type Level = keyof Logger

function makeLazyLogger(initial: Record<string, unknown>): Logger {
  let realPromise: Promise<any> | undefined

  const ensure = () => {
    if (!realPromise) {
      realPromise = import('c8y-nitro/utils')
        .then(({ createLogger }) => createLogger(initial))
        .catch(() => undefined) // running outside Nitro (tests) — degrade to console
    }
    return realPromise
  }

  const emit = (level: Level, message: string, context?: Record<string, unknown>) => {
    void ensure().then((real) => {
      if (real && typeof real[level] === 'function') {
        real[level](message, context)
      } else {
        // Fallback so logging never throws when the platform logger is absent.
        const line = { level, message, ...initial, ...context }
        // eslint-disable-next-line no-console
        console[level === 'debug' ? 'log' : level](JSON.stringify(line))
      }
    })
  }

  return {
    info: (m, c) => emit('info', m, c),
    warn: (m, c) => emit('warn', m, c),
    error: (m, c) => emit('error', m, c),
    debug: (m, c) => emit('debug', m, c),
  }
}

export function getLogger(component: string): Logger {
  return makeLazyLogger({ service: SERVICE, component })
}
