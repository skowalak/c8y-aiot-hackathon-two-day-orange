// Loads the Markdown knowledge file for a device type.
//
// Resolution order (see TICKET.md follow-up — knowledge lives in the platform):
//   1. Cumulocity file repository / Dateiablage (Inventory Binaries), by name
//      "<deviceType>.md" — so operators can edit expectations without redeploying.
//   2. Bundled fallback file shipped in src/knowledge/*.md.
//   3. Bundled _defaults.md.
//
// The Cumulocity lookup is only attempted when c8y fetchers are supplied; in
// tests (and offline) the bundled files are used.

import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import {
  fetchKnowledgeBinary,
  type C8yFetcher,
  type C8yTextFetcher,
} from '../c8y/client'

const here = dirname(fileURLToPath(import.meta.url))

export type KnowledgeSourceKind = 'dateiablage' | 'bundled' | 'defaults' | 'none'

export interface Expectations {
  deviceType: string
  source: string // which file/name was used
  sourceKind: KnowledgeSourceKind
  markdown: string
}

export interface KnowledgeFetchers {
  json: C8yFetcher
  text: C8yTextFetcher
}

async function tryReadBundled(fileName: string): Promise<string | undefined> {
  try {
    return await readFile(join(here, fileName), 'utf8')
  } catch {
    return undefined
  }
}

/**
 * Resolve the expectation spec for a device type. Sanitizes the type into a file
 * name (`sim_PowerNode` -> `sim_PowerNode.md`), tries the Cumulocity file
 * repository first (if fetchers are given), then the bundled file, then defaults.
 */
export async function getExpectations(
  deviceType?: string,
  fetchers?: KnowledgeFetchers,
): Promise<Expectations> {
  if (deviceType) {
    const safe = deviceType.replace(/[^A-Za-z0-9_]/g, '_')
    const fileName = `${safe}.md`

    // 1) Cumulocity file repository (Dateiablage).
    if (fetchers) {
      const fromPlatform = await fetchKnowledgeBinary(
        fetchers.json,
        fetchers.text,
        fileName,
      )
      if (fromPlatform && fromPlatform.trim().length > 0) {
        return {
          deviceType,
          source: fileName,
          sourceKind: 'dateiablage',
          markdown: fromPlatform,
        }
      }
    }

    // 2) Bundled fallback.
    const bundled = await tryReadBundled(fileName)
    if (bundled) {
      return { deviceType, source: fileName, sourceKind: 'bundled', markdown: bundled }
    }
  }

  // 3) Defaults.
  const fallback = (await tryReadBundled('_defaults.md')) ?? '# No knowledge base available'
  return {
    deviceType: deviceType ?? 'unknown',
    source: '_defaults.md',
    sourceKind: deviceType ? 'defaults' : 'none',
    markdown: fallback,
  }
}
