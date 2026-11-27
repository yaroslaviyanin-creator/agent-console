import assert from 'node:assert/strict'
import test from 'node:test'
import { isStorageBlocked, readConnection, saveConnection } from '../src/app/runtime'

test('runtime falls back to in-memory connection when localStorage throws SecurityError', () => {
  const originalWindow = (globalThis as { window?: unknown }).window
  try {
    const blockedWindow = {
      get localStorage() {
        throw new DOMException('Blocked by Brave Shields', 'SecurityError')
      },
    }
    ;(globalThis as { window?: unknown }).window = blockedWindow

    assert.equal(isStorageBlocked(), true)
    assert.deepEqual(readConnection(), {
      mode: 'demo',
      address: 'https://macbook-pro.tail2aa739.ts.net',
    })

    saveConnection({
      mode: 'server',
      address: 'https://example.ts.net',
      token: 'secret-token',
    })

    assert.deepEqual(readConnection(), {
      mode: 'server',
      address: 'https://example.ts.net',
      token: 'secret-token',
    })
  } finally {
    if (originalWindow === undefined) {
      delete (globalThis as { window?: unknown }).window
    } else {
      ;(globalThis as { window?: unknown }).window = originalWindow
    }
  }
})
