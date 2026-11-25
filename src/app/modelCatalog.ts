import type { Provider, ServerModel } from '../protocol/types'

const REASONING_SUFFIX = /^(.*?)\s*\(([^()]+)\)\s*$/
const KNOWN_REASONING = new Set([
  'none', 'off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'thinking', 'fast', 'standard', 'default',
])
const REASONING_ORDER = ['none', 'off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'fast', 'standard', 'thinking', 'default']

export interface ReasoningOption {
  value: string
  label: string
}

export interface ModelVariant {
  id: string
  reasoning: string
  isDefault: boolean
}

export interface ServerModelGroup {
  key: string
  name: string
  variants: ModelVariant[]
  hasVariantReasoning: boolean
}

export function formatReasoningLabel(value: string): string {
  const lower = value.trim().toLowerCase()
  if (!lower || lower === 'default') return 'По умолчанию'
  if (lower === 'xhigh') return 'XHigh'
  return lower.charAt(0).toUpperCase() + lower.slice(1)
}

function reasoningRank(label: string): number {
  const idx = REASONING_ORDER.indexOf(label.trim().toLowerCase())
  return idx === -1 ? REASONING_ORDER.length : idx
}

export function splitModelReasoning(name: string): { baseName: string; reasoning: string | null } {
  const match = REASONING_SUFFIX.exec(name.trim())
  if (!match || !match[1] || !match[2]) return { baseName: name.trim(), reasoning: null }
  const candidate = match[2].trim()
  if (!KNOWN_REASONING.has(candidate.toLowerCase())) {
    return { baseName: name.trim(), reasoning: null }
  }
  return { baseName: match[1].trim(), reasoning: candidate }
}

export function groupServerModels(items: ServerModel[]): ServerModelGroup[] {
  const groups = new Map<string, ServerModelGroup>()
  for (const item of items) {
    const { baseName, reasoning } = splitModelReasoning(item.name)
    const key = reasoning ? baseName : item.id
    let group = groups.get(key)
    if (!group) {
      group = { key, name: baseName, variants: [], hasVariantReasoning: Boolean(reasoning) }
      groups.set(key, group)
    }
    group.variants.push({
      id: item.id,
      reasoning: reasoning ?? '',
      isDefault: item.isDefault,
    })
  }
  for (const group of groups.values()) {
    if (group.hasVariantReasoning) {
      group.variants.sort((a, b) => reasoningRank(a.reasoning) - reasoningRank(b.reasoning))
    }
  }
  return Array.from(groups.values())
}

export function pickVariantForGroup(group: ServerModelGroup, preferredReasoning?: string): ModelVariant | undefined {
  if (preferredReasoning) {
    const target = preferredReasoning.trim().toLowerCase()
    const exact = group.variants.find(v => v.reasoning.toLowerCase() === target)
    if (exact) return exact
  }
  return (
    group.variants.find(v => v.isDefault) ??
    group.variants.find(v => v.reasoning.toLowerCase() === 'medium') ??
    group.variants.find(v => v.reasoning.toLowerCase() === 'high') ??
    group.variants[0]
  )
}

export function defaultReasoningForProvider(provider: Provider | 'stub', serverModel?: ServerModel): ReasoningOption[] {
  if (serverModel?.reasoningEfforts?.length) {
    return serverModel.reasoningEfforts.map(value => ({ value, label: formatReasoningLabel(value) }))
  }
  if (provider === 'codex') {
    return ['low', 'medium', 'high', 'xhigh'].map(value => ({ value, label: formatReasoningLabel(value) }))
  }
  if (provider === 'claude') {
    return ['low', 'medium', 'high', 'thinking'].map(value => ({ value, label: formatReasoningLabel(value) }))
  }
  return []
}
