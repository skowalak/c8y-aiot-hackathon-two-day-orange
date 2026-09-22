// Web-standard SSE transport for MCP (the pre-Streamable-HTTP "HTTP+SSE" protocol).
//
// WHY THIS EXISTS: the SDK ships `SSEServerTransport`, but it is Node-only — it
// requires a `node:http` ServerResponse. Our h3 v2 runtime is web-standard
// (srvx): handlers receive a `Request` and return a `Response`. So we implement
// the same wire protocol directly against the SDK's `Transport` interface.
//
// The Cumulocity AI Agent Manager connects over this older transport, so the
// service must speak it in addition to Streamable HTTP on /mcp.
//
// PROTOCOL (two endpoints, unlike stateless Streamable HTTP):
//   1. GET  /sse                  -> opens the event stream. The first frame is
//                                    `event: endpoint` whose data is the URL the
//                                    client must POST to, carrying a session id.
//   2. POST /sse?sessionid=<id>   -> client->server JSON-RPC. The response body
//                                    is empty (202); the actual reply is pushed
//                                    back over that session's event stream.
//
// This makes the transport stateful: a POST must find the stream opened by an
// earlier GET, so live sessions live in a module-level map (see ./sse-sessions).

import type { Transport } from '@modelcontextprotocol/sdk/shared/transport.js'
import type { JSONRPCMessage } from '@modelcontextprotocol/sdk/types.js'

const encoder = new TextEncoder()

/**
 * One SSE session: an open event stream plus the JSON-RPC plumbing that writes
 * to it. `stream` is handed straight back to h3 as the response body.
 */
export class WebStandardSSEServerTransport implements Transport {
  readonly sessionId: string
  readonly stream: ReadableStream<Uint8Array>

  onclose?: () => void
  onerror?: (error: Error) => void
  onmessage?: (message: JSONRPCMessage) => void

  private controller?: ReadableStreamDefaultController<Uint8Array>
  private closed = false
  private keepAlive?: ReturnType<typeof setInterval>

  /**
   * @param endpoint  Path the client should POST messages to (session id appended).
   * @param sessionId Identifier tying later POSTs back to this stream.
   */
  constructor(
    private readonly endpoint: string,
    sessionId: string,
  ) {
    this.sessionId = sessionId
    this.stream = new ReadableStream<Uint8Array>({
      start: (controller) => {
        this.controller = controller
      },
      // Fired when the client disconnects (browser closed, agent gave up).
      cancel: () => {
        void this.close()
      },
    })
  }

  /**
   * Emit the mandatory `endpoint` frame. The SDK calls this via server.connect();
   * the client blocks until it arrives, so nothing may be sent before it.
   */
  async start(): Promise<void> {
    if (this.closed) return
    const url = `${this.endpoint}?sessionid=${this.sessionId}`
    this.write(`event: endpoint\ndata: ${url}\n\n`)

    // Cumulocity sits behind a proxy that drops idle connections; a periodic
    // comment frame keeps the stream open without disturbing the protocol.
    this.keepAlive = setInterval(() => this.write(': keepalive\n\n'), 25_000)
    // Don't hold the event loop open on shutdown.
    this.keepAlive.unref?.()
  }

  /** Push a server->client JSON-RPC message over the event stream. */
  async send(message: JSONRPCMessage): Promise<void> {
    if (this.closed) return
    this.write(`event: message\ndata: ${JSON.stringify(message)}\n\n`)
  }

  /**
   * Feed a POSTed body into the MCP server. Returns an error string when the
   * payload is not valid JSON-RPC, so the route can answer 400.
   */
  async handlePostMessage(body: unknown): Promise<string | undefined> {
    if (this.closed) return 'session is closed'
    let parsed: JSONRPCMessage
    try {
      parsed = (typeof body === 'string' ? JSON.parse(body) : body) as JSONRPCMessage
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      this.onerror?.(new Error(`invalid JSON: ${message}`))
      return `invalid JSON: ${message}`
    }
    this.onmessage?.(parsed)
    return undefined
  }

  async close(): Promise<void> {
    if (this.closed) return
    this.closed = true
    if (this.keepAlive) clearInterval(this.keepAlive)
    try {
      this.controller?.close()
    } catch {
      // Already closed by the runtime (client vanished) — nothing to do.
    }
    this.onclose?.()
  }

  private write(chunk: string): void {
    if (this.closed) return
    try {
      this.controller?.enqueue(encoder.encode(chunk))
    } catch (error) {
      // The stream died under us (client disconnected mid-write).
      this.onerror?.(error instanceof Error ? error : new Error(String(error)))
      void this.close()
    }
  }
}
