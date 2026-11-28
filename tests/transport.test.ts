import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { AccountEvent, PermissionDecision, RunEvent, RunRequest } from '../src/protocol/types'
import { MockTransport } from '../src/transport/MockTransport'
import { RemoteTransport } from '../src/transport/RemoteTransport'

async function setup(options: ConstructorParameters<typeof MockTransport>[0]) {
  const mock = new MockTransport(options)
  const w = await mock.workspace.login({ connection: btoa('mock://agent-console'), login: 'demo', password: 'demo', deviceName: 'Test' })
  return Object.assign(mock, { deviceId: w.deviceId })
}

function request(mcp = false): RunRequest {
  return {
    chatId: 'tests', messageId: crypto.randomUUID(), prompt: mcp ? 'Предложи изменение settings.ts' : 'Проверка',
    model: { provider: 'claude', id: 'demo' },
    target: mcp ? { mode: 'mcp', deviceId: 'home' } : { mode: 'chat' },
  }
}

async function collect(mock: MockTransport, run: RunRequest, answer: PermissionDecision = 'allow') {
  const events: RunEvent[] = []
  for await (const event of mock.stream(run, new AbortController().signal)) {
    assert.equal(event.chatId, run.chatId)
    assert.equal(event.messageId, run.messageId)
    assert.equal(event.seq, events.length + 1)
    events.push(event)
    if (event.type === 'permission_request') {
      assert.equal(mock.respondToPermission(event.requestId, answer), true)
      assert.equal(mock.respondToPermission(event.requestId, answer), false)
    }
  }
  return events
}

test('chat streams multiple thinking/text deltas and ends exactly once', async () => {
  const events = await collect(await setup({ delayMs: 0 }), request())
  for (const type of ['thinking_delta', 'text_delta']) assert.ok(events.filter(e => e.type === type).length > 1)
  assert.equal(events.filter(e => e.type === 'done').length, 1)
  assert.equal(events.at(-1)?.type, 'done')
  assert.ok(!events.some(e => e.type === 'tool_use'))
})

test('folder approval applies only within the same chat and device', async () => {
  const mock = await setup({ delayMs: 0 })
  const first = await collect(mock, request(true), 'allow_folder')
  const approval = first.find(e => e.type === 'permission_request')
  assert.ok(approval?.tool.name === 'edit_file')
  assert.deepEqual(approval.tool.diff.map(line => line.kind), ['remove', 'add'])
  assert.ok(first.some(e => e.type === 'tool_result' && e.callId === approval.callId && e.status === 'ok'))
  const second = await collect(mock, { ...request(true), prompt: 'Установи тайм-аут в settings.ts на 4000 мс' })
  assert.ok(!second.some(e => e.type === 'permission_request'))
  const denied = await collect(mock, { ...request(true), target: { mode: 'mcp', deviceId: mock.deviceId } }, 'deny')
  assert.ok(denied.some(e => e.type === 'tool_result' && e.status === 'denied'))
  assert.ok(denied.some(e => e.type === 'done' && e.reason === 'denied'))
  assert.ok((await collect(mock, { ...request(true), chatId: 'review' })).some(e => e.type === 'permission_request'))
})

test('a pending edit waits for a decision and abort invalidates it', { timeout: 2000 }, async () => {
  const mock = await setup({ delayMs: 0 })
  const controller = new AbortController()
  const stream = mock.stream(request(true), controller.signal)
  for await (const event of stream) {
    if (event.type !== 'permission_request') continue
    const next = stream.next()
    const result = await Promise.race([next, new Promise(resolve => setTimeout(() => resolve('waiting'), 20))])
    assert.equal(result, 'waiting')
    controller.abort()
    await assert.rejects(next, { name: 'AbortError' })
    assert.equal(mock.respondToPermission(event.requestId, 'allow'), false)
    return
  }
  assert.fail('missing permission request')
})

test('refusal and error have distinct terminal outcomes', async () => {
  const refusal = await collect(await setup({ delayMs: 0, scenario: 'refusal' }), request())
  assert.deepEqual(refusal.slice(-2).map(e => e.type), ['refusal_detected', 'done'])
  const error = await collect(await setup({ delayMs: 0, scenario: 'error' }), request())
  assert.equal(error.at(-1)?.type, 'error')
  assert.ok(!error.some(e => e.type === 'done'))
})

test('account subscription receives incoming request without a run and quota updates through unsubscribe', async () => {
  const mock = await setup({ delayMs: 0, scenario: 'quota' })
  const account: AccountEvent[] = []
  const stop = mock.subscribeAccount(event => account.push(event))
  assert.equal(account.length, 3)
  await new Promise(resolve => setTimeout(resolve, 10))
  assert.ok(account.some(event => event.type === 'quota_transfer_request' && event.from.name === 'Алексей'))
  await collect(mock, request())
  const quota = account.at(-1)
  assert.ok(quota?.type === 'quota_update' && quota.provider === 'claude')
  assert.equal(quota.fiveHour.usedPercent, 50)
  stop()
  const count = account.length
  await collect(mock, request())
  assert.equal(account.length, count)
})

test('all six MCP tools use one virtual project per device and decline leaves files intact', async () => {
  const mock = await setup({ delayMs: 0 })
  const run = (prompt: string, answer: PermissionDecision = 'allow', deviceId = 'home') =>
    collect(mock, { ...request(true), prompt, target: { mode: 'mcp', deviceId } }, answer)
  const outputs = (events: RunEvent[]) => events.flatMap(e => e.type === 'tool_result' ? [e.output] : [])
  const list = () => run('Покажи файлы проекта').then(outputs)
  assert.deepEqual(await list(), ['README.md\nsettings.ts'])
  await run('Создай notes.md с текстом: timeout пример', 'deny')
  assert.deepEqual(await list(), ['README.md\nsettings.ts'])
  await run('Создай notes.md с текстом: timeout пример')
  assert.deepEqual(await list(), ['README.md\nnotes.md\nsettings.ts'])
  assert.deepEqual(outputs(await run('Прочитай notes.md')), ['timeout пример'])
  assert.equal((await run('Создай notes.md с текстом: второй вариант')).at(-1)?.type, 'error')
  assert.deepEqual(outputs(await run('Прочитай notes.md')), ['timeout пример'])
  await run('Установи тайм-аут в settings.ts на 5000 мс', 'deny')
  assert.deepEqual(outputs(await run('Прочитай settings.ts')), ['export const timeout = 1000'])
  await run('Установи тайм-аут в settings.ts на 5000 мс')
  assert.deepEqual(outputs(await run('Прочитай settings.ts')), ['export const timeout = 5000'])
  assert.match(outputs(await run('Найди «5000» в проекте'))[0]!, /settings\.ts:1 .*5000/)
  assert.deepEqual(outputs(await run('Найди «неизвестно» в проекте')), ['Совпадений нет.'])
  assert.deepEqual(outputs(await run('Прочитай settings.ts', 'allow', mock.deviceId)), ['export const timeout = 1000'])
  assert.equal((await run('Прочитай notes.md', 'allow', mock.deviceId)).at(-1)?.type, 'error')
  assert.match(outputs(await run('Запусти тесты'))[0]!, /5 тестов пройдено/)
  const unknown = await run('Погода завтра?')
  assert.ok(!unknown.some(e => e.type === 'tool_use'))
})

test('approving a stale diff cannot overwrite a change accepted in another chat', async () => {
  const mock = await setup({ delayMs: 0 })
  const first = mock.stream(request(true), new AbortController().signal)
  for await (const event of first) {
    if (event.type === 'permission_request') {
      await collect(mock, { ...request(true), chatId: 'review', prompt: 'Установи тайм-аут в settings.ts на 9000 мс' })
      assert.ok(mock.respondToPermission(event.requestId, 'allow'))
    }
    if (event.type === 'error') {
      assert.match(event.message, /изменился/)
      const read = await collect(mock, { ...request(true), prompt: 'Прочитай settings.ts' })
      assert.ok(read.some(e => e.type === 'tool_result' && e.output === 'export const timeout = 9000'))
      return
    }
  }
  assert.fail('expected conflict')
})

test('remote sessions connect with a ticket and manage server devices', async t => {
  const current = { id: 'current', userId: 'u', name: 'Test', lastSeenAt: 0, createdAt: 0, isCurrent: true }
  const session = { user: { id: 'u', username: 'demo' }, device: current, token: 'device-token' }
  const old = { ...current, id: 'old', name: 'Old device', lastSeenAt: 1_700_000_000_000, isCurrent: false }
  let usedCents = 120
  const quota = () => ({
    planTier: 'pro', bypass5hLimit: false, subscriptionUntil: 0,
    fiveHour: { usedCents, limitCents: 1500, usedPercent: usedCents / 15, resetsAt: 123 },
    sevenDay: { usedCents, limitCents: 15000, usedPercent: usedCents / 150, resetsAt: 456 },
  })
  let devices = [current, old]
  const originalProfile = { displayName: '', profileMd: '', memoryMd: '', memoryEnabled: true, memoryUpdatedAt: 0 }
  let profile = originalProfile
  let loginLimited = true
  let ticketStatus = 200
  let tickets = 0
  let logouts = 0
  let logoutKeepalive: boolean | undefined
  let closedAtLogout: boolean | undefined
  let socketUrl: URL | undefined
  let closed = false
  class TestSocket {
    static readonly OPEN = 1
    readyState = TestSocket.OPEN
    onclose?: () => void
    close() { closed = true; this.onclose?.() }
    constructor(url: URL) { socketUrl = url }
  }
  const webSocket = Object.getOwnPropertyDescriptor(globalThis, 'WebSocket')
  Object.defineProperty(globalThis, 'WebSocket', { configurable: true, value: TestSocket })
  t.after(() => {
    if (webSocket) Object.defineProperty(globalThis, 'WebSocket', webSocket)
    else Reflect.deleteProperty(globalThis, 'WebSocket')
  })
  t.mock.method(globalThis, 'fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    assert.ok(init)
    const route = init.method + ' ' + new URL(String(input)).pathname
    if (route !== 'POST /api/auth/login') {
      assert.equal(new Headers(init.headers).get('Authorization'), 'Bearer device-token')
    }
    switch (route) {
      case 'POST /api/auth/login':
        return loginLimited ? Response.json({ code: 'rate_limited' }, { status: 429 }) : Response.json(session)
      case 'GET /api/health': return Response.json({ status: 'ok', provider: 'stub' })
      case 'GET /api/chats': return Response.json([])
      case 'GET /api/users': return Response.json([])
      case 'GET /api/devices': return Response.json(devices)
      case 'GET /api/profile': return Response.json(profile)
      case 'PATCH /api/profile': {
        assert.equal(new Headers(init.headers).get('Content-Type'), 'application/json')
        const patch = JSON.parse(String(init.body))
        assert.deepEqual(patch, { displayName: 'Ярослав', memoryEnabled: false })
        profile = { ...profile, ...patch, memoryUpdatedAt: 123 }
        return Response.json(profile)
      }
      case 'GET /api/quotas/current': return Response.json(quota())
      case 'POST /api/ws/ticket':
        tickets++
        return ticketStatus === 200 ? Response.json({ ticket: 'single-use' }) :
          Response.json(ticketStatus === 401 ? {} : { code: 'account_inactive' }, { status: ticketStatus })
      case 'DELETE /api/devices/old':
      case 'DELETE /api/devices/current':
        devices = devices.filter(device => !route.endsWith('/' + device.id))
        return new Response(null, { status: 204 })
      case 'POST /api/auth/logout':
        logouts++
        logoutKeepalive = init.keepalive
        closedAtLogout = closed
        return new Response(null, { status: 204 })
      default: assert.fail(route)
    }
  })

  const login = { connection: 'https://example.test', login: 'demo', password: 'demo', deviceName: 'Test' }
  const remote = new RemoteTransport(login.connection)
  t.after(() => remote.close())
  await assert.rejects(remote.workspace.login(login), {
    status: 429, message: 'Слишком много попыток входа. Попробуйте позже.',
  })
  loginLimited = false
  const workspace = await remote.workspace.login(login)
  assert.deepEqual(remote.sessionQuota, quota())
  assert.deepEqual(await remote.getProfile(), originalProfile)
  assert.deepEqual(await remote.updateProfile({ displayName: 'Ярослав', memoryEnabled: false }), {
    ...originalProfile, displayName: 'Ярослав', memoryEnabled: false, memoryUpdatedAt: 123,
  })
  usedCents = 121
  assert.deepEqual(await remote.quota(), quota())
  assert.deepEqual(workspace.devices.map(d => d.id), ['current', 'old'])
  assert.equal(workspace.devices.find(d => d.id === old.id)?.lastSeenAt, old.lastSeenAt)
  await Promise.all([remote.connect(), remote.connect()])
  assert.equal(tickets, 1)
  assert.equal(socketUrl?.href, 'wss://example.test/ws?ticket=single-use')
  assert.deepEqual((await remote.workspace.revokeDevice('old'))?.devices.map(d => d.id), ['current'])
  devices = [current, old]
  assert.deepEqual((await remote.refreshDevices()).devices.map(d => d.id), ['current', 'old'])
  const logout = remote.workspace.logout()
  assert.equal(remote.sessionToken, '')
  await logout
  assert.equal(logouts, 1)
  assert.equal(logoutKeepalive, true)
  assert.equal(closedAtLogout, true)
  assert.equal(closed, true)
  assert.equal(remote.sessionToken, '')

  const expired = new RemoteTransport(login.connection)
  t.after(() => expired.close())
  const events: string[] = []
  expired.subscribe(event => events.push(event.type))
  await expired.workspace.login(login)
  for (const status of [401, 403]) {
    ticketStatus = status
    socketUrl = undefined
    await expired.connect()
    assert.equal(socketUrl, undefined)
  }
  assert.deepEqual(events, ['expired', 'expired'])
  assert.equal(await expired.workspace.revokeDevice('current'), null)
  assert.equal(expired.sessionToken, '')
  assert.equal(logouts, 1)
})

test('registration and session restore stay offline until the account is approved', async t => {
  const device = { id: 'current', userId: 'u', name: 'Browser', lastSeenAt: 0, createdAt: 0 }
  const user = { id: 'u', username: 'alice', role: 'pending', planTier: 'none', isActive: true }
  const requests: string[] = []
  t.mock.method(globalThis, 'fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    assert.ok(init)
    const route = init.method + ' ' + new URL(String(input)).pathname
    requests.push(route)
    const authorization = new Headers(init.headers).get('Authorization')
    if (route === 'POST /api/auth/register') {
      assert.equal(authorization, null)
      assert.deepEqual(JSON.parse(String(init.body)), { username: 'alice', password: 'a-password', deviceName: 'Browser' })
      return Response.json({ user, pending: true }, { status: 201 })
    }
    assert.equal(authorization, 'Bearer registered-token')
    switch (route) {
      case 'GET /api/auth/me': return Response.json({ user, device })
      case 'GET /api/health': return Response.json({ status: 'ok', provider: 'stub' })
      case 'GET /api/devices': return Response.json([device])
      case 'GET /api/quotas/current': return Response.json({
        planTier: 'pro', bypass5hLimit: true, subscriptionUntil: 0,
        fiveHour: { usedCents: 0, limitCents: 1500, usedPercent: 0, resetsAt: 0 },
        sevenDay: { usedCents: 0, limitCents: 15000, usedPercent: 0, resetsAt: 0 },
      })
      case 'GET /api/chats': return Response.json([])
      case 'GET /api/users': return Response.json([])
      default: assert.fail(route)
    }
  })
  const remote = new RemoteTransport('https://example.test')
  const restored = new RemoteTransport(remote.address)
  t.after(() => { remote.close(); restored.close() })
  const regRes = await remote.register({ connection: remote.address, login: 'alice', password: 'a-password', deviceName: 'Browser' })
  assert.ok('pending' in regRes && regRes.pending)
  assert.equal(remote.sessionToken, '')
  await restored.restore('registered-token')
  await remote.connect()
  await restored.connect()
  const pendingQuota = restored.sessionQuota
  assert.equal(pendingQuota, null)
  assert.deepEqual(requests.splice(0), ['POST /api/auth/register', 'GET /api/auth/me'])
  user.role = 'user'
  user.planTier = 'pro'
  assert.equal((await restored.checkStatus()).user.role, 'user')
  assert.equal(restored.sessionQuota?.bypass5hLimit, true)
  assert.deepEqual(requests.sort(), ['GET /api/auth/me', 'GET /api/chats', 'GET /api/devices', 'GET /api/health', 'GET /api/quotas/current'])
})

test('remote workspace lists members, updates grants and forks shared chats', async t => {
  const alice = { id: 'alice', username: 'Алиса' }
  const bob = { id: 'bob', username: 'Боб' }
  const device = { id: 'browser', userId: alice.id, name: 'Browser', lastSeenAt: 0, createdAt: 0 }
  const owner = { id: 'mine', ownerId: alice.id, access: 'owner', title: 'Mine', provider: 'stub', model: 'default', createdAt: 0, updatedAt: 0 }
  const shared = { ...owner, id: 'shared/thread', ownerId: bob.id, access: 'fork' }
  const fork = { ...owner, id: 'copy', title: 'Shared · копия' }
  let grant: { userId: string; access: string }[] = []
  let directoryLoads = 0
  t.mock.method(globalThis, 'fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    assert.ok(init)
    const route = init.method + ' ' + new URL(String(input)).pathname
    switch (route) {
      case 'POST /api/auth/login': return Response.json({ user: alice, device, token: 'token' })
      case 'GET /api/health': return Response.json({ status: 'ok', provider: 'stub' })
      case 'GET /api/devices': return Response.json([device])
      case 'GET /api/quotas/current': return Response.json({
        planTier: 'pro', bypass5hLimit: true, subscriptionUntil: 0,
        fiveHour: { usedCents: 0, limitCents: 1500, usedPercent: 0, resetsAt: 0 },
        sevenDay: { usedCents: 0, limitCents: 15000, usedPercent: 0, resetsAt: 0 },
      })
      case 'GET /api/users':
        directoryLoads++
        return Response.json([alice, bob])
      case 'GET /api/chats': return Response.json([{ ...owner, grants: grant }, shared])
      case 'PUT /api/chats/mine/shares/bob':
        assert.deepEqual(JSON.parse(String(init.body)), { access: 'write' })
        grant = [{ userId: bob.id, access: 'write' }]
        return new Response(null, { status: 204 })
      case 'DELETE /api/chats/mine/shares/bob':
        assert.equal(init.body, null)
        grant = []
        return new Response(null, { status: 204 })
      case 'POST /api/chats/shared%2Fthread/fork': return Response.json(fork, { status: 201 })
      default: assert.fail(route)
    }
  })
  const remote = new RemoteTransport('https://example.test')
  t.after(() => remote.close())
  const workspace = await remote.workspace.login({
    connection: remote.address, login: alice.username, password: 'password', deviceName: 'Browser',
  })
  assert.deepEqual(workspace.users.map(user => [user.id, user.name]), [[alice.id, alice.username]])
  assert.equal(directoryLoads, 0)
  const found = await remote.searchUsers('бо')
  assert.deepEqual(found, [alice, bob])
  assert.equal(directoryLoads, 1)
  assert.deepEqual(workspace.chats[1], { id: shared.id, ownerId: bob.id, access: 'fork', grants: [] })
  const granted = await remote.workspace.setShare(owner.id, bob.id, 'write', true)
  assert.deepEqual(granted.chats[0]?.grants, grant)
  const revoked = await remote.workspace.setShare(owner.id, bob.id, null, false)
  assert.deepEqual(revoked.chats[0]?.grants, [])
  await remote.refreshChats()
  assert.equal(directoryLoads, 1)
  const copy = await remote.workspace.createChat(shared.id)
  assert.equal(copy.id, fork.id)
  assert.deepEqual(copy.workspace.chats[0], { id: fork.id, ownerId: alice.id, access: 'owner', grants: [] })
})

test('remote providers, scoped models, 404 fallback, and createChat with provider', async t => {
  const user = { id: 'u', username: 'demo' }
  const device = { id: 'd', userId: 'u', name: 'Browser', lastSeenAt: 0, createdAt: 0 }
  let providersStatus = 200
  const calls: { route: string; query: string; body?: unknown }[] = []

  t.mock.method(globalThis, 'fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    assert.ok(init)
    const url = new URL(String(input))
    const route = init.method + ' ' + url.pathname
    calls.push({ route, query: url.search, body: init.body ? JSON.parse(String(init.body)) : undefined })
    switch (route) {
      case 'POST /api/auth/login': return Response.json({ user, device, token: 'tok' })
      case 'GET /api/health': return Response.json({ status: 'ok', provider: 'antigravity' })
      case 'GET /api/devices': return Response.json([device])
      case 'GET /api/quotas/current': return Response.json(null, { status: 404 })
      case 'GET /api/chats': return Response.json([])
      case 'GET /api/users': return Response.json([])
      case 'GET /api/providers':
        if (providersStatus === 404) return Response.json({ code: 'not_found' }, { status: 404 })
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
          return Response.json([{ id: 'gpt-5.2-codex', name: 'GPT-5.2-Codex', isDefault: true }])
        }
        return Response.json([{ id: 'gemini-4-hydrogen-medium', name: 'Gemini 4 Hydrogen (Medium)', isDefault: true }])
      case 'POST /api/chats': {
        const body = JSON.parse(String(init.body)) as { provider?: string; model?: string; title?: string }
        return Response.json({
          id: 'created-1',
          title: body.title ?? 'Новый чат',
          provider: body.provider ?? 'antigravity',
          model: body.model ?? 'default',
          createdAt: 1,
          updatedAt: 1,
        }, { status: 201 })
      }
      default: assert.fail(route)
    }
  })

  const remote = new RemoteTransport('https://example.test')
  t.after(() => remote.close())
  await remote.workspace.login({ connection: remote.address, login: 'demo', password: 'pw', deviceName: 'Browser' })

  const list = await remote.providers()
  assert.equal(list.defaultProvider, 'antigravity')
  assert.deepEqual(list.providers.map(p => [p.id, p.available]), [
    ['antigravity', true],
    ['codex', true],
    ['claude', false],
    ['paid_api', false],
  ])

  providersStatus = 404
  const fallback = await remote.providers()
  assert.equal(fallback.defaultProvider, 'antigravity')
  assert.deepEqual(fallback.providers, [
    { id: 'antigravity', available: true },
    { id: 'codex', available: false },
    { id: 'claude', available: false },
    { id: 'paid_api', available: false },
  ])

  const codexModels = await remote.models('codex')
  assert.deepEqual(codexModels.map(m => m.id), ['gpt-5.2-codex'])
  const defaultModels = await remote.models()
  assert.deepEqual(defaultModels.map(m => m.id), ['gemini-4-hydrogen-medium'])

  const created = await remote.createChat({ provider: 'codex', model: 'gpt-5.2-codex', title: 'Codex chat' })
  assert.equal(created.id, 'created-1')
  assert.equal(remote.chats[0]?.provider, 'codex')
  const postCall = calls.find(c => c.route === 'POST /api/chats')
  assert.deepEqual(postCall?.body, { provider: 'codex', model: 'gpt-5.2-codex', title: 'Codex chat' })
})

test('RemoteTransport reconnectNow() recovers WebSocket connection and emits connected: true', async (t) => {
  const prevFetch = globalThis.fetch
  const prevWS = Object.getOwnPropertyDescriptor(globalThis, 'WebSocket')
  t.after(() => {
    globalThis.fetch = prevFetch
    if (prevWS) Object.defineProperty(globalThis, 'WebSocket', prevWS)
    else Reflect.deleteProperty(globalThis, 'WebSocket')
  })

  const sockets: Array<{
    url: string
    readyState: number
    onopen: (() => void) | null
    onclose: (() => void) | null
    onerror: (() => void) | null
    onmessage: ((ev: { data: string }) => void) | null
    close: () => void
    send: (data: string) => void
  }> = []

  class FakeWebSocket {
    static OPEN = 1
    static CLOSED = 3
    url: string
    readyState = 0
    onopen: (() => void) | null = null
    onclose: (() => void) | null = null
    onerror: (() => void) | null = null
    onmessage: ((ev: { data: string }) => void) | null = null
    constructor(url: string) {
      this.url = url
      sockets.push(this)
    }
    close() {
      this.readyState = FakeWebSocket.CLOSED
    }
    send() {}
  }
  Object.defineProperty(globalThis, 'WebSocket', { configurable: true, value: FakeWebSocket })

  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url)
    const route = url.pathname
    if (route === '/api/auth/login') {
      return Response.json({
        token: 'tok-1',
        user: { id: 'u1', username: 'demo', displayName: 'Demo', role: 'user', status: 'approved', avatar: '' },
        device: { id: 'd1', name: 'Browser', kind: 'web', online: true },
        chats: [],
      })
    }
    if (route === '/api/ws/ticket') {
      return Response.json({ ticket: `t-${sockets.length + 1}` })
    }
    return Response.json([])
  }) as typeof fetch

  const remote = new RemoteTransport('https://example.test')
  t.after(() => remote.close())

  const waitForSockets = async (count: number) => {
    for (let i = 0; i < 30 && sockets.length < count; i++) {
      await new Promise((r) => setTimeout(r, 10))
    }
  }

  await remote.workspace.login({ connection: remote.address, login: 'demo', password: 'pw', deviceName: 'Browser' })
  await remote.connect()
  await waitForSockets(1)
  assert.equal(sockets.length, 1)
  sockets[0]!.readyState = FakeWebSocket.OPEN
  sockets[0]!.onopen?.()
  assert.equal(remote.connected, true)

  sockets[0]!.readyState = FakeWebSocket.CLOSED
  sockets[0]!.onclose?.()
  assert.equal(remote.connected, false)

  remote.reconnectNow()
  await waitForSockets(2)
  assert.equal(sockets.length, 2)
  sockets[1]!.readyState = FakeWebSocket.OPEN
  sockets[1]!.onopen?.()

  assert.equal(remote.connected, true)
})
