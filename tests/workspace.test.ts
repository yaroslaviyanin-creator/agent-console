import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { RunRequest } from '../src/protocol/types'
import { MockTransport } from '../src/transport/MockTransport'

const credentials = (login: string) => ({ connection: btoa('mock://agent-console'), login, password: 'demo', deviceName: login + ' Mac' })
test('invalid connection or credentials do not bind devices', async () => {
  const { workspace } = new MockTransport()
  await assert.rejects(workspace.login({ ...credentials('demo'), connection: 'invalid' }))
  await assert.rejects(workspace.login({ ...credentials('demo'), password: 'wrong' }))
  const w = await workspace.login(credentials('demo'))
  assert.equal(w.devices.filter(d => d.name === 'demo Mac').length, 1)
})

test('sharing enforces read/fork/write, trust and revocation at the transport boundary', async () => {
  const mock = new MockTransport({ delayMs: 0 })
  const ws = mock.workspace
  const owner = await ws.login(credentials('demo'))
  const req: RunRequest = { chatId: 'timeout', messageId: 'response', prompt: 'npm test', model: { provider: 'codex', id: 'demo' }, target: { mode: 'mcp', deviceId: owner.deviceId } }
  await ws.setShare('timeout', 'alex', 'read', true)
  await ws.login(credentials('alex'))
  await assert.rejects(mock.stream(req, new AbortController().signal).next())
  await assert.rejects(ws.createChat('timeout'))
  await assert.rejects(ws.setShare('timeout', 'maria', 'write', true))
  await assert.rejects(ws.revokeDevice(owner.deviceId))
  await ws.login(credentials('demo'))
  await ws.setShare('timeout', 'alex', 'fork', false)
  await ws.login(credentials('alex'))
  const { id, workspace } = await ws.createChat('timeout')
  assert.ok(workspace.chats.some(c => c.id === id && c.access === 'owner'))
  await ws.login(credentials('demo'))
  await ws.setShare('timeout', 'alex', 'write', false)
  await ws.login(credentials('alex'))
  await assert.rejects(mock.stream(req, new AbortController().signal).next())
  await ws.login(credentials('demo'))
  await ws.setShare('timeout', 'alex', 'write', true)
  await ws.login(credentials('alex'))
  const events = []
  for await (const e of mock.stream(req, new AbortController().signal)) {
    assert.notEqual(e.type, 'permission_request')
    events.push(e)
  }
  assert.equal(events.at(-1)?.type, 'done')
  await assert.rejects(mock.stream({ ...req, prompt: 'Измени файл' }, new AbortController().signal).next())
  await ws.login(credentials('demo'))
  assert.equal(await ws.revokeDevice(owner.deviceId), null)
  const again = await ws.login(credentials('demo'))
  assert.notEqual(again.deviceId, owner.deviceId)
  assert.equal(again.chats.find(c => c.id === 'timeout')?.grants[0]?.trustedDeviceId, undefined)
  await ws.setShare('timeout', 'alex', null, false)
  const alex = await ws.login(credentials('alex'))
  assert.ok(!alex.chats.some(c => c.id === 'timeout'))
})

test('revoking an unrelated device preserves a pending approval on the current device', async () => {
  const mock = new MockTransport({ delayMs: 0 })
  const w = await mock.workspace.login(credentials('demo'))
  const req: RunRequest = { chatId: 'timeout', messageId: 'pending', prompt: 'npm test', model: { provider: 'claude', id: 'demo' }, target: { mode: 'mcp', deviceId: w.deviceId } }
  let completed = false
  for await (const e of mock.stream(req, new AbortController().signal)) {
    if (e.type === 'permission_request') {
      await mock.workspace.revokeDevice('archive')
      assert.equal(mock.respondToPermission(e.requestId, 'allow'), true)
    }
    if (e.type === 'done') completed = e.reason === 'completed'
  }
  assert.equal(completed, true)
})
