// The AI Agent Manager connects over the legacy HTTP+SSE transport, so these
// tests pin the wire protocol it depends on: the endpoint handshake frame, the
// session id round trip, and messages arriving as `event: message` frames.

import { describe, it, expect } from 'vitest'
import { WebStandardSSEServerTransport } from '../src/mcp/sse-transport'
import {
  addSession,
  getSession,
  removeSession,
  sessionCount,
} from '../src/mcp/sse-sessions'

/** Read whatever frames are currently buffered on the stream. */
async function drain(
  stream: ReadableStream<Uint8Array>,
  expectedFrames: number,
): Promise<string[]> {
  const reader = stream.getReader()
  const decoder = new TextDecoder()
  const frames: string[] = []
  let buffer = ''
  while (frames.length < expectedFrames) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    let idx: number
    while ((idx = buffer.indexOf('\n\n')) !== -1) {
      frames.push(buffer.slice(0, idx))
      buffer = buffer.slice(idx + 2)
    }
  }
  reader.releaseLock()
  return frames
}

describe('WebStandardSSEServerTransport', () => {
  it('advertises the POST endpoint with the session id in its first frame', async () => {
    const transport = new WebStandardSSEServerTransport('/service/x/sse', 'sess123')
    await transport.start()
    const [frame] = await drain(transport.stream, 1)
    expect(frame).toBe('event: endpoint\ndata: /service/x/sse?sessionid=sess123')
    await transport.close()
  })

  it('pushes server messages as SSE message frames', async () => {
    const transport = new WebStandardSSEServerTransport('/sse', 'abc')
    await transport.start()
    await transport.send({ jsonrpc: '2.0', id: 1, result: { ok: true } })
    const frames = await drain(transport.stream, 2)
    expect(frames[1]).toContain('event: message')
    expect(JSON.parse(frames[1].split('data: ')[1])).toEqual({
      jsonrpc: '2.0',
      id: 1,
      result: { ok: true },
    })
    await transport.close()
  })

  it('forwards a posted JSON-RPC message to onmessage', async () => {
    const transport = new WebStandardSSEServerTransport('/sse', 'abc')
    const received: unknown[] = []
    transport.onmessage = (m) => received.push(m)
    const error = await transport.handlePostMessage({
      jsonrpc: '2.0',
      id: 7,
      method: 'tools/list',
    })
    expect(error).toBeUndefined()
    expect(received).toHaveLength(1)
    await transport.close()
  })

  it('reports invalid JSON rather than throwing', async () => {
    const transport = new WebStandardSSEServerTransport('/sse', 'abc')
    const error = await transport.handlePostMessage('{not json')
    expect(error).toMatch(/invalid JSON/)
    await transport.close()
  })

  it('invokes onclose exactly once', async () => {
    const transport = new WebStandardSSEServerTransport('/sse', 'abc')
    let closes = 0
    transport.onclose = () => closes++
    await transport.close()
    await transport.close()
    expect(closes).toBe(1)
  })
})

describe('sse session registry', () => {
  it('stores and removes sessions by id', async () => {
    const before = sessionCount()
    const transport = new WebStandardSSEServerTransport('/sse', 'registry-test')
    addSession(transport)
    expect(sessionCount()).toBe(before + 1)
    expect(getSession('registry-test')).toBe(transport)

    removeSession('registry-test')
    expect(getSession('registry-test')).toBeUndefined()
    expect(sessionCount()).toBe(before)
    await transport.close()
  })
})
