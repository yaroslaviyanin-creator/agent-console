import { ChevronDown, RefreshCw } from 'lucide-react'
import { refreshServerModels, useChats } from './chatStore'

export function ServerModelSelect({ model, unavailable, disabled, onChange }: {
  model: string
  unavailable: boolean
  disabled: boolean
  onChange: (id: string) => void
}) {
  const { remote, serverModels, modelsLoading, modelsError } = useChats()
  return <div className="server-model-select">
    <label className="select-wrap catalog-select">
      <span>Модель</span>
      <select aria-label="Модель" value={model} disabled={disabled} onChange={e => onChange(e.currentTarget.value)}>
        <option value="default">По умолчанию</option>
        {unavailable && <option value={model} disabled>{model} · {modelsLoading ? 'загрузка' : 'недоступна'}</option>}
        {serverModels.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
      </select>
      <ChevronDown size={12}/>
    </label>
    {remote?.provider === 'codex' && <button className="icon-btn models-refresh" type="button" disabled={modelsLoading}
      onClick={() => void refreshServerModels()} aria-label="Обновить список моделей" title="Обновить список моделей">
      <RefreshCw size={13}/>
    </button>}
    {(modelsLoading || modelsError || unavailable) && <small className="models-status" role="status">
      {modelsLoading ? 'Загружаю модели…' : modelsError || 'Выберите доступную модель.'}
    </small>}
  </div>
}
