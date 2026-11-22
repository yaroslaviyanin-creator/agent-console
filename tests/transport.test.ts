import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { AccountEvent, PermissionDecision, RunEvent, RunRequest } from '../src/protocol/types'
import { MockTransport } from '../src/transport/MockTransport'

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
