import assert from 'node:assert/strict'
import { test } from 'node:test'
import { login, logout, newChat, retryAnswer, sendPrompt, updateChat, useChats } from '../src/app/chatStore'
import { transport } from '../src/app/runtime'
import type { RunRequest } from '../src/protocol/types'

test('retry replays the original prompt with its own prefix, preserves draft, targets and permissions', async t => {
  const sent: RunRequest[] = []
  t.mock.method(transport, 'stream', async function* (r: RunRequest) {
    sent.push(r)
    const base = { messageId: r.messageId, chatId: r.chatId }
    const refused = r.prompt === 'Вопрос B' && !r.retryOf
    yield { ...base, seq: 1, type: 'text_delta', delta: refused ? 'Отказ B' : 'Ответ ' + r.prompt }
    if (refused) yield { ...base, seq: 2, type: 'refusal_detected', reason: 'Демо' }
    yield { ...base, seq: 3, type: 'done', reason: refused ? 'refused' : 'completed' }
  })
  await login({ login: 'demo', password: 'demo', connection: btoa('mock://agent-console'), deviceName: 'Test' })
  try {
    await newChat()
    const id = useChats.getState().activeId
    const chat = () => useChats.getState().chats.find(c => c.id === id)!
    await sendPrompt(id, 'Вопрос A')
    updateChat(id, c => ({ ...c, model: 'opus' }))
    await sendPrompt(id, 'Вопрос B')
    assert.deepEqual(sent.slice(0, 2).map(r => r.model), [
      { id: 'sonnet', provider: 'claude' }, { id: 'opus', provider: 'claude' },
    ])
    assert.deepEqual(chat().turns.map(t => t.model), ['Claude Sonnet', 'Claude Opus'])
    const source = chat().turns[1]!
    assert.equal(source.state,'refused')
    await sendPrompt(id, 'Позже')
    updateChat(id, c => ({ ...c, draft: 'Черновик', target: { mode: 'mcp', deviceId: 'home' } }))
    await retryAnswer(id,source.id,'codex')
    assert.deepEqual(sent.at(-1)?.history,[{role:'user', text:'Вопрос A'},{role:'assistant', text:'Ответ Вопрос A'}])
    assert.equal(sent.at(-1)?.prompt,source.prompt)
    assert.equal(sent.at(-1)?.retryOf,source.id)
    assert.equal(sent.at(-1)?.target.mode,'chat')
    assert.equal(chat().draft,'Черновик')
    assert.equal(chat().turns.length,4)
    await retryAnswer(id,chat().turns.at(-1)!.id,'deepseek')
    assert.deepEqual(sent.at(-1)?.history,sent.at(-2)?.history)
    await sendPrompt(id,'Продолжить')
    assert.deepEqual(sent.at(-1)?.history?.map(m => m.text),['Вопрос A','Ответ Вопрос A','Вопрос B','Ответ Вопрос B'])
    const count = sent.length
    await retryAnswer('shared','sample-shared','codex')
    await retryAnswer('branch','sample-branch','codex')
    assert.equal(sent.length,count)
  } finally { logout() }
})
