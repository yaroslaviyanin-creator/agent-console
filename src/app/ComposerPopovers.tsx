import { Check, ChevronLeft, ChevronRight, Laptop, RefreshCw, RotateCcw, Sparkles, Terminal, Zap } from 'lucide-react'
import { useState } from 'react'
import type { Device, ServerProvider } from '../protocol/types'
import { refreshServerModels, useChats } from './chatStore'
import { defaultReasoningForProvider, groupServerModels, pickVariantForGroup } from './modelCatalog'
import type { ProviderOption } from './ProviderSelect'

export interface ReasoningStep {
  value: string
  label: string
}

function SteppedSlider({
  steps,
  value,
  disabled,
  onChange,
}: {
  steps: readonly ReasoningStep[]
  value: string
  disabled: boolean
  onChange: (value: string) => void
}) {
  if (steps.length === 0) return null
  const rawIndex = steps.findIndex(s => s.value === value)
  const activeIndex = rawIndex >= 0 ? rawIndex : 0
  const maxIndex = Math.max(1, steps.length - 1)
  const ratio = steps.length <= 1 ? 1 : activeIndex / maxIndex
  const thumbLeft = steps.length <= 1 ? '50%' : `calc(15px + (100% - 30px) * ${ratio})`
  const fillWidth = steps.length <= 1 ? '100%' : `calc(30px + (100% - 30px) * ${ratio})`

  return (
    <div className="reasoning-slider-wrap" data-disabled={disabled || steps.length <= 1}>
      <div className="reasoning-track">
        <div className="reasoning-fill" style={{ width: fillWidth }} />
        <div className="reasoning-dots" aria-hidden="true">
          {steps.map((step, idx) => (
            <span
              key={step.value || 'default'}
              className={`reasoning-dot${idx <= activeIndex ? ' is-active' : ''}`}
              style={{ left: steps.length <= 1 ? '50%' : `${(idx / maxIndex) * 100}%` }}
            />
          ))}
        </div>
        <div
          className="reasoning-thumb"
          style={{ left: thumbLeft }}
          aria-hidden="true"
        />
        <input
          type="range"
          className="reasoning-range"
          min={0}
          max={steps.length - 1}
          step={1}
          value={activeIndex}
          disabled={disabled || steps.length <= 1}
          aria-label="Уровень рассуждений"
          aria-valuetext={steps[activeIndex]?.label ?? ''}
          onChange={e => {
            const next = steps[Number(e.target.value)]
            if (next) onChange(next.value)
          }}
        />
      </div>
      <div className="reasoning-scale" aria-hidden="true">
        <span>{steps[0]?.label}</span>
        {steps.length > 1 && <span>{steps[steps.length - 1]?.label}</span>}
      </div>
    </div>
  )
}

export interface DemoModelGroup {
  name: string
  variants: readonly { id: string; label: string; shortLabel: string; provider: string; reasoning?: string }[]
}

export function ModelPopover({
  remoteMode,
  provider,
  providerOptions,
  model,
  reasoning,
  unavailableModel,
  disabled,
  canChangeProvider = true,
  providerDisabled = false,
  demoModelGroups,
  activeDemoGroup,
  hasDemoVariantReasoning,
  demoReasoningOptions,
  onSelectProvider,
  onSelectModel,
  onSelectReasoning,
}: {
  remoteMode: boolean
  provider: string
  providerOptions: readonly ProviderOption[]
  model: string
  reasoning?: string | undefined
  unavailableModel: boolean
  disabled: boolean
  canChangeProvider?: boolean
  providerDisabled?: boolean
  demoModelGroups: readonly DemoModelGroup[]
  activeDemoGroup: DemoModelGroup | undefined
  hasDemoVariantReasoning: boolean
  demoReasoningOptions: readonly { value: string; label: string }[]
  onSelectProvider: (providerId: string) => void
  onSelectModel: (modelId: string) => void
  onSelectReasoning: (reasoning: string) => void
}) {
  const [subview, setSubview] = useState<'main' | 'provider' | 'model'>('main')
  const serverProvider = provider as ServerProvider
  const serverModels = useChats(s => s.serverModelsByProvider[serverProvider]) ?? []
  const modelsLoading = useChats(s => Boolean(s.modelsLoadingByProvider[serverProvider]))
  const modelsError = useChats(s => s.modelsErrorByProvider[serverProvider] ?? '')

  const groups = remoteMode ? groupServerModels(serverModels) : []
  const activeGroup = remoteMode && model !== 'default'
    ? groups.find(g => g.variants.some(v => v.id === model))
    : undefined
  const currentVariant = activeGroup?.variants.find(v => v.id === model)
  const currentServerModel = remoteMode ? serverModels.find(m => m.id === model) : undefined
  const nonVariantEfforts = remoteMode && activeGroup && !activeGroup.hasVariantReasoning
    ? defaultReasoningForProvider(serverProvider, currentServerModel)
    : remoteMode && model === 'default' && (serverProvider === 'codex' || serverProvider === 'claude')
      ? defaultReasoningForProvider(serverProvider)
      : []

  const reasoningSteps: ReasoningStep[] = remoteMode
    ? activeGroup?.hasVariantReasoning
      ? activeGroup.variants.map(v => ({ value: v.id, label: v.reasoning }))
      : nonVariantEfforts.length > 0
        ? [{ value: '', label: 'По умолчанию' }, ...nonVariantEfforts]
        : []
    : hasDemoVariantReasoning
      ? (activeDemoGroup?.variants ?? []).map(v => ({
          value: v.id,
          label: v.reasoning ?? v.shortLabel,
        }))
      : demoReasoningOptions.length > 0
        ? [{ value: '', label: 'По умолчанию' }, ...demoReasoningOptions]
        : []

  const reasoningValue = remoteMode
    ? activeGroup?.hasVariantReasoning ? model : (reasoning ?? '')
    : hasDemoVariantReasoning ? model : (reasoning ?? '')

  const activeReasoningLabel = reasoningSteps.find(s => s.value === reasoningValue)?.label
    ?? reasoningSteps[0]?.label
    ?? 'Без настройки'

  const providerLabel = providerOptions.find(p => p.id === provider)?.label ?? provider
  const modelLabel = remoteMode
    ? model === 'default'
      ? 'По умолчанию'
      : activeGroup?.name ?? model
    : activeDemoGroup?.name ?? model

  const canResetReasoning = !disabled && reasoningSteps.length > 1 && reasoningValue !== reasoningSteps[0]?.value

  const handleReasoningChange = (nextVal: string) => {
    if (remoteMode) {
      if (activeGroup?.hasVariantReasoning) onSelectModel(nextVal)
      else onSelectReasoning(nextVal)
    } else {
      if (hasDemoVariantReasoning) onSelectModel(nextVal)
      else onSelectReasoning(nextVal)
    }
  }

  return (
    <div className="composer-popover model-popover" role="dialog" aria-label="Модель, провайдер и рассуждения">
      {subview === 'main' && (
        <>
          <div className="popover-codex-head">
            <span className="popover-head-icon" aria-hidden="true"><Zap size={14} /></span>
            <span className="popover-reasoning-title">{activeReasoningLabel}</span>
            {remoteMode && serverProvider !== 'stub' && !providerDisabled ? (
              <button
                type="button"
                className="popover-head-action"
                disabled={modelsLoading}
                onClick={() => void refreshServerModels(serverProvider)}
                aria-label="Обновить список моделей"
                title="Обновить список моделей"
              >
                <RefreshCw size={13} className={modelsLoading ? 'spin' : undefined} />
              </button>
            ) : (
              <button
                type="button"
                className="popover-head-action"
                disabled={!canResetReasoning}
                onClick={() => { if (reasoningSteps[0]) handleReasoningChange(reasoningSteps[0].value) }}
                aria-label="Сбросить рассуждения"
                title="Сбросить рассуждения"
              >
                <RotateCcw size={13} />
              </button>
            )}
          </div>

          <div className="popover-nav-group">
            <button
              type="button"
              className="popover-nav-row"
              disabled={disabled || !canChangeProvider || !providerOptions.some(p => p.available && p.id !== provider)}
              onClick={() => setSubview('provider')}
            >
              <span className="popover-nav-kicker">Провайдер</span>
              <span className={`popover-nav-val${providerDisabled ? ' is-muted' : ''}`}>{providerLabel}</span>
              <ChevronRight size={14} className="popover-nav-chevron" />
            </button>
            <button
              type="button"
              className="popover-nav-row"
              disabled={disabled || providerDisabled}
              onClick={() => setSubview('model')}
            >
              <span className="popover-nav-kicker">Модель</span>
              <span className={`popover-nav-val${unavailableModel ? ' is-muted' : ''}`}>{modelLabel}</span>
              <ChevronRight size={14} className="popover-nav-chevron" />
            </button>
          </div>

          {(providerDisabled || modelsLoading || modelsError || unavailableModel) && (
            <p className="popover-status-note" role="status">
              {providerDisabled
                ? 'Провайдер недоступен на сервере.'
                : modelsLoading
                  ? 'Загружаю модели…'
                  : modelsError || 'Выберите доступную модель.'}
            </p>
          )}

          {reasoningSteps.length > 0 ? (
            <SteppedSlider
              steps={reasoningSteps}
              value={reasoningValue}
              disabled={disabled || providerDisabled || unavailableModel}
              onChange={handleReasoningChange}
            />
          ) : (
            <p className="popover-slider-empty">Уровень рассуждений не настраивается</p>
          )}
        </>
      )}

      {subview === 'provider' && (
        <div className="popover-subview">
          <button type="button" className="popover-back" onClick={() => setSubview('main')}>
            <ChevronLeft size={14} />
            <span>Провайдер</span>
          </button>
          <div className="popover-list" role="listbox" aria-label="Провайдер">
            {providerOptions.map(opt => (
              <button
                key={opt.id}
                type="button"
                role="option"
                aria-selected={opt.id === provider}
                disabled={disabled || !canChangeProvider || !opt.available}
                className={`popover-list-item${opt.id === provider ? ' is-active' : ''}`}
                onClick={() => {
                  if (opt.id !== provider) onSelectProvider(opt.id)
                  setSubview('main')
                }}
              >
                <span>{opt.label}</span>
                {opt.id === provider && <Check size={14} />}
              </button>
            ))}
          </div>
        </div>
      )}

      {subview === 'model' && (
        <div className="popover-subview">
          <button type="button" className="popover-back" onClick={() => setSubview('main')}>
            <ChevronLeft size={14} />
            <span>Модель · {providerLabel}</span>
          </button>
          <div className="popover-list" role="listbox" aria-label="Модель">
            {remoteMode ? (
              <>
                <button
                  type="button"
                  role="option"
                  aria-selected={model === 'default'}
                  disabled={disabled}
                  className={`popover-list-item${model === 'default' ? ' is-active' : ''}`}
                  onClick={() => { onSelectModel('default'); setSubview('main') }}
                >
                  <span>По умолчанию</span>
                  {model === 'default' && <Check size={14} />}
                </button>
                {unavailableModel && (
                  <button type="button" role="option" aria-selected={true} disabled className="popover-list-item is-active">
                    <span>{model}</span>
                    <Check size={14} />
                  </button>
                )}
                {groups.map(group => {
                  const selected = activeGroup?.key === group.key
                  return (
                    <button
                      key={group.key}
                      type="button"
                      role="option"
                      aria-selected={selected}
                      disabled={disabled}
                      className={`popover-list-item${selected ? ' is-active' : ''}`}
                      onClick={() => {
                        const chosen = pickVariantForGroup(group, currentVariant?.reasoning || reasoning)
                        if (chosen) onSelectModel(chosen.id)
                        setSubview('main')
                      }}
                    >
                      <span>{group.name}</span>
                      {selected && <Check size={14} />}
                    </button>
                  )
                })}
              </>
            ) : (
              demoModelGroups.map(group => {
                const selected = activeDemoGroup?.name === group.name
                return (
                  <button
                    key={group.name}
                    type="button"
                    role="option"
                    aria-selected={selected}
                    disabled={disabled}
                    className={`popover-list-item${selected ? ' is-active' : ''}`}
                    onClick={() => {
                      const currentVariantObj = activeDemoGroup?.variants.find(v => v.id === model)
                      const currentLevel = currentVariantObj?.reasoning
                      const matched = currentLevel
                        ? group.variants.find(v => v.reasoning === currentLevel)
                        : undefined
                      const chosen = matched ?? group.variants[0]
                      if (chosen) onSelectModel(chosen.id)
                      setSubview('main')
                    }}
                  >
                    <span>{group.name}</span>
                    {selected && <Check size={14} />}
                  </button>
                )
              })
            )}
          </div>
        </div>
      )}
    </div>
  )
}

export function EnvironmentPopover({
  remoteMode,
  mode,
  deviceId,
  devices,
  ownerId,
  disabled,
  mcpExamples,
  onSelectMode,
  onSelectDevice,
  onPickExample,
}: {
  remoteMode: boolean
  mode: 'chat' | 'mcp'
  deviceId: string
  devices: readonly Device[]
  ownerId: string
  disabled: boolean
  mcpExamples: readonly (readonly [string, string])[]
  onSelectMode: (mode: 'chat' | 'mcp') => void
  onSelectDevice: (deviceId: string) => void
  onPickExample: (prompt: string) => void
}) {
  const [subview, setSubview] = useState<'main' | 'device' | 'examples'>('main')
  const activeDevice = devices.find(d => d.id === deviceId) ?? devices[0]

  return (
    <div className="composer-popover env-popover" role="dialog" aria-label="Режим, устройства и сценарии MCP">
      {subview === 'main' && (
        <>
          <div className="popover-section-label">Режим работы</div>
          {remoteMode ? (
            <div className="popover-static-mode">
              <Terminal size={13} />
              <span>Только чат</span>
              <small>Серверный режим</small>
            </div>
          ) : (
            <div className="popover-mode-switch" role="group" aria-label="Режим работы">
              <button
                type="button"
                aria-pressed={mode === 'chat'}
                disabled={disabled}
                onClick={() => onSelectMode('chat')}
              >
                Чат
              </button>
              <button
                type="button"
                aria-pressed={mode === 'mcp'}
                disabled={disabled}
                onClick={() => onSelectMode('mcp')}
              >
                Управление ПК (MCP)
              </button>
            </div>
          )}

          {mode === 'mcp' && !remoteMode && (
            <div className="popover-nav-group">
              <button
                type="button"
                className="popover-nav-row"
                disabled={disabled}
                onClick={() => setSubview('device')}
              >
                <Laptop size={14} className="popover-nav-icon" />
                <span className="popover-nav-kicker">Устройство</span>
                <span className="popover-nav-val">
                  <i className={`env-dot ${activeDevice?.online ? 'is-online' : 'is-offline'}`} />
                  {activeDevice ? activeDevice.name : 'Не выбрано'}
                </span>
                <ChevronRight size={14} className="popover-nav-chevron" />
              </button>
              <button
                type="button"
                className="popover-nav-row"
                disabled={disabled}
                onClick={() => setSubview('examples')}
              >
                <Sparkles size={14} className="popover-nav-icon" />
                <span className="popover-nav-kicker">Сценарии MCP</span>
                <span className="popover-nav-val">{mcpExamples.length} примеров</span>
                <ChevronRight size={14} className="popover-nav-chevron" />
              </button>
            </div>
          )}
        </>
      )}

      {subview === 'device' && (
        <div className="popover-subview">
          <button type="button" className="popover-back" onClick={() => setSubview('main')}>
            <ChevronLeft size={14} />
            <span>Целевое устройство</span>
          </button>
          <div className="popover-list" role="listbox" aria-label="Целевое устройство">
            {devices.map(d => (
              <button
                key={d.id}
                type="button"
                role="option"
                aria-selected={d.id === deviceId}
                disabled={disabled || !d.online}
                className={`popover-list-item${d.id === deviceId ? ' is-active' : ''}`}
                onClick={() => { onSelectDevice(d.id); setSubview('main') }}
              >
                <span className="popover-device-item">
                  <i className={`env-dot ${d.online ? 'is-online' : 'is-offline'}`} />
                  <span>{d.name}</span>
                  <small>{d.ownerId !== ownerId ? 'ПК владельца · ' : ''}{d.online ? 'онлайн' : 'офлайн'}</small>
                </span>
                {d.id === deviceId && <Check size={14} />}
              </button>
            ))}
          </div>
        </div>
      )}

      {subview === 'examples' && (
        <div className="popover-subview">
          <button type="button" className="popover-back" onClick={() => setSubview('main')}>
            <ChevronLeft size={14} />
            <span>Сценарии MCP</span>
          </button>
          <div className="popover-list" role="listbox" aria-label="Сценарии MCP">
            {mcpExamples.map(([name, prompt]) => (
              <button
                key={name}
                type="button"
                role="option"
                aria-selected={false}
                disabled={disabled}
                className="popover-list-item"
                onClick={() => onPickExample(prompt)}
              >
                <span>{name}</span>
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
