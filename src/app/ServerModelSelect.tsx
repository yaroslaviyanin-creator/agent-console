import { RefreshCw } from 'lucide-react'
import type { ServerProvider } from '../protocol/types'
import { refreshServerModels, useChats } from './chatStore'
import { defaultReasoningForProvider, groupServerModels, pickVariantForGroup } from './modelCatalog'
import { SelectMenu } from './SelectMenu'

export function ServerModelSelect({ provider, model, reasoning, unavailable, disabled, placement = 'auto', onChangeModel, onChangeReasoning }: {
  provider?: ServerProvider
  model: string
  reasoning?: string | undefined
  unavailable: boolean
  disabled: boolean
  placement?: 'auto' | 'top' | 'bottom'
  onChangeModel: (id: string) => void
  onChangeReasoning: (reasoning: string) => void
}) {
  const { remote, serverModelsByProvider, modelsLoadingByProvider, modelsErrorByProvider } = useChats()
  const effectiveProvider: ServerProvider = provider ?? remote?.provider ?? 'stub'
  const serverModels = serverModelsByProvider[effectiveProvider] ?? []
  const modelsLoading = Boolean(modelsLoadingByProvider[effectiveProvider])
  const modelsError = modelsErrorByProvider[effectiveProvider] ?? ''
  const groups = groupServerModels(serverModels)
  const activeGroup = model === 'default'
    ? undefined
    : groups.find(g => g.variants.some(v => v.id === model))
  const currentVariant = activeGroup?.variants.find(v => v.id === model)
  const currentServerModel = serverModels.find(m => m.id === model)
  const nonVariantEfforts = activeGroup && !activeGroup.hasVariantReasoning
    ? defaultReasoningForProvider(effectiveProvider, currentServerModel)
    : model === 'default' && (effectiveProvider === 'codex' || effectiveProvider === 'claude')
      ? defaultReasoningForProvider(effectiveProvider)
      : []

  const modelValue = model === 'default'
    ? 'default'
    : activeGroup
      ? activeGroup.key
      : model

  const modelOptions = [
    { value: 'default', label: 'По умолчанию' },
    ...(unavailable ? [{ value: model, label: model, disabled: true }] : []),
    ...groups.map(group => ({ value: group.key, label: group.name })),
  ]

  return <div className="server-model-select">
    <div className="select-wrap catalog-select">
      <span>Модель</span>
      <SelectMenu
        ariaLabel="Модель"
        className={unavailable ? 'model-unavailable' : ''}
        value={modelValue}
        disabled={disabled}
        placement={placement}
        options={modelOptions}
        onChange={nextKey => {
          if (nextKey === 'default') {
            onChangeModel('default')
            return
          }
          const nextGroup = groups.find(g => g.key === nextKey)
          if (!nextGroup) return
          const chosen = pickVariantForGroup(nextGroup, currentVariant?.reasoning || reasoning)
          if (chosen) onChangeModel(chosen.id)
        }}
      />
    </div>
    <div className="select-wrap catalog-select">
      <span>Рассуждения</span>
      {activeGroup?.hasVariantReasoning ? (
        <SelectMenu
          ariaLabel="Рассуждения"
          value={model}
          disabled={disabled || unavailable || activeGroup.variants.length <= 1}
          placement={placement}
          options={activeGroup.variants.map(v => ({ value: v.id, label: v.reasoning }))}
          onChange={onChangeModel}
        />
      ) : (
        <SelectMenu
          ariaLabel="Рассуждения"
          value={nonVariantEfforts.length ? (reasoning ?? '') : ''}
          disabled={disabled || unavailable || nonVariantEfforts.length === 0}
          placement={placement}
          options={[{ value: '', label: 'По умолчанию' }, ...nonVariantEfforts]}
          onChange={onChangeReasoning}
        />
      )}
    </div>
    {effectiveProvider !== 'stub' && <button className="icon-btn models-refresh" type="button" disabled={modelsLoading}
      onClick={() => void refreshServerModels(effectiveProvider)} aria-label="Обновить список моделей" title="Обновить список моделей">
      <RefreshCw size={13}/>
    </button>}
    {(modelsLoading || modelsError || unavailable) && <small className="models-status" role="status">
      {modelsLoading ? 'Загружаю модели…' : modelsError || 'Выберите доступную модель.'}
    </small>}
  </div>
}
