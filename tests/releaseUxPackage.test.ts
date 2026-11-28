import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { clearChatDraft, hasNonEmptyChatDraft, NEW_CHAT_DRAFT_KEY, readChatDraft, writeChatDraft } from '../src/app/drafts'
import { applySlashTemplate, filterPromptTemplates, findSlashQuery } from '../src/app/SlashCommandMenu'
import { formatQuoteMarkdown } from '../src/app/QuoteReplyButton'
import { getMermaidInitConfig } from '../src/app/MermaidBlock'
import { RichText } from '../src/app/Markdown'
import { getSpeechRecognitionCtor } from '../src/app/voiceInput'
import { createChatFolder, useChats } from '../src/app/chatStore'
import { ProjectSettingsModal } from '../src/app/ProjectSettingsModal'

function withMockLocalStorage(fn: () => void) {
  const store = new Map<string, string>()
  const prev = (globalThis as { localStorage?: Storage }).localStorage
  ;(globalThis as { localStorage: Storage }).localStorage = {
    get length() { return store.size },
    clear() { store.clear() },
    getItem(k: string) { return store.get(k) ?? null },
    key(i: number) { return Array.from(store.keys())[i] ?? null },
    removeItem(k: string) { store.delete(k) },
    setItem(k: string, v: string) { store.set(k, v) },
  }
  try {
    fn()
  } finally {
    if (prev === undefined) delete (globalThis as { localStorage?: Storage }).localStorage
    else (globalThis as { localStorage: Storage }).localStorage = prev
  }
}

test('per-chat drafts persist, distinguish chats and new chat, and clear on send', () => {
  withMockLocalStorage(() => {
    writeChatDraft('chat-1', 'Черновик первого чата')
    writeChatDraft(null, 'Черновик нового чата')
    assert.equal(readChatDraft('chat-1'), 'Черновик первого чата')
    assert.equal(readChatDraft(NEW_CHAT_DRAFT_KEY), 'Черновик нового чата')
    assert.equal(hasNonEmptyChatDraft('chat-1'), true)
    assert.equal(hasNonEmptyChatDraft('chat-2'), false)
    clearChatDraft('chat-1')
    assert.equal(readChatDraft('chat-1'), '')
    assert.equal(hasNonEmptyChatDraft('chat-1'), false)
  })
})

test('slash command query detection, filtering, and template insertion work cleanly', () => {
  const templates = [
    { id: '1', slash: '/конспект', title: 'Краткий конспект', content: 'Сделай конспект:\n' },
    { id: '2', slash: '/ревью', title: 'Код-ревью', content: 'Проверь код:\n' },
  ]
  const match1 = findSlashQuery('/кон', 4)
  assert.deepEqual(match1, { start: 0, end: 4, query: '/кон' })
  assert.equal(filterPromptTemplates(templates, match1!.query).length, 1)
  assert.equal(filterPromptTemplates(templates, match1!.query)[0]!.id, '1')

  // Does not trigger mid-word without preceding whitespace
  assert.equal(findSlashQuery('https://example.com/рев', 23), null)

  // Triggers after whitespace
  const match2 = findSlashQuery('Привет /рев', 11)
  assert.deepEqual(match2, { start: 7, end: 11, query: '/рев' })
  const applied = applySlashTemplate('Привет /рев', match2!, templates[1]!)
  assert.equal(applied.nextText, 'Привет Проверь код:\n')
})

test('Quote Reply formats multiline selections as Markdown blockquotes', () => {
  assert.equal(formatQuoteMarkdown('строка 1\nстрока 2'), '> строка 1\n> строка 2\n\n')
  assert.equal(formatQuoteMarkdown('   '), '')
})

test('Mermaid initializes strictly with securityLevel=strict and renders safe fallback during stream', () => {
  assert.deepEqual(getMermaidInitConfig('dark'), { startOnLoad: false, securityLevel: 'strict', theme: 'dark' })
  assert.deepEqual(getMermaidInitConfig('light'), { startOnLoad: false, securityLevel: 'strict', theme: 'default' })

  const html = renderToStaticMarkup(
    React.createElement(RichText, { text: '```mermaid\ngraph TD\n  A-->B\n```', streaming: true }),
  )
  assert.match(html, /mermaid-block/)
  assert.match(html, /graph TD/)
})

test('Voice input hides gracefully when SpeechRecognition is unavailable', () => {
  assert.equal(getSpeechRecognitionCtor(), null)
})

test('createChatFolder immediately adds empty folder to chatStore', async () => {
  const prevFolders = useChats.getState().folders
  const prevRemote = useChats.getState().remote
  useChats.setState({ remote: null, folders: [] })
  try {
    const folder = await createChatFolder('Пустой проект', '#10b981')
    assert.ok(folder)
    const folders = useChats.getState().folders
    assert.equal(folders.length, 1)
    assert.equal(folders[0]!.name, 'Пустой проект')
  } finally {
    useChats.setState({ remote: prevRemote, folders: prevFolders })
  }
})

test('ProjectSettingsModal renders dialog with systemPrompt and project name', () => {
  const html = renderToStaticMarkup(
    React.createElement(ProjectSettingsModal, {
      folder: {
        id: 'f-1',
        name: 'Мой проект',
        color: '#6366f1',
        sortOrder: 0,
        systemPrompt: 'Отвечай кратко',
        attachmentCount: 0,
        createdAt: 1000,
      },
      onClose: () => {},
    }),
  )
  assert.match(html, /role="dialog"/)
  assert.match(html, /Настройки проекта/)
  assert.match(html, /Мой проект/)
  assert.match(html, /Отвечай кратко/)
})
