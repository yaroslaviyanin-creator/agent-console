import assert from 'node:assert/strict'
import { test } from 'node:test'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { ChatContextPanel, formatCompactTokens, formatUsageInline, formatUsageTooltip, summarizeChatContext } from '../src/app/ChatContextPanel'
import type { Turn } from '../src/app/chatStore'

test('summarizeChatContext aggregates multi-model and multi-provider token usage accurately', () => {
  const turns: Turn[] = [
    {
      id: 't1',
      prompt: 'Первый запрос',
      time: 1000,
      provider: 'codex',
      modelId: 'GPT-6.2-Sol',
      model: 'GPT-6.2-Sol',
      seq: 1,
      state: 'completed',
      parts: [{ kind: 'text', text: 'Ответ 1' }],
      usage: { inputTokens: 14200, cachedInputTokens: 8000, outputTokens: 420, reasoningTokens: 1200, totalTokens: 14620 },
    },
    {
      id: 't2',
      prompt: 'Второй запрос',
      time: 2000,
      provider: 'codex',
      modelId: 'GPT-6.2-Astra',
      model: 'GPT-6.2-Astra',
      seq: 1,
      state: 'completed',
      parts: [{ kind: 'text', text: 'Ответ 2' }],
      usage: { inputTokens: 800, cachedInputTokens: 0, outputTokens: 80, reasoningTokens: 0, totalTokens: 880 },
    },
    {
      id: 't3',
      prompt: 'Третий запрос',
      time: 3000,
      provider: 'claude',
      modelId: 'claude-sonnet',
      model: 'Claude Sonnet',
      seq: 1,
      state: 'completed',
      parts: [{ kind: 'text', text: 'Ответ 3' }],
      usage: { inputTokens: 1000, cachedInputTokens: 500, outputTokens: 200, reasoningTokens: 0, totalTokens: 1200 },
    },
  ]

  const summary = summarizeChatContext(turns, 'Claude', 'Claude Sonnet')
  assert.equal(summary.hasDetailedUsage, true)
  assert.equal(summary.inputTokens, 16000)
  assert.equal(summary.cachedInputTokens, 8500)
  assert.equal(summary.outputTokens, 700)
  assert.equal(summary.reasoningTokens, 1200)
  assert.equal(summary.totalTokens, 16700)
  assert.equal(summary.byProvider.length, 2)

  const codex = summary.byProvider.find(p => p.providerId === 'codex')!
  assert.equal(codex.count, 2)
  assert.equal(codex.models.length, 2)
  assert.equal(codex.inputTokens, 15000)
  assert.equal(codex.outputTokens, 500)
  assert.equal(codex.reasoningTokens, 1200)

  const claude = summary.byProvider.find(p => p.providerId === 'claude')!
  assert.equal(claude.count, 1)
  assert.equal(claude.models.length, 1)
  assert.equal(claude.inputTokens, 1000)
  assert.equal(claude.outputTokens, 200)

  const multiHtml = renderToStaticMarkup(createElement(ChatContextPanel, {
    turns,
    providerLabel: 'Claude',
    modelLabel: 'Claude Sonnet',
    remote: null,
    canAttach: false,
    onAttachClick: () => {},
    onClose: () => {},
  }))
  assert.match(multiHtml, /По провайдерам и моделям/)
  assert.match(multiHtml, /GPT-6\.2-Sol/)
  assert.match(multiHtml, /GPT-6\.2-Astra/)
  assert.match(multiHtml, /Обдумывание/)

  // Single provider & single model hides redundant nested breakdown
  const singleHtml = renderToStaticMarkup(createElement(ChatContextPanel, {
    turns: turns.slice(2, 3),
    providerLabel: 'Claude',
    modelLabel: 'Claude Sonnet',
    remote: null,
    canAttach: false,
    onAttachClick: () => {},
    onClose: () => {},
  }))
  assert.doesNotMatch(singleHtml, /По провайдерам и моделям/)
  assert.doesNotMatch(singleHtml, /Обдумывание/)
})

test('formatCompactTokens, formatUsageInline, and formatUsageTooltip format input/output and optional reasoning', () => {
  assert.equal(formatCompactTokens(420), '420')
  assert.equal(formatCompactTokens(14200), '14.2K')
  assert.equal(formatUsageInline({ inputTokens: 14200, cachedInputTokens: 8000, outputTokens: 420, reasoningTokens: 1200 }), '↑ 14.2K · ↓ 420 ток. · мысли 1.2K')
  assert.equal(formatUsageInline({ inputTokens: 1000, cachedInputTokens: 0, outputTokens: 50, reasoningTokens: 0 }), '↑ 1K · ↓ 50 ток.')

  const tip = formatUsageTooltip({ inputTokens: 14200, cachedInputTokens: 8000, outputTokens: 420, reasoningTokens: 1200 })
  assert.match(tip, /Вход:/)
  assert.match(tip, /кэш:/)
  assert.match(tip, /Выход:/)
  assert.match(tip, /Обдумывание:/)
})

test('ChatContextPanel merges default/empty modelId with explicit model name and renders compact empty sections', () => {
  const turns: Turn[] = [
    {
      id: 't1',
      prompt: 'Первый',
      time: 1000,
      provider: 'codex',
      model: 'Codex',
      seq: 1,
      state: 'completed',
      parts: [{ kind: 'text', text: 'Ок' }],
      usage: { inputTokens: 100, cachedInputTokens: 0, outputTokens: 20, totalTokens: 120 },
    },
    {
      id: 't2',
      prompt: 'Второй',
      time: 2000,
      provider: 'codex',
      modelId: 'gpt-6.2-sol',
      model: 'GPT-6.2-Sol',
      seq: 2,
      state: 'completed',
      parts: [{ kind: 'text', text: 'Ок 2' }],
      usage: { inputTokens: 200, cachedInputTokens: 0, outputTokens: 40, totalTokens: 240 },
    },
  ]
  const catalog = {
    codex: [
      { id: 'gpt-6.2-sol', name: 'GPT-6.2-Sol', isDefault: true },
      { id: 'gpt-6.2-astra', name: 'GPT-6.2-Astra', isDefault: false },
    ],
  }
  const summary = summarizeChatContext(turns, 'Codex', 'GPT-6.2-Sol', catalog)
  assert.equal(summary.byProvider.length, 1)
  assert.equal(summary.byProvider[0]?.models.length, 1)
  assert.equal(summary.byProvider[0]?.models[0]?.modelLabel, 'GPT-6.2-Sol')
  assert.equal(summary.byProvider[0]?.models[0]?.count, 2)

  const emptyHtml = renderToStaticMarkup(createElement(ChatContextPanel, {
    turns: [],
    providerLabel: 'Codex',
    modelLabel: 'GPT-6.2-Sol',
    serverModelsByProvider: catalog,
    remote: null,
    canAttach: true,
    onAttachClick: () => {},
    onClose: () => {},
  }))
  assert.match(emptyHtml, /context-section is-empty/)
  assert.doesNotMatch(emptyHtml, /В этом чате пока нет файлов/)

  // Deduplicates provider when old turns omit provider/modelId and new turns set 'codex' / 'gpt-6.2-sol'
  const mixedTurns: Turn[] = [
    {
      id: 'legacy-1',
      prompt: 'Старый запрос',
      time: 1000,
      model: 'Codex',
      seq: 1,
      state: 'completed',
      parts: [{ kind: 'text', text: 'Ответ 1' }],
      usage: { inputTokens: 100, cachedInputTokens: 0, outputTokens: 20, totalTokens: 120 },
    },
    {
      id: 'new-1',
      prompt: 'Новый запрос',
      time: 2000,
      provider: 'codex',
      modelId: 'gpt-6.2-sol',
      model: 'GPT-6.2-Sol',
      seq: 2,
      state: 'completed',
      parts: [{ kind: 'text', text: 'Ответ 2' }],
      usage: { inputTokens: 300, cachedInputTokens: 0, outputTokens: 60, totalTokens: 360 },
    },
  ]
  const dedupSummary = summarizeChatContext(mixedTurns, 'Codex', 'GPT-6.2-Sol', catalog, 'codex')
  assert.equal(dedupSummary.byProvider.length, 1)
  assert.equal(dedupSummary.byProvider[0]?.providerId, 'codex')
  assert.equal(dedupSummary.byProvider[0]?.count, 2)
  assert.equal(dedupSummary.byProvider[0]?.models.length, 1)
  assert.equal(dedupSummary.byProvider[0]?.models[0]?.modelLabel, 'GPT-6.2-Sol')
})
