import type { TransportAdapter } from '../protocol/types'
import { MockTransport } from '../transport/MockTransport'

export type ConnectionMode = 'demo' | 'server'
export const defaultServerAddress = 'https://macbook-pro.tail2aa739.ts.net'
const storageKey = 'agent-console.connection'
interface Connection {
  mode: ConnectionMode
  address: string
  token?: string
}
const demoTransport = new MockTransport({ nearLimit: true })
export let transport: TransportAdapter = demoTransport

let memoryConnection: Connection | null = null
let storageBlocked = false

export function setTransport(adapter: TransportAdapter = demoTransport) {
  transport = adapter
}

export function isStorageBlocked(): boolean {
  if (storageBlocked) return true
  if (typeof window === 'undefined') return false
  try {
    void window.localStorage.getItem(storageKey)
    return false
  } catch {
    storageBlocked = true
    return true
  }
}

export function readConnection(): Connection {
  try {
    if (typeof window === 'undefined') return memoryConnection ?? { mode: 'demo', address: defaultServerAddress }
    const saved = JSON.parse(window.localStorage.getItem(storageKey) ?? 'null')
    storageBlocked = false
    if (saved && typeof saved.address === 'string' && (saved.mode === 'demo' || saved.mode === 'server')) {
      const connection: Connection = {
        mode: saved.mode,
        address: saved.address,
        token: typeof saved.token === 'string' ? saved.token : undefined,
      }
      memoryConnection = connection
      return connection
    }
  } catch {
    storageBlocked = true
  }
  return memoryConnection ?? { mode: 'demo', address: defaultServerAddress }
}

export function saveConnection(connection: Connection) {
  memoryConnection = { ...connection }
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(storageKey, JSON.stringify(connection))
    storageBlocked = false
  } catch {
    storageBlocked = true
  }
}
