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

/**
 * Production fetcher: authenticated Cumulocity client from the request event.
 *
 * `useUserClient` is imported dynamically so that the (runtime-only)
 * `c8y-nitro/utils` module chain is loaded lazily — importing this file in a
 * plain Node/test process (which never calls fetcherFromEvent) does not pull it
 * in. The returned fetcher resolves the client on first use and caches it.
 */
export function fetcherFromEvent(event: H3Event): C8yFetcher {
  let clientPromise: Promise<any> | undefined
  return async (path: string) => {
    if (!clientPromise) {
      clientPromise = import('c8y-nitro/utils').then(({ useUserClient }) =>
        useUserClient(event),
      )
    }
    const client = await clientPromise
    const res = await client.core.fetch(path)
    return res.json()
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
