import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { AmbientBackground } from '../src/app/AmbientBackground'
import { applyAppearanceToDom, resolveTheme, useAppearance } from '../src/app/appearanceStore'
import { hexToHsv, hsvToHex } from '../src/app/ColorWheelPicker'
import { ModelPopover } from '../src/app/ComposerPopovers'
import { clampContextWidth, DEFAULT_CONTEXT_WIDTH, MAX_CONTEXT_WIDTH, MIN_CONTEXT_WIDTH, readContextWidth, saveContextWidth } from '../src/app/contextWidth'
import { AssistantMessageActions, markdownToPlainText } from '../src/app/MessageActions'
import { clampMenuPosition } from '../src/app/Sidebar'
import { exportChatMarkdown, resolveAvailableProviders, type Chat, type Turn } from '../src/app/chatStore'

test('contextWidth clamps to [240, 460] and handles throwing localStorage safely', () => {
  assert.equal(clampContextWidth(100), MIN_CONTEXT_WIDTH)
  assert.equal(clampContextWidth(700), MAX_CONTEXT_WIDTH)
  assert.equal(clampContextWidth(342.6), 343)
  assert.equal(clampContextWidth(Number.NaN), DEFAULT_CONTEXT_WIDTH)

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
    assert.equal(readContextWidth(), DEFAULT_CONTEXT_WIDTH)
    assert.doesNotThrow(() => saveContextWidth(330))
  } finally {
    Object.defineProperty(globalThis, 'window', { configurable: true, value: prevWindow })
  }
})

test('Light theme removes ambient background gradient completely while dark theme renders it', () => {
  assert.equal(resolveTheme('light'), 'light')
  assert.equal(resolveTheme('dark'), 'dark')

  const removedProps: string[] = []
  const styleProps = new Map<string, string>()
  const rootMock = {
    dataset: {} as Record<string, string>,
    style: {
      colorScheme: '',
      setProperty(k: string, v: string) { styleProps.set(k, v) },
      removeProperty(k: string) { styleProps.delete(k); removedProps.push(k) },
    },
  }
  const prevDocument = globalThis.document
  try {
    Object.defineProperty(globalThis, 'document', {
      configurable: true,
      value: {
        documentElement: rootMock,
        querySelector: () => null,
      },
    })
    applyAppearanceToDom({
      theme: 'dark',
      accent: 'mint',
      accentCustomColor: '#7c5cff',
      ambientGradient: true,
      gradientPalette: 'custom',
      gradientCustomColor: '#112233',
      gradientCustomColor2: '#445566',
    })
    assert.equal(rootMock.dataset.theme, 'dark')
    assert.equal(rootMock.dataset.ambient, 'on')
    assert.ok(styleProps.has('--orb-1'))

    applyAppearanceToDom({
      theme: 'light',
      accent: 'mint',
      accentCustomColor: '#7c5cff',
      ambientGradient: true,
      gradientPalette: 'custom',
      gradientCustomColor: '#112233',
      gradientCustomColor2: '#445566',
    })
    assert.equal(rootMock.dataset.theme, 'light')
    assert.equal(rootMock.dataset.ambient, 'off')
    assert.equal(styleProps.has('--orb-1'), false)
    assert.ok(removedProps.includes('--orb-1'))
  } finally {
    Object.defineProperty(globalThis, 'document', { configurable: true, value: prevDocument })
  }

  const darkHtml = renderToStaticMarkup(createElement(AmbientBackground, { dimmed: true, active: false }))
  assert.match(darkHtml, /class="ambient-bg"/)

  const ambientSource = readFileSync(new URL('../src/app/AmbientBackground.tsx', import.meta.url), 'utf8')
  assert.match(ambientSource, /resolveTheme\(theme\) !== 'light'/)

  const cssSource = readFileSync(new URL('../src/style.css', import.meta.url), 'utf8')
  assert.match(cssSource, /:root\[data-theme="light"\]\s+\.ambient-bg\s*\{\s*display:\s*none;/)
})

test('ColorWheelPicker HSV<->HEX conversion round-trips accurately and appearanceStore stores custom colors', () => {
  for (const hex of ['#7c5cff', '#ff3b30', '#10b981']) {
    const hsv = hexToHsv(hex)
    const roundTripped = hsvToHex(hsv.h, hsv.s, hsv.v)
    assert.equal(roundTripped, hex)
  }

  const prev = useAppearance.getState()
  try {
    useAppearance.getState().setAccentCustomColor('#7C5CFF')
    useAppearance.getState().setGradientCustomColor('#112233')
    useAppearance.getState().setGradientCustomColor2('#445566')
    const next = useAppearance.getState()
    assert.equal(next.accent, 'custom')
    assert.equal(next.accentCustomColor, '#7c5cff')
    assert.equal(next.gradientPalette, 'custom')
    assert.equal(next.gradientCustomColor, '#112233')
    assert.equal(next.gradientCustomColor2, '#445566')
  } finally {
    useAppearance.setState({
      accent: prev.accent,
      accentCustomColor: prev.accentCustomColor,
      gradientPalette: prev.gradientPalette,
      gradientCustomColor: prev.gradientCustomColor,
      gradientCustomColor2: prev.gradientCustomColor2,
    })
  }
})

test('Unified header removes topbar strip and share button from chat header', () => {
  const appSource = readFileSync(new URL('../src/app/App.tsx', import.meta.url), 'utf8')
  assert.doesNotMatch(appSource, /className="topbar"/)
  assert.doesNotMatch(appSource, /className="share-action"/)
  assert.match(appSource, /className="chat-heading unified-header"/)
  assert.match(appSource, /context-resizer/)
})

test('Adaptive multi-provider UI hides provider switcher and multi-provider regenerate when <= 1 available provider', () => {
  const oneProvider = resolveAvailableProviders(true, [
    { id: 'codex', available: true },
    { id: 'claude', available: false },
    { id: 'antigravity', available: false },
  ], 'codex')
  assert.equal(oneProvider.length, 1)

  const twoProviders = resolveAvailableProviders(true, [
    { id: 'codex', available: true },
    { id: 'claude', available: true },
    { id: 'antigravity', available: false },
  ], 'codex')
  assert.equal(twoProviders.length, 2)

  const sampleTurn: Turn = {
    id: 'm1',
    prompt: 'Привет',
    time: 1000,
    model: 'GPT-5',
    provider: 'codex',
    seq: 1,
    state: 'completed',
    parts: [{ kind: 'text', text: '**Ответ** от модели' }],
  }

  const lastActionsHtml = renderToStaticMarkup(createElement(AssistantMessageActions, {
    chatId: 'c1',
    turn: sampleTurn,
    writable: true,
    canFork: true,
    busy: false,
    isLast: true,
  }))
  assert.match(lastActionsHtml, /aria-label="Повторить"/)
  assert.match(lastActionsHtml, /aria-label="Копировать"/)
  assert.match(lastActionsHtml, /aria-label="Создать ветку отсюда"/)
  assert.doesNotMatch(lastActionsHtml, /Другой провайдер/)
  assert.match(lastActionsHtml, /msg-actions is-last/)

  const olderActionsHtml = renderToStaticMarkup(createElement(AssistantMessageActions, {
    chatId: 'c1',
    turn: sampleTurn,
    writable: true,
    canFork: true,
    busy: false,
    isLast: false,
  }))
  assert.doesNotMatch(olderActionsHtml, /msg-actions is-last/)
  assert.doesNotMatch(olderActionsHtml, /Другой провайдер/)

  const singlePopoverHtml = renderToStaticMarkup(createElement(ModelPopover, {
    remoteMode: true,
    provider: 'codex',
    providerOptions: [{ id: 'codex', label: 'Codex', available: true }, { id: 'claude', label: 'Claude', available: false }],
    model: 'default',
    unavailableModel: false,
    disabled: false,
    canChangeProvider: false,
    providerDisabled: false,
    demoModelGroups: [],
    activeDemoGroup: undefined,
    hasDemoVariantReasoning: false,
    demoReasoningOptions: [],
    onSelectProvider: () => {},
    onSelectModel: () => {},
    onSelectReasoning: () => {},
  }))
  assert.doesNotMatch(singlePopoverHtml, /Провайдер/)
  assert.match(singlePopoverHtml, /Модель/)

  const multiPopoverHtml = renderToStaticMarkup(createElement(ModelPopover, {
    remoteMode: true,
    provider: 'codex',
    providerOptions: [{ id: 'codex', label: 'Codex', available: true }, { id: 'claude', label: 'Claude', available: true }],
    model: 'default',
    unavailableModel: false,
    disabled: false,
    canChangeProvider: true,
    providerDisabled: false,
    demoModelGroups: [],
    activeDemoGroup: undefined,
    hasDemoVariantReasoning: false,
    demoReasoningOptions: [],
    onSelectProvider: () => {},
    onSelectModel: () => {},
    onSelectReasoning: () => {},
  }))
  assert.match(multiPopoverHtml, /Провайдер/)
})

test('Markdown export, plain text stripping, and context menu position clamping work cleanly', () => {
  const c: Chat = {
    id: 'c1',
    title: 'Архитектура',
    access: 'owner',
    model: 'codex',
    target: { mode: 'chat' },
    draft: '',
    turns: [{
      id: 't1',
      prompt: 'Сделай обзор',
      time: 1_700_000_000_000,
      model: 'Codex',
      provider: 'codex',
      seq: 1,
      state: 'completed',
      parts: [
        { kind: 'thinking', text: 'Проверяю модули' },
        { kind: 'text', text: '### Итог\nВсё **готово** [док](https://example.com).' },
      ],
    }],
  }
  const md = exportChatMarkdown(c)
  assert.match(md, /^---\ntitle: "Архитектура"\ncreated: .+\nexported: .+\nproviders: \["codex"\]\nmodels: \["codex", "Codex"\]\nmessages: 2\n---/)
  assert.match(md, /\n# Архитектура\n/)
  assert.match(md, /## Пользователь/)
  assert.match(md, /## Ассистент — Codex · codex/)
  assert.match(md, /<summary>Размышления<\/summary>/)

  const plain = markdownToPlainText('### Итог\nВсё **готово** [док](https://example.com) и `x`.')
  assert.equal(plain, 'Итог\nВсё готово док и x.')

  const clamped = clampMenuPosition(1200, 900, 210, 240, 1280, 920)
  assert.equal(clamped.left, 1062)
  assert.equal(clamped.top, 672)
})

test('Stage 3: Sidebar folders, archive, SearchPalette Cmd+K, and Obsidian export with attachments', async () => {
  const { createChatFolder, deleteChatFolder, moveChatToFolder, toggleArchiveChat, searchChatsAndMessages, useChats } = await import('../src/app/chatStore')
  const { SearchPalette } = await import('../src/app/SearchPalette')

  const sidebarSource = readFileSync(new URL('../src/app/Sidebar.tsx', import.meta.url), 'utf8')
  assert.match(sidebarSource, /Переместить в папку/)
  assert.match(sidebarSource, /Архивировать/)
  assert.match(sidebarSource, /Архив \(/)

  const appSource = readFileSync(new URL('../src/app/App.tsx', import.meta.url), 'utf8')
  assert.match(appSource, /e\.key\.toLowerCase\(\) === 'k'/)
  assert.match(appSource, /<SearchPalette/)

  // Verify SearchPalette renders empty prompt state cleanly on SSR
  const searchHtml = renderToStaticMarkup(createElement(SearchPalette, { open: true, onClose: () => {} }))
  assert.match(searchHtml, /Поиск по чатам и сообщениям/)

  // Verify in-memory folders, archive, and search in chatStore
  useChats.setState({
    remote: null,
    folders: [],
    chats: [
      {
        id: 'c-org',
        title: 'Организация дел',
        access: 'owner',
        model: 'sonnet',
        target: { mode: 'chat' },
        draft: '',
        turns: [{
          id: 't-org-1',
          prompt: 'Напиши заметку про кварц',
          time: 1_700_000_000_000,
          model: 'Claude Sonnet',
          seq: 1,
          state: 'completed',
          attachments: [{ id: 'u1', kind: 'text', name: 'notes.txt' }],
          assistantAttachments: [{ id: 'a1', kind: 'pdf', name: 'summary.pdf', source: 'assistant' }],
          parts: [{ kind: 'text', text: 'Готово: **формула** $E=mc^2$.' }],
        }],
      },
    ],
  })

  const folder = await createChatFolder('Заметки', '#10b981')
  assert.ok(folder?.id)
  await moveChatToFolder('c-org', folder!.id)
  assert.equal(useChats.getState().chats[0]?.folderId, folder!.id)

  await toggleArchiveChat('c-org')
  assert.ok(useChats.getState().chats[0]?.archivedAt != null)

  await deleteChatFolder(folder!.id)
  assert.equal(useChats.getState().folders.length, 0)
  assert.equal(useChats.getState().chats[0]?.folderId, null)

  const hits = await searchChatsAndMessages('кварц')
  assert.equal(hits.length, 1)
  assert.equal(hits[0]?.chatId, 'c-org')
  assert.equal(hits[0]?.messageId, 't-org-1')

  const exported = exportChatMarkdown(useChats.getState().chats[0]!)
  assert.match(exported, /\*\*Прикреплённые файлы:\*\*\n- `notes\.txt`/)
  assert.match(exported, /\*\*Созданные файлы:\*\*\n- `summary\.pdf`/)
  assert.match(exported, /\$E=mc\^2\$/)
})
