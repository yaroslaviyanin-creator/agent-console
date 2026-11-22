import assert from 'node:assert/strict'
import { test } from 'node:test'
import { MockTransport } from '../src/transport/MockTransport'
import type { AccountEvent, Provider, RunRequest } from '../src/protocol/types'

async function login(mock: MockTransport, user: string) {
  await mock.workspace.login({ login: user, password: 'demo', connection: btoa('mock://agent-console'), deviceName: 'Test' })
}
function snapshot(mock: MockTransport) {
  const events: AccountEvent[] = []
  mock.subscribeAccount(e => events.push(e))()
  return events
}
function claude(mock: MockTransport) {
  const q = snapshot(mock).find(e => e.type === 'quota_update' && e.provider === 'claude')
  assert.ok(q?.type === 'quota_update' && q.provider === 'claude')
  return q
}
function request(provider: Provider, chatId = 'review'): RunRequest {
  return { chatId, messageId: crypto.randomUUID(), model: {provider, id: 'demo'}, target: {mode:'chat'}, prompt:'Проверка' }
}
async function run(mock: MockTransport, request: RunRequest) {
  let last
  for await (const event of mock.stream(request,new AbortController().signal)) last = event
  return last?.type
}

test('transfers change only the selected provider and expire at the five-hour boundary', async () => {
  let now = 0
  const mock = new MockTransport({ now: () => now, delayMs: 0, scenario: 'quota' })
  await login(mock,'demo')
  assert.equal(await run(mock,request('claude')),'done')
  assert.equal(claude(mock).fiveHour.usedPercent,50)
  assert.equal(await run(mock,request('claude')),'error')
  assert.equal(await run(mock,request('codex')),'done')
  await mock.accounting.requestTransfer('alex','claude',5)
  const t = snapshot(mock).find(e => e.type === 'quota_transfer_request')
  assert.ok(t?.type === 'quota_transfer_request')
  await assert.rejects(mock.accounting.resolveTransfer(t.requestId,true))
  await login(mock,'alex')
  await mock.accounting.resolveTransfer(t.requestId,true)
  assert.equal(claude(mock).allowancePercent,45)
  await assert.rejects(mock.accounting.resolveTransfer(t.requestId,true))
  await login(mock,'demo')
  assert.equal(claude(mock).allowancePercent,55)
  assert.equal(await run(mock,request('claude')),'done')
  await mock.accounting.requestTransfer('maria','claude',10)
  now += 5 * 3600_000
  assert.equal(claude(mock).allowancePercent,50)
  assert.equal(claude(mock).fiveHour.usedPercent,0)
  assert.ok(claude(mock).sevenDay.usedPercent > 12)
  await login(mock,'maria')
  const expired = snapshot(mock).find(e => e.type === 'quota_transfer_request')
  assert.ok(expired?.type === 'quota_transfer_request' && expired.status === 'expired')
  await assert.rejects(mock.accounting.resolveTransfer(expired.requestId,true))
  now += 7 * 86400_000
  assert.equal(claude(mock).sevenDay.usedPercent,0)
})

test('insufficient donations and declined requests preserve balances', async () => {
  const mock = new MockTransport()
  await login(mock,'demo')
  for (const percent of [0,51,1.2,NaN]) await assert.rejects(mock.accounting.requestTransfer('alex','codex',percent))
  await mock.accounting.requestTransfer('alex','claude',50)
  await assert.rejects(mock.accounting.requestTransfer('alex','claude',5))
  await login(mock,'alex')
  const t = snapshot(mock).find(e => e.type === 'quota_transfer_request')
  assert.ok(t?.type === 'quota_transfer_request')
  await assert.rejects(mock.accounting.resolveTransfer(t.requestId,true))
  await mock.accounting.resolveTransfer(t.requestId,false)
  assert.equal(claude(mock).allowancePercent,50)
  await login(mock,'demo')
  assert.equal(claude(mock).allowancePercent,50)
})

test('paid runs are counted once and cannot exceed the shared cap, closed periods preserve user rows', async () => {
  const mock = new MockTransport({ delayMs: 0 })
  await login(mock,'alex')
  await mock.accounting.setHardLimit(10)
  const r = request('paid_api','team')
  const outcomes = await Promise.all([run(mock,r),run(mock,r),run(mock,request('paid_api','team'))])
  assert.deepEqual(outcomes.sort(),['done','error','error'])
  const bill = await mock.accounting.billing()
  assert.equal(bill.current.rows.find(r => r.user.id === 'alex')?.cents,2)
  assert.ok(bill.current.rows.some(r => r.user.id === 'alex' && r.cacheTokens > 0 && r.tokens > r.cacheTokens))
  await login(mock,'demo')
  assert.equal(await run(mock,request('paid_api')),'error')
  const fresh = await mock.accounting.closePeriod(bill.current.id)
  assert.deepEqual(fresh.closed[0]?.rows,bill.current.rows)
  assert.equal(fresh.current.rows.reduce((s,r) => s+r.cents,0),0)
  assert.equal(fresh.hardLimitCents,10)
  await assert.rejects(mock.accounting.closePeriod(bill.current.id))
  await mock.accounting.setHardLimit(0)
  assert.equal(await run(mock,request('paid_api')),'error')
})
