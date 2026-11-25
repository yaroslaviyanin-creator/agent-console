import assert from 'node:assert/strict'
import { test } from 'node:test'
import { defaultReasoningForProvider, groupServerModels, pickVariantForGroup, splitModelReasoning } from '../src/app/modelCatalog'
import type { ServerModel } from '../src/protocol/types'

test('splitModelReasoning extracts known reasoning suffixes without altering base names', () => {
  assert.deepEqual(splitModelReasoning('Gemini 4 Hydrogen (High)'), { baseName: 'Gemini 4 Hydrogen', reasoning: 'High' })
  assert.deepEqual(splitModelReasoning('Claude Sonnet 4.6 (Thinking)'), { baseName: 'Claude Sonnet 4.6', reasoning: 'Thinking' })
  assert.deepEqual(splitModelReasoning('GPT-5.2-Codex'), { baseName: 'GPT-5.2-Codex', reasoning: null })
})

test('groupServerModels groups Antigravity variants by base model and sorts reasoning levels', () => {
  const models: ServerModel[] = [
    { id: 'gemini-4-hydrogen-high', name: 'Gemini 4 Hydrogen (High)', isDefault: false },
    { id: 'gemini-4-hydrogen-xhigh', name: 'Gemini 4 Hydrogen (XHigh)', isDefault: false },
    { id: 'gemini-4-hydrogen-medium', name: 'Gemini 4 Hydrogen (Medium)', isDefault: false },
    { id: 'gemini-4-hydrogen-low', name: 'Gemini 4 Hydrogen (Low)', isDefault: false },
    { id: 'gemini-3.8-flash-high', name: 'Gemini 3.8 Flash (High)', isDefault: true },
    { id: 'gemini-3.8-flash-low', name: 'Gemini 3.8 Flash (Low)', isDefault: false },
    { id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6 (Thinking)', isDefault: false },
  ]
  const groups = groupServerModels(models)
  assert.deepEqual(groups.map(g => g.name), ['Gemini 4 Hydrogen', 'Gemini 3.8 Flash', 'Claude Sonnet 4.6'])
  assert.deepEqual(groups[0]?.variants.map(v => v.reasoning), ['Low', 'Medium', 'High', 'XHigh'])
  assert.equal(pickVariantForGroup(groups[1]!, 'High')?.id, 'gemini-3.8-flash-high')
  assert.equal(pickVariantForGroup(groups[1]!, 'XHigh')?.id, 'gemini-3.8-flash-high')
})

test('defaultReasoningForProvider provides reasoning options for codex and claude', () => {
  assert.deepEqual(defaultReasoningForProvider('codex').map(o => o.value), ['low', 'medium', 'high', 'xhigh'])
  assert.deepEqual(defaultReasoningForProvider('claude').map(o => o.value), ['low', 'medium', 'high', 'thinking'])
  assert.deepEqual(defaultReasoningForProvider('paid_api'), [])
})
