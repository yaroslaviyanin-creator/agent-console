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
