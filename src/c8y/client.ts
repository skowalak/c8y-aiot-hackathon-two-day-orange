// Thin read-only wrapper over the Cumulocity REST API for the three device-data
// domains we diagnose.
//
// The data layer is expressed against a C8yFetcher — a `(path) => Promise<json>`
// function — so it can be dependency-injected in tests. In production the fetcher
// is derived from the H3 request event via c8y-nitro's useUserClient, so calls
// run as the caller and authorization is deferred to Cumulocity core.

import type { H3Event } from 'nitro/h3'
import type {
  AlarmRecord,
  EventRecord,
  MeasurementRecord,
  Pass,
} from '../store/types'

/** Minimal seam over Cumulocity GETs: given a REST path, return parsed JSON. */
export type C8yFetcher = (path: string) => Promise<any>

/** Fetches raw text (not JSON) from a Cumulocity path — for binary/file content. */
export type C8yTextFetcher = (path: string) => Promise<string | undefined>

/**
 * Production fetcher: authenticated Cumulocity client from the request event.
 *
 * `useUserClient` is imported dynamically so that the (runtime-only)
 * `c8y-nitro/utils` module chain is loaded lazily — importing this file in a
 * plain Node/test process (which never calls fetcherFromEvent) does not pull it
 * in. The returned fetcher resolves the client on first use and caches it.
 */
export function fetcherFromEvent(event: H3Event): C8yFetcher {
  const raw = rawFetcherFromEvent(event)
  return async (path: string) => {
    const res = await raw(path)
    return res.json()
  }
}

// Shared lazy client resolver returning the raw c8y fetch (IFetchResponse).
function rawFetcherFromEvent(event: H3Event) {
  let clientPromise: Promise<any> | undefined
  return async (path: string) => {
    if (!clientPromise) {
      clientPromise = import('c8y-nitro/utils').then(({ useUserClient }) =>
        useUserClient(event),
      )
    }
    const client = await clientPromise
    return client.core.fetch(path)
  }
}

/**
 * Text fetcher for reading file/binary content (e.g. a knowledge Markdown file
 * from the Cumulocity file repository / Dateiablage). Returns undefined on a
 * non-OK response so callers can fall back to bundled files.
 */
export function textFetcherFromEvent(event: H3Event): C8yTextFetcher {
  const raw = rawFetcherFromEvent(event)
  return async (path: string) => {
    try {
      const res = await raw(path)
      if (res?.ok === false || (typeof res?.status === 'number' && res.status >= 400)) {
        return undefined
      }
      return await res.text()
    } catch {
      return undefined
    }
  }
}

interface Window {
  from: string // ISO
  to: string // ISO
}

// Cumulocity returns measurements with arbitrary fragment/series shapes:
//   { "c8y_Temperature": { "T": { "value": 21.3, "unit": "C" } } }
// Flatten every numeric series into one MeasurementRecord.
function flattenMeasurement(mo: any, pass: Pass): MeasurementRecord[] {
  const out: MeasurementRecord[] = []
  for (const [fragment, series] of Object.entries(mo)) {
    if (fragment === 'id' || fragment === 'self' || fragment === 'time') continue
    if (fragment === 'type' || fragment === 'source') continue
    if (series == null || typeof series !== 'object') continue
    for (const [seriesName, point] of Object.entries(series as object)) {
      const p = point as { value?: unknown; unit?: unknown }
      if (typeof p?.value !== 'number') continue
      out.push({
        pass,
        time: mo.time,
        type: mo.type ?? fragment,
        series: `${fragment}.${seriesName}`,
        unit: typeof p.unit === 'string' ? p.unit : undefined,
        value: p.value,
      })
    }
  }
  return out
}

export async function fetchMeasurements(
  fetch: C8yFetcher,
  deviceId: string,
  w: Window,
  pass: Pass,
  pageSize = 2000,
): Promise<MeasurementRecord[]> {
  const path =
    `/measurement/measurements?source=${encodeURIComponent(deviceId)}` +
    `&dateFrom=${encodeURIComponent(w.from)}&dateTo=${encodeURIComponent(w.to)}` +
    `&pageSize=${pageSize}&revert=false`
  const body = await fetch(path)
  const items: any[] = body.measurements ?? []
  return items.flatMap((m) => flattenMeasurement(m, pass))
}

export async function fetchAlarms(
  fetch: C8yFetcher,
  deviceId: string,
  w: Window,
  pass: Pass,
  pageSize = 2000,
): Promise<AlarmRecord[]> {
  const path =
    `/alarm/alarms?source=${encodeURIComponent(deviceId)}` +
    `&dateFrom=${encodeURIComponent(w.from)}&dateTo=${encodeURIComponent(w.to)}` +
    `&pageSize=${pageSize}`
  const body = await fetch(path)
  const items: any[] = body.alarms ?? []
  return items.map((a) => ({
    pass,
    time: a.time,
    type: a.type,
    severity: a.severity,
    status: a.status,
    text: a.text,
    count: a.count ?? 1,
  }))
}

export async function fetchEvents(
  fetch: C8yFetcher,
  deviceId: string,
  w: Window,
  pass: Pass,
  pageSize = 2000,
): Promise<EventRecord[]> {
  const path =
    `/event/events?source=${encodeURIComponent(deviceId)}` +
    `&dateFrom=${encodeURIComponent(w.from)}&dateTo=${encodeURIComponent(w.to)}` +
    `&pageSize=${pageSize}`
  const body = await fetch(path)
  const items: any[] = body.events ?? []
  return items.map((e) => ({
    pass,
    time: e.time,
    type: e.type,
    text: e.text,
  }))
}

/**
 * Read a knowledge Markdown file from the Cumulocity file repository (Inventory
 * Binaries / "Dateiablage") by its file name (e.g. "sim_PowerNode.md").
 *
 * Inventory Binaries are managed objects with a `c8y_IsBinary` fragment and a
 * `name`; their bytes are downloaded from /inventory/binaries/<id>. We locate the
 * binary by name via the inventory query API, then download it as text. Returns
 * undefined if not found so the caller can fall back to bundled files.
 */
export async function fetchKnowledgeBinary(
  json: C8yFetcher,
  text: C8yTextFetcher,
  fileName: string,
): Promise<string | undefined> {
  const query = encodeURIComponent(
    `has(c8y_IsBinary) and name eq '${fileName}'`,
  )
  let listing: any
  try {
    listing = await json(
      `/inventory/managedObjects?query=${query}&pageSize=1`,
    )
  } catch {
    return undefined
  }
  const id = listing?.managedObjects?.[0]?.id
  if (!id) return undefined
  return text(`/inventory/binaries/${encodeURIComponent(String(id))}`)
}

/** Resolve a device's `type` from the inventory (for knowledge-base lookup). */
export async function fetchDeviceType(
  fetch: C8yFetcher,
  deviceId: string,
): Promise<string | undefined> {
  const body = await fetch(
    `/inventory/managedObjects/${encodeURIComponent(deviceId)}`,
  )
  return body?.type
}

/**
 * Resolve a Cumulocity managed-object id from an external id (e.g. a serial like
 * "sim-node-01" bound under the "c8y_Serial" external-id type). Returns
 * undefined if the external id is not found.
 */
export async function resolveDeviceIdByExternalId(
  fetch: C8yFetcher,
  externalId: string,
  externalIdType = 'c8y_Serial',
): Promise<string | undefined> {
  const path =
    `/identity/externalIds/${encodeURIComponent(externalIdType)}/` +
    encodeURIComponent(externalId)
  try {
    const body = await fetch(path)
    return body?.managedObject?.id
  } catch {
    return undefined
  }
}

/**
 * List device ids of a given type from the inventory (for fleet checks). Returns
 * up to `pageSize` managed-object ids.
 */
export async function listDeviceIdsByType(
  fetch: C8yFetcher,
  type: string,
  pageSize = 100,
): Promise<string[]> {
  const path =
    `/inventory/managedObjects?query=${encodeURIComponent(`type eq '${type}'`)}` +
    `&pageSize=${pageSize}`
  const body = await fetch(path)
  const items: any[] = body.managedObjects ?? []
  return items.map((m) => String(m.id))
}
