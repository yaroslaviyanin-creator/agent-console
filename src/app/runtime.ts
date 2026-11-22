import type { TransportAdapter } from '../protocol/types'
import { MockTransport } from '../transport/MockTransport'

export type ConnectionMode = 'demo' | 'server'
export const defaultServerAddress = 'http://127.0.0.1:8080'
const storageKey = 'agent-console.connection'
interface Connection {
  mode: ConnectionMode
  address: string
  token?: string
}
const demoTransport = new MockTransport({ nearLimit: true })
export let transport: TransportAdapter = demoTransport

export function setTransport(adapter: TransportAdapter = demoTransport) {
  transport = adapter
}

export function readConnection(): Connection {
  try {
    if (typeof window === 'undefined') return { mode: 'demo', address: defaultServerAddress }
    const saved = JSON.parse(localStorage.getItem(storageKey) ?? 'null')
    if (saved && typeof saved.address === 'string' && (saved.mode === 'demo' || saved.mode === 'server')) {
      return { mode: saved.mode, address: saved.address, token: typeof saved.token === 'string' ? saved.token : undefined }
    }
  } catch { /* Storage may be unavailable in a web view. */ }
  return { mode: 'demo', address: defaultServerAddress }
}

export function saveConnection(connection: Connection) {
  try { localStorage.setItem(storageKey, JSON.stringify(connection)) }
  catch { /* The current session remains usable without storage. */ }
}
