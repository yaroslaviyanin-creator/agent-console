import { useEffect, useState } from 'react'
import { Check, Plus, Trash2, Zap } from 'lucide-react'
import type { PaidAPIConfig, PaidAPIModel, ServerAdminAdapter } from '../protocol/types'

const PRESETS: readonly { name: string; baseUrl: string; models: PaidAPIModel[] }[] = [
  {
    name: 'DeepSeek',
    baseUrl: 'https://api.deepseek.com/v1',
    models: [
      { id: 'deepseek-chat', label: 'DeepSeek V3', inputPricePer1M: 0.27, outputPricePer1M: 1.1, supportsVision: false, supportsReasoning: false },
      { id: 'deepseek-reasoner', label: 'DeepSeek R1', inputPricePer1M: 0.55, outputPricePer1M: 2.19, supportsVision: false, supportsReasoning: true },
    ],
  },
  {
    name: 'OpenRouter',
    baseUrl: 'https://openrouter.ai/api/v1',
    models: [
      { id: 'anthropic/claude-sonnet-4', label: 'Claude Sonnet 4', inputPricePer1M: 3, outputPricePer1M: 15, supportsVision: true, supportsReasoning: true },
    ],
  },
]

const emptyModel = (): PaidAPIModel => ({
  id: '',
  label: '',
  inputPricePer1M: 0,
  outputPricePer1M: 0,
  supportsVision: false,
  supportsReasoning: false,
})

export function AdminAPIProvidersPanel({ admin }: { admin: ServerAdminAdapter }) {
  const [items, setItems] = useState<PaidAPIConfig[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [status, setStatus] = useState('')
  const [testingId, setTestingId] = useState<string | null>(null)

  const [editingId, setEditingId] = useState<string | null>(null)
  const [name, setName] = useState('')
  const [baseUrl, setBaseUrl] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [enabled, setEnabled] = useState(true)
  const [models, setModels] = useState<PaidAPIModel[]>([emptyModel()])

  async function reload() {
    if (!admin.apiProviders) {
      setLoading(false)
      return
    }
    try {
      setItems(await admin.apiProviders())
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Не удалось загрузить API-провайдеров.')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void reload()
  }, [admin])

  function resetForm() {
    setEditingId(null)
    setName('')
    setBaseUrl('')
    setApiKey('')
    setEnabled(true)
    setModels([emptyModel()])
  }

  function applyPreset(preset: (typeof PRESETS)[number]) {
    setEditingId(null)
    setName(preset.name)
    setBaseUrl(preset.baseUrl)
    setModels(preset.models.map(m => ({ ...m })))
    setEnabled(true)
    setError('')
    setStatus('')
  }

  function startEdit(item: PaidAPIConfig) {
    setEditingId(item.id)
    setName(item.name)
    setBaseUrl(item.baseUrl)
    setApiKey('')
    setEnabled(item.enabled)
    setModels(item.models.length > 0 ? item.models.map(m => ({ ...m })) : [emptyModel()])
    setError('')
    setStatus('')
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!admin.createAPIProvider || !admin.updateAPIProvider) return
    const cleanModels = models.filter(m => m.id.trim() !== '').map(m => ({
      ...m,
      id: m.id.trim(),
      label: m.label.trim() || m.id.trim(),
    }))
    setBusy(true)
    setError('')
    setStatus('')
    try {
      if (editingId) {
        await admin.updateAPIProvider(editingId, {
          name: name.trim(),
          baseUrl: baseUrl.trim(),
          ...(apiKey.trim() ? { apiKey: apiKey.trim() } : {}),
          models: cleanModels,
          enabled,
        })
        setStatus('Провайдер обновлён.')
      } else {
        await admin.createAPIProvider({
          name: name.trim(),
          baseUrl: baseUrl.trim(),
          apiKey: apiKey.trim(),
          models: cleanModels,
          enabled,
        })
        setStatus('Провайдер добавлен.')
      }
      resetForm()
      await reload()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Ошибка сохранения провайдера.')
    } finally {
      setBusy(false)
    }
  }

  async function handleTest(id: string) {
    if (!admin.testAPIProvider) return
    setTestingId(id)
    setError('')
    setStatus('')
    try {
      await admin.testAPIProvider(id)
      setStatus('Соединение и ключ проверены успешно.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Проверка ключа не удалась.')
    } finally {
      setTestingId(null)
    }
  }

  async function handleDelete(id: string) {
    if (!admin.deleteAPIProvider) return
    setBusy(true)
    setError('')
    setStatus('')
    try {
      await admin.deleteAPIProvider(id)
      if (editingId === id) resetForm()
      setStatus('Провайдер удалён.')
      await reload()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Не удалось удалить провайдера.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="api-providers-panel">
      <div className="api-presets-row">
        <span>Пресеты:</span>
        {PRESETS.map(p => (
          <button key={p.name} type="button" className="alt-action" onClick={() => applyPreset(p)}>
            {p.name}
          </button>
        ))}
      </div>

      {loading ? (
        <p>Загрузка…</p>
      ) : items.length > 0 && (
        <ul className="api-providers-list">
          {items.map(item => (
            <li key={item.id} className="api-provider-card">
              <div className="api-provider-meta">
                <strong>{item.name}</strong>
                <code>{item.baseUrl}</code>
                <small>
                  Ключ: {item.apiKeyMask || 'не задан'} · Моделей: {item.models.length} · {item.enabled ? 'Вкл' : 'Выкл'}
                </small>
              </div>
              <div className="api-provider-actions">
                <button
                  type="button"
                  className="alt-action"
                  disabled={testingId === item.id}
                  onClick={() => void handleTest(item.id)}
                >
                  <Zap size={12} />
                  <span>{testingId === item.id ? 'Проверка…' : 'Проверить ключ'}</span>
                </button>
                <button type="button" className="alt-action" onClick={() => startEdit(item)}>Изменить</button>
                <button
                  type="button"
                  className="icon-btn"
                  aria-label={`Удалить ${item.name}`}
                  onClick={() => void handleDelete(item.id)}
                >
                  <Trash2 size={13} />
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}

      <form className="api-provider-form" onSubmit={e => void handleSubmit(e)}>
        <h4>{editingId ? 'Редактировать провайдера' : 'Добавить OpenAI-compatible API'}</h4>
        <div className="api-provider-grid">
          <input
            type="text"
            placeholder="Название (DeepSeek, OpenRouter)"
            value={name}
            required
            onChange={e => setName(e.target.value)}
          />
          <input
            type="url"
            placeholder="https://api.deepseek.com/v1"
            value={baseUrl}
            required
            onChange={e => setBaseUrl(e.target.value)}
          />
          <input
            type="password"
            placeholder={editingId ? 'Новый API-ключ (оставьте пустым чтобы не менять)' : 'sk-...'}
            value={apiKey}
            required={!editingId}
            onChange={e => setApiKey(e.target.value)}
          />
          <label className="api-enabled-check">
            <input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)} />
            <span>Включён</span>
          </label>
        </div>

        <div className="api-models-editor">
          <div className="api-models-head">
            <strong>Модели</strong>
            <button type="button" className="alt-action" onClick={() => setModels(m => [...m, emptyModel()])}>
              <Plus size={12} />
              <span>Модель</span>
            </button>
          </div>
          {models.map((m, idx) => (
            <div key={idx} className="api-model-row">
              <input
                type="text"
                placeholder="ID (deepseek-chat)"
                value={m.id}
                onChange={e => setModels(prev => prev.map((it, i) => i === idx ? { ...it, id: e.target.value } : it))}
              />
              <input
                type="text"
                placeholder="Название"
                value={m.label}
                onChange={e => setModels(prev => prev.map((it, i) => i === idx ? { ...it, label: e.target.value } : it))}
              />
              <label title="Поддержка изображений">
                <input
                  type="checkbox"
                  checked={m.supportsVision}
                  onChange={e => setModels(prev => prev.map((it, i) => i === idx ? { ...it, supportsVision: e.target.checked } : it))}
                />
                Vision
              </label>
              <label title="Поддержка reasoning_content">
                <input
                  type="checkbox"
                  checked={m.supportsReasoning}
                  onChange={e => setModels(prev => prev.map((it, i) => i === idx ? { ...it, supportsReasoning: e.target.checked } : it))}
                />
                Thinking
              </label>
              <button
                type="button"
                className="icon-btn"
                aria-label="Удалить модель"
                onClick={() => setModels(prev => prev.length > 1 ? prev.filter((_, i) => i !== idx) : [emptyModel()])}
              >
                <Trash2 size={12} />
              </button>
            </div>
          ))}
        </div>

        {error && <p className="auth-error" role="alert">{error}</p>}
        {status && <p className="settings-inline-notice" role="status"><Check size={12} /> {status}</p>}

        <div className="form-actions">
          {editingId && <button type="button" className="alt-action" onClick={resetForm}>Отмена</button>}
          <button type="submit" className="primary-action" disabled={busy}>
            {busy ? 'Сохранение…' : editingId ? 'Обновить провайдера' : 'Добавить провайдера'}
          </button>
        </div>
      </form>
    </div>
  )
}
