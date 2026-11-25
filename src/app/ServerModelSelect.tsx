import { ChevronDown, RefreshCw } from 'lucide-react'
import { refreshServerModels, useChats } from './chatStore'
import { defaultReasoningForProvider, groupServerModels, pickVariantForGroup } from './modelCatalog'

export function ServerModelSelect({ model, reasoning, unavailable, disabled, onChangeModel, onChangeReasoning }: {
  model: string
  reasoning?: string | undefined
  unavailable: boolean
  disabled: boolean
  onChangeModel: (id: string) => void
  onChangeReasoning: (reasoning: string) => void
}) {
  const { remote, serverModels, modelsLoading, modelsError } = useChats()
  const groups = groupServerModels(serverModels)
  const activeGroup = model === 'default'
    ? undefined
    : groups.find(g => g.variants.some(v => v.id === model))
  const currentVariant = activeGroup?.variants.find(v => v.id === model)
  const currentServerModel = serverModels.find(m => m.id === model)
  const nonVariantEfforts = activeGroup && !activeGroup.hasVariantReasoning && remote
    ? defaultReasoningForProvider(remote.provider, currentServerModel)
    : model === 'default' && remote?.provider === 'codex'
      ? defaultReasoningForProvider('codex')
      : []

  const modelValue = model === 'default'
    ? 'default'
    : activeGroup
      ? activeGroup.key
      : model

  return <div className="server-model-select">
    <label className="select-wrap catalog-select">
      <span>Модель</span>
      <select aria-label="Модель" value={modelValue} disabled={disabled} onChange={e => {
        const nextKey = e.currentTarget.value
        if (nextKey === 'default') {
          onChangeModel('default')
          return
        }
        const nextGroup = groups.find(g => g.key === nextKey)
        if (!nextGroup) return
        const chosen = pickVariantForGroup(nextGroup, currentVariant?.reasoning || reasoning)
        if (chosen) onChangeModel(chosen.id)
      }}>
        <option value="default">По умолчанию</option>
        {unavailable && <option value={model} disabled>{model} · {modelsLoading ? 'загрузка' : 'недоступна'}</option>}
        {groups.map(group => <option key={group.key} value={group.key}>{group.name}</option>)}
      </select>
      <ChevronDown size={12}/>
    </label>
    <label className="select-wrap catalog-select">
      <span>Рассуждения</span>
      {activeGroup?.hasVariantReasoning ? (
        <select
          aria-label="Рассуждения"
          value={model}
          disabled={disabled || unavailable || activeGroup.variants.length <= 1}
          onChange={e => onChangeModel(e.currentTarget.value)}
        >
          {activeGroup.variants.map(v => <option key={v.id} value={v.id}>{v.reasoning}</option>)}
        </select>
      ) : (
        <select
          aria-label="Рассуждения"
          value={nonVariantEfforts.length ? (reasoning ?? '') : ''}
          disabled={disabled || unavailable || nonVariantEfforts.length === 0}
          onChange={e => onChangeReasoning(e.currentTarget.value)}
        >
          <option value="">По умолчанию</option>
          {nonVariantEfforts.map(opt => <option key={opt.value} value={opt.value}>{opt.label}</option>)}
        </select>
      )}
      <ChevronDown size={12}/>
    </label>
    {(remote?.provider === 'codex' || remote?.provider === 'gemini' || remote?.provider === 'antigravity') && <button className="icon-btn models-refresh" type="button" disabled={modelsLoading}
      onClick={() => void refreshServerModels()} aria-label="Обновить список моделей" title="Обновить список моделей">
      <RefreshCw size={13}/>
    </button>}
    {(modelsLoading || modelsError || unavailable) && <small className="models-status" role="status">
      {modelsLoading ? 'Загружаю модели…' : modelsError || 'Выберите доступную модель.'}
    </small>}
  </div>
}
