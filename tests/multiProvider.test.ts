import assert from 'node:assert/strict'
import { test } from 'node:test'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { ModelPopover } from '../src/app/ComposerPopovers'
import { ServerModelSelect } from '../src/app/ServerModelSelect'
import {
  login, logout, refreshServerModels, selectChat, sendPrompt, setConnectionMode, switchChatProvider, useChats,
} from '../src/app/chatStore'

test('multi-provider chatStore scopes models, guards stale loads, switches provider in-place, and routes ws by active provider', async t => {
  const user = { id: 'u1', username: 'demo', role: 'user' }
  const device = { id: 'd1', userId: 'u1', name: 'Browser', lastSeenAt: 0, createdAt: 0, isCurrent: true }
  const chats = [
    { id: 'c-anti', ownerId: 'u1', access: 'owner', title: 'Anti chat', provider: 'antigravity', model: 'gemini-4', createdAt: 1, updatedAt: 1 },
    { id: 'c-shared', ownerId: 'u2', access: 'write', title: 'Shared write', provider: 'antigravity', model: 'gemini-4', createdAt: 1, updatedAt: 1 },
    { id: 'c-disabled', ownerId: 'u1', access: 'owner', title: 'Disabled paid', provider: 'paid_api', model: 'deepseek', createdAt: 1, updatedAt: 1 },
  ]
  const messagesByChat: Record<string, unknown[]> = {
    'c-anti': [
      { id: 'u-1', runId: 'r-1', chatId: 'c-anti', role: 'user', content: 'Привет', thinking: '', createdAt: 10 },
      { id: 'a-1', runId: 'r-1', chatId: 'c-anti', role: 'assistant', content: 'Здравствуйте!', thinking: '', createdAt: 11 },
    ],
  }
  let createdCount = 0
  const sentWsPayloads: { chatId: string; provider?: string; model: { provider: string; id: string } }[] = []
  let resolveSlowCodex: ((res: Response) => void) | undefined
  let codexRequests = 0

  class TestSocket {
    static readonly OPEN = 1
    readyState = TestSocket.OPEN
    onopen?: () => void
    onmessage?: (ev: { data: string }) => void
    onclose?: () => void
    constructor() {
      queueMicrotask(() => this.onopen?.())
    }
    send(raw: string) {
      const msg = JSON.parse(raw) as { chatId: string; messageId: string; provider?: string; model: { provider: string; id: string } }
      sentWsPayloads.push({ chatId: msg.chatId, ...(msg.provider ? { provider: msg.provider } : {}), model: msg.model })
      queueMicrotask(() => {
        this.onmessage?.({ data: JSON.stringify({ type: 'done', reason: 'completed', chatId: msg.chatId, messageId: msg.messageId, seq: 1 }) })
      })
    }
    close() { this.onclose?.() }
  }

  const prevWs = Object.getOwnPropertyDescriptor(globalThis, 'WebSocket')
  Object.defineProperty(globalThis, 'WebSocket', { configurable: true, value: TestSocket })
  t.after(() => {
    logout()
    setConnectionMode('demo')
    if (prevWs) Object.defineProperty(globalThis, 'WebSocket', prevWs)
    else Reflect.deleteProperty(globalThis, 'WebSocket')
  })

  t.mock.method(globalThis, 'fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input))
    const route = (init?.method ?? 'GET') + ' ' + url.pathname
    switch (route) {
      case 'POST /api/auth/login': return Response.json({ user, device, token: 'tok' })
      case 'GET /api/health': return Response.json({ status: 'ok', provider: 'antigravity' })
      case 'GET /api/devices': return Response.json([device])
      case 'GET /api/quotas/current': return Response.json(null, { status: 404 })
      case 'GET /api/users': return Response.json([user])
      case 'GET /api/chats': return Response.json(chats)
      case 'POST /api/ws/ticket': return Response.json({ ticket: 't1' })
      case 'POST /api/auth/logout': return new Response(null, { status: 204 })
      case 'GET /api/providers':
        return Response.json({
          defaultProvider: 'antigravity',
          providers: [
            { id: 'antigravity', available: true },
            { id: 'codex', available: true },
            { id: 'claude', available: false },
            { id: 'paid_api', available: false },
          ],
        })
      case 'GET /api/models':
        if (url.search === '?provider=codex') {
          codexRequests++
          if (codexRequests === 1) {
            return new Promise<Response>(resolve => { resolveSlowCodex = resolve })
          }
          return Response.json([
            { id: 'codex-old', name: 'Codex Old', isDefault: false },
            { id: 'gpt-5.2-codex', name: 'GPT-5.2-Codex', isDefault: true },
          ])
        }
        return Response.json([{ id: 'gemini-4', name: 'Gemini 4', isDefault: true }])
      case 'POST /api/chats': {
        createdCount++
        assert.fail('switchChatProvider should not create a new chat')
      }
      default:
        if (route.endsWith('/messages')) {
          const id = url.pathname.split('/')[3] ?? ''
          return Response.json(messagesByChat[id] ?? [])
        }
        assert.fail(route)
    }
  })

  setConnectionMode('server')
  await login({ connection: 'https://example.test', login: 'demo', password: 'pw', deviceName: 'Browser' })
  await new Promise(r => setTimeout(r, 15))

  assert.equal(useChats.getState().defaultServerProvider, 'antigravity')
  assert.deepEqual(useChats.getState().serverModelsByProvider.antigravity?.map(m => m.id), ['gemini-4'])

  // Shared write chat cannot switch provider
  await switchChatProvider('c-shared', 'codex')
  assert.equal(useChats.getState().chats.find(c => c.id === 'c-shared')?.provider, 'antigravity')

  // Stale async model load guard: start slow load 1, then newer load 2, then resolve load 1 after load 2
  const slowLoad = refreshServerModels('codex', true)
  const fastLoad = refreshServerModels('codex', true)
  await fastLoad
  assert.deepEqual(useChats.getState().serverModelsByProvider.codex?.map(m => m.id), ['codex-old', 'gpt-5.2-codex'])
  resolveSlowCodex?.(Response.json([{ id: 'stale-model', name: 'Stale', isDefault: true }]))
  await slowLoad
  assert.deepEqual(useChats.getState().serverModelsByProvider.codex?.map(m => m.id), ['codex-old', 'gpt-5.2-codex'])

  // Switching provider inside an existing chat with messages keeps the same chatId, turns, and draft, and picks default codex model
  useChats.setState(s => ({
    connected: true,
    chats: s.chats.map(c => c.id === 'c-anti' ? { ...c, draft: 'keep draft', historyLoaded: true } : c),
  }))
  const beforeTurns = useChats.getState().chats.find(c => c.id === 'c-anti')?.turns.length ?? 0
  assert.ok(beforeTurns > 0)
  await switchChatProvider('c-anti', 'codex')
  assert.equal(createdCount, 0)
  assert.equal(useChats.getState().activeId, 'c-anti')
  const switchedChat = useChats.getState().chats.find(c => c.id === 'c-anti')
  assert.equal(switchedChat?.provider, 'codex')
  assert.equal(switchedChat?.model, 'gpt-5.2-codex')
  assert.equal(switchedChat?.draft, 'keep draft')
  assert.equal(switchedChat?.turns.length, beforeTurns)

  // Disabled / unavailable provider switch is ignored
  await switchChatProvider('c-anti', 'claude')
  assert.equal(useChats.getState().chats.find(c => c.id === 'c-anti')?.provider, 'codex')

  // Switching back to another chat and returning restores c-anti's selected provider and model
  selectChat('c-shared')
  assert.equal(useChats.getState().activeId, 'c-shared')
  assert.equal(useChats.getState().chats.find(c => c.id === 'c-shared')?.provider, 'antigravity')
  selectChat('c-anti')
  assert.equal(useChats.getState().chats.find(c => c.id === 'c-anti')?.provider, 'codex')
  assert.equal(useChats.getState().chats.find(c => c.id === 'c-anti')?.model, 'gpt-5.2-codex')

  // Sending in the switched chat sends provider='codex' and model={ provider: 'codex', id: 'gpt-5.2-codex' }
  await sendPrompt('c-anti', 'hello codex')
  assert.deepEqual(sentWsPayloads[0], { chatId: 'c-anti', provider: 'codex', model: { provider: 'codex', id: 'gpt-5.2-codex' } })

  // Switching back to antigravity restores the previously used antigravity model
  await switchChatProvider('c-anti', 'antigravity')
  assert.equal(useChats.getState().chats.find(c => c.id === 'c-anti')?.provider, 'antigravity')
  assert.equal(useChats.getState().chats.find(c => c.id === 'c-anti')?.model, 'gemini-4')

  // Disabled provider chat cannot send
  useChats.setState(s => ({
    chats: s.chats.map(c => c.id === 'c-disabled' ? { ...c, historyLoaded: true } : c),
  }))
  await sendPrompt('c-disabled', 'should not send')
  assert.equal(sentWsPayloads.length, 1)

  // Static markup shows the unavailable provider state and preserves a model ID before hydration.
  const html1 = renderToStaticMarkup(createElement(ModelPopover, {
    remoteMode: true,
    provider: 'paid_api',
    providerOptions: [
      { id: 'antigravity', label: 'Antigravity', available: true },
      { id: 'codex', label: 'Codex', available: true },
      { id: 'paid_api', label: 'Paid API', available: false },
    ],
    model: 'deepseek',
    unavailableModel: false,
    disabled: false,
    canChangeProvider: true,
    providerDisabled: true,
    demoModelGroups: [],
    activeDemoGroup: undefined,
    hasDemoVariantReasoning: false,
    demoReasoningOptions: [],
    onSelectProvider: () => {},
    onSelectModel: () => {},
    onSelectReasoning: () => {},
  }))
  assert.ok(!html1.includes('(недоступно)'))
  assert.ok(html1.includes('Провайдер недоступен на сервере.'))

  const html2 = renderToStaticMarkup(createElement(ServerModelSelect, {
    provider: 'codex',
    model: 'gpt-5.2-codex',
    unavailable: false,
    disabled: false,
    onChangeModel: () => {},
    onChangeReasoning: () => {},
  }))
  assert.ok(html2.includes('gpt-5.2-codex'))
})

test('switchChatProvider allows switching to paid_api and loads paid_api models', async () => {
  const { switchChatProvider, useChats } = await import('../src/app/chatStore')
  const { RemoteTransport } = await import('../src/transport/RemoteTransport')

  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (url: string | URL | Request) => {
    const u = String(url)
    if (u.includes('/api/models?provider=paid_api')) {
      return new Response(JSON.stringify([
        { id: 'deepseek-chat', displayName: 'DeepSeek Chat', isDefault: true, reasoningLevels: [] },
        { id: 'gpt-4o-mini', displayName: 'GPT-4o Mini', isDefault: false, reasoningLevels: [] },
      ]), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    return new Response('{}', { status: 200 })
  }) as typeof fetch

  try {
    const remote = new RemoteTransport('http://127.0.0.1:8080')
    useChats.setState({
      remote,
      providersLoaded: true,
      serverProviders: [
        { id: 'codex', available: true },
        { id: 'paid_api', available: true },
      ],
      serverModelsByProvider: {},
      chats: [{
        id: 'c1',
        title: 'Test',
        provider: 'codex',
        model: 'gpt-5.2-codex',
        access: 'owner',
        target: { mode: 'chat' },
        turns: [],
        draft: '',
        attachments: [],
      }],
    })

    await switchChatProvider('c1', 'paid_api')
    const updated = useChats.getState().chats.find(c => c.id === 'c1')
    assert.equal(updated?.provider, 'paid_api')
    assert.equal(updated?.model, 'deepseek-chat')
  } finally {
    globalThis.fetch = originalFetch
  }
})
