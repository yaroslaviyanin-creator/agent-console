import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { AmbientBackground } from '../src/app/AmbientBackground'
import { GRADIENT_PALETTES } from '../src/app/appearanceStore'
import { collectChatAttachments, extractChatLinks, summarizeChatContext } from '../src/app/ChatContextPanel'
import { clampSidebarWidth, DEFAULT_SIDEBAR_WIDTH, MAX_SIDEBAR_WIDTH, MIN_SIDEBAR_WIDTH, readSidebarWidth, saveSidebarWidth } from '../src/app/sidebarWidth'
import type { Turn } from '../src/app/chatStore'

test('AmbientBackground sets data-state=idle when inactive and data-state=active when busy', () => {
  const idleHtml = renderToStaticMarkup(createElement(AmbientBackground, { dimmed: true, active: false }))
  assert.match(idleHtml, /data-state="idle"/)
  assert.match(idleHtml, /data-mode="workspace"/)

  const activeHtml = renderToStaticMarkup(createElement(AmbientBackground, { dimmed: true, active: true }))
  assert.match(activeHtml, /data-state="active"/)
})

test('sidebarWidth clamps to [200, 420] and handles throwing localStorage safely', () => {
  assert.equal(clampSidebarWidth(120), MIN_SIDEBAR_WIDTH)
  assert.equal(clampSidebarWidth(600), MAX_SIDEBAR_WIDTH)
  assert.equal(clampSidebarWidth(312.4), 312)
  assert.equal(clampSidebarWidth(Number.NaN), DEFAULT_SIDEBAR_WIDTH)

  const prevWindow = globalThis.window
  try {
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: {
        localStorage: {
          getItem() { throw new Error('SecurityError') },
          setItem() { throw new Error('SecurityError') },
        },
      },
    })
    assert.equal(readSidebarWidth(), DEFAULT_SIDEBAR_WIDTH)
    assert.doesNotThrow(() => saveSidebarWidth(300))
  } finally {
    Object.defineProperty(globalThis, 'window', { configurable: true, value: prevWindow })
  }
})

test('ChatContextPanel helpers collect unique attachments, extract http(s) links, and summarize chat', () => {
  const turns: Turn[] = [
    {
      id: 't1',
      parentId: null,
      time: 1000,
      model: 'claude',
      seq: 1,
      state: 'completed',
      target: { mode: 'chat' },
      prompt: 'Смотри https://example.com/docs?a=1, и еще (https://openai.com/research).',
      attachments: [
        { id: 'a1', kind: 'pdf', name: 'lecture.pdf', sizeBytes: 1024 },
      ],
      parts: [
        { kind: 'text', text: 'Повтор https://example.com/docs?a=1 и ftp://ignored.test/file' },
      ],
    },
    {
      id: 't2',
      parentId: 't1',
      time: 2000,
      model: 'claude',
      seq: 1,
      state: 'completed',
      target: { mode: 'chat' },
      prompt: '',
      attachments: [
        { id: 'a1', kind: 'pdf', name: 'lecture.pdf', sizeBytes: 1024 },
        { id: 'a2', kind: 'image', name: 'chart.png', sizeBytes: 2048 },
      ],
      parts: [{ kind: 'text', text: 'Готово: https://github.com/org/repo.' }],
    },
  ]

  const atts = collectChatAttachments(turns)
  assert.equal(atts.length, 2)
  assert.deepEqual(atts.map(a => a.id), ['a1', 'a2'])

  const links = extractChatLinks(turns)
  assert.deepEqual(links, [
    'https://example.com/docs?a=1',
    'https://openai.com/research',
    'https://github.com/org/repo',
  ])

  const summary = summarizeChatContext(turns, 'Claude', 'Sonnet')
  assert.equal(summary.providerLabel, 'Claude')
  assert.equal(summary.modelLabel, 'Sonnet')
  assert.equal(summary.turnsCount, 2)
  assert.equal(summary.messagesCount, 4)
})

test('App renders user attachments above .user-bubble and skips empty .user-bubble', () => {
  const appSource = readFileSync(new URL('../src/app/App.tsx', import.meta.url), 'utf8')
  const attIdx = appSource.indexOf('<div className="user-turn-attachments">')
  const bubbleIdx = appSource.indexOf('<div className="user-bubble">')
  assert.ok(attIdx > 0 && bubbleIdx > 0 && attIdx < bubbleIdx, 'attachments must render above .user-bubble')
  assert.match(appSource, /\{hasPrompt && \(\s*<div className="user-bubble">/)
  assert.ok(!appSource.includes('Рабочее пространство'), 'breadcrumb should be removed from header')
  assert.ok(GRADIENT_PALETTES.length >= 5)
  assert.match(appSource, /onPointerDown=\{\(\) => setOpenMenu\(null\)\}/)
  assert.match(appSource, /onFocus=\{\(\) => setOpenMenu\(null\)\}/)
})

test('appearanceStore syncs server uiSettings without feedback loop', async () => {
  const { applyServerUISettings, setAppearanceSyncHandler, useAppearance } = await import('../src/app/appearanceStore')
  let synced: unknown = null
  setAppearanceSyncHandler(ui => {
    synced = ui
  })
  applyServerUISettings({ theme: 'light', accent: 'ocean', accentCustomColor: '#123456' })
  assert.equal(useAppearance.getState().theme, 'light')
  assert.equal(useAppearance.getState().accent, 'ocean')
  assert.equal(synced, null)
  setAppearanceSyncHandler(null)
})
