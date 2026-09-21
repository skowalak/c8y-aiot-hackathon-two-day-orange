// Loads the Markdown knowledge file for a device type, falling back to defaults.
// Files are bundled with the microservice (see src/knowledge/*.md).

import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))

export interface Expectations {
  deviceType: string
  source: string // which file was used
  markdown: string
}

async function tryRead(fileName: string): Promise<string | undefined> {
  try {
    return await readFile(join(here, fileName), 'utf8')
  } catch {
    return undefined
  }
}

/**
 * Resolve the expectation spec for a device type. Sanitizes the type into a
 * file name (`c8y_Water_Pump` -> `c8y_Water_Pump.md`) and falls back to
 * `_defaults.md` when no specific file exists.
 */
export async function getExpectations(deviceType?: string): Promise<Expectations> {
  if (deviceType) {
    const safe = deviceType.replace(/[^A-Za-z0-9_]/g, '_')
    const md = await tryRead(`${safe}.md`)
    if (md) return { deviceType, source: `${safe}.md`, markdown: md }
  }
  const fallback = (await tryRead('_defaults.md')) ?? '# No knowledge base available'
  return {
    deviceType: deviceType ?? 'unknown',
    source: '_defaults.md',
    markdown: fallback,
  }
}
