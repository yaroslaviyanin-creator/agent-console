import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import {
  bindTelegramViewport,
  type TelegramViewportBridge,
  type ViewportEnvironment,
} from '../src/platform/telegram'

type Listener = () => void

function createFakeEnv(initialHeight = 700) {
  const sdkListeners = new Map<string, Set<Listener>>()
  const docListeners = new Map<string, Set<Listener>>()
  const winListeners = new Map<string, Set<Listener>>()
  const vvListeners = new Map<string, Set<Listener>>()
  const cssProps = new Map<string, string>()

  let nextRafId = 1
  const rafQueue = new Map<number, () => void>()
  let nextTimerId = 1
  const timers = new Map<number, { cb: () => void; delayMs: number }>()
  let expandCalls = 0

  const add = (map: Map<string, Set<Listener>>, type: string, fn: Listener) => {
    let set = map.get(type)
    if (!set) {
      set = new Set()
      map.set(type, set)
    }
    set.add(fn)
  }

  const remove = (map: Map<string, Set<Listener>>, type: string, fn: Listener) => {
    map.get(type)?.delete(fn)
  }

  const emit = (map: Map<string, Set<Listener>>, type: string) => {
    for (const fn of [...(map.get(type) ?? [])]) fn()
  }

  const tg: TelegramViewportBridge = {
    viewportHeight: 640,
    expand() {
      expandCalls++
    },
    onEvent(type, fn) {
      add(sdkListeners, type, fn)
    },
    offEvent(type, fn) {
      remove(sdkListeners, type, fn)
    },
  }

  const env: ViewportEnvironment = {
    window: {
      innerHeight: initialHeight,
      addEventListener(type, fn) {
        add(winListeners, type, fn)
      },
      removeEventListener(type, fn) {
        remove(winListeners, type, fn)
      },
      visualViewport: {
        addEventListener(type, fn) {
          add(vvListeners, type, fn)
        },
        removeEventListener(type, fn) {
          remove(vvListeners, type, fn)
        },
      },
    },
    document: {
      visibilityState: 'visible',
      documentElement: {
        style: {
          setProperty(name, value) {
            cssProps.set(name, value)
          },
        },
      },
      addEventListener(type, fn) {
        add(docListeners, type, fn)
      },
      removeEventListener(type, fn) {
        remove(docListeners, type, fn)
      },
    },
    requestAnimationFrame(cb) {
      const id = nextRafId++
      rafQueue.set(id, cb)
      return id
    },
    cancelAnimationFrame(id) {
      rafQueue.delete(id)
    },
    setTimeout(cb, delayMs) {
      const id = nextTimerId++
      timers.set(id, { cb, delayMs })
      return id
    },
    clearTimeout(id) {
      timers.delete(Number(id))
    },
  }

  return {
    tg,
    env,
    cssProps,
    sdkListeners,
    docListeners,
    winListeners,
    vvListeners,
    rafQueue,
    timers,
    get expandCalls() {
      return expandCalls
    },
    emitSdk: (type: string) => emit(sdkListeners, type),
    emitDoc: (type: string) => emit(docListeners, type),
    emitWin: (type: string) => emit(winListeners, type),
    emitVisualViewport: (type: string) => emit(vvListeners, type),
    flushRaf() {
      for (const [id, cb] of [...rafQueue.entries()]) {
        rafQueue.delete(id)
        cb()
      }
    },
    flushTimerByDelay(delayMs: number) {
      for (const [id, entry] of [...timers.entries()]) {
        if (entry.delayMs === delayMs) {
          timers.delete(id)
          entry.cb()
        }
      }
    },
  }
}

test('tokens.css uses 100dvh fallback without --tg-viewport-stable-height', () => {
  const tokensCss = readFileSync(new URL('../src/styles/tokens.css', import.meta.url), 'utf8')
  assert.match(tokensCss, /--app-height:\s*100dvh;/)
  assert.ok(!tokensCss.includes('--tg-viewport-stable-height'))
})

test('bindTelegramViewport computes max height across SDK and DOM events', () => {
  const fake = createFakeEnv(700)
  fake.tg.viewportHeight = 640
  const cleanup = bindTelegramViewport(fake.tg, fake.env)

  assert.equal(fake.cssProps.get('--app-height'), '700px')

  fake.tg.viewportHeight = 760
  fake.emitSdk('viewportChanged')
  assert.equal(fake.cssProps.get('--app-height'), '760px')

  delete fake.tg.viewportHeight
  fake.env.window.innerHeight = 720
  fake.emitSdk('fullscreenChanged')
  assert.equal(fake.cssProps.get('--app-height'), '720px')

  fake.tg.viewportHeight = 780
  fake.emitSdk('safeAreaChanged')
  assert.equal(fake.cssProps.get('--app-height'), '780px')

  fake.env.window.innerHeight = 810
  fake.emitWin('resize')
  assert.equal(fake.cssProps.get('--app-height'), '810px')

  fake.tg.viewportHeight = 840
  fake.emitVisualViewport('resize')
  assert.equal(fake.cssProps.get('--app-height'), '840px')

  cleanup()
})

test('activated and visible visibilitychange expand and sync immediately, on RAF, 150ms, and 400ms', () => {
  const fake = createFakeEnv(600)
  fake.tg.viewportHeight = 600
  const cleanup = bindTelegramViewport(fake.tg, fake.env)

  fake.env.document.visibilityState = 'hidden'
  fake.emitDoc('visibilitychange')
  assert.equal(fake.expandCalls, 0)
  assert.equal(fake.rafQueue.size, 0)
  assert.equal(fake.timers.size, 0)

  fake.tg.viewportHeight = 650
  fake.emitSdk('activated')
  assert.equal(fake.expandCalls, 1)
  assert.equal(fake.cssProps.get('--app-height'), '650px')
  assert.equal(fake.rafQueue.size, 1)
  assert.deepEqual([...fake.timers.values()].map(t => t.delayMs).sort((a, b) => a - b), [150, 400])

  fake.tg.viewportHeight = 680
  fake.flushRaf()
  assert.equal(fake.cssProps.get('--app-height'), '680px')

  fake.tg.viewportHeight = 710
  fake.flushTimerByDelay(150)
  assert.equal(fake.cssProps.get('--app-height'), '710px')

  fake.tg.viewportHeight = 740
  fake.flushTimerByDelay(400)
  assert.equal(fake.cssProps.get('--app-height'), '740px')

  fake.env.document.visibilityState = 'visible'
  fake.tg.viewportHeight = 760
  fake.emitDoc('visibilitychange')
  assert.equal(fake.expandCalls, 2)
  assert.equal(fake.cssProps.get('--app-height'), '760px')
  assert.equal(fake.rafQueue.size, 1)
  assert.equal(fake.timers.size, 2)

  // Repeated trigger cancels previous pending RAF and timers before scheduling fresh ones.
  fake.emitSdk('activated')
  assert.equal(fake.expandCalls, 3)
  assert.equal(fake.rafQueue.size, 1)
  assert.equal(fake.timers.size, 2)

  cleanup()
  assert.equal(fake.rafQueue.size, 0)
  assert.equal(fake.timers.size, 0)
  for (const set of fake.sdkListeners.values()) assert.equal(set.size, 0)
  for (const set of fake.docListeners.values()) assert.equal(set.size, 0)
  for (const set of fake.winListeners.values()) assert.equal(set.size, 0)
  for (const set of fake.vvListeners.values()) assert.equal(set.size, 0)
})
