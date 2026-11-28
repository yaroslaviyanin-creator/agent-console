import { useEffect, useRef, useState } from 'react'
import { Check, ChevronRight, Copy, Laptop, Link2, LogOut, Monitor, Palette, Trash2, Upload, User, X } from 'lucide-react'
import type { ServerDirectoryUser, ShareAccess, UserProfile, UserProfilePatch, Workspace } from '../protocol/types'
import { getTelegramUserId } from '../platform/telegram'
import type { RemoteTransport } from '../transport/RemoteTransport'
import { ACCENT_PRESETS, GRADIENT_PALETTES, resolveTheme, type ThemePreference, useAppearance } from './appearanceStore'
import { ColorWheelPicker } from './ColorWheelPicker'
import { changeServerAddress, logout, logoutAllOtherDevices, refreshServerDevices, revokeDevice, setConnectionMode, setShare, updateSelfProfileMember, useChats } from './chatStore'
import { ConnectionMode } from './ConnectionMode'
import { SelectMenu } from './SelectMenu'
import { UserAvatar } from './UserAvatar'

export const rights = { read: 'Чтение', fork: 'Разветвление (Fork)', write: 'Полный доступ' }

function GitHubIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
      <path d="M12 1.5C6.2 1.5 1.5 6.2 1.5 12c0 4.65 3.01 8.59 7.19 9.98.52.1.71-.22.71-.5 0-.25-.01-1.08-.01-1.95-2.92.63-3.54-1.24-3.54-1.24-.48-1.21-1.17-1.53-1.17-1.53-.95-.65.07-.64.07-.64 1.06.07 1.61 1.08 1.61 1.08.94 1.61 2.46 1.14 3.06.87.09-.68.37-1.14.67-1.4-2.33-.26-4.78-1.16-4.78-5.18 0-1.14.41-2.08 1.08-2.81-.11-.26-.47-1.33.1-2.77 0 0 .88-.28 2.89 1.07a10.06 10.06 0 0 1 5.26 0c2.01-1.35 2.89-1.07 2.89-1.07.57 1.44.21 2.51.1 2.77.67.73 1.08 1.67 1.08 2.81 0 4.03-2.46 4.92-4.8 5.17.38.32.72.97.72 1.96 0 1.41-.01 2.55-.01 2.9 0 .28.19.61.72.5A10.5 10.5 0 0 0 22.5 12c0-5.8-4.7-10.5-10.5-10.5Z" />
    </svg>
  )
}

async function compressAvatarFile(file: File): Promise<string> {
  if (!['image/png', 'image/jpeg', 'image/webp'].includes(file.type)) {
    throw new Error('Поддерживаются только PNG, JPEG или WebP.')
  }
  if (file.size > 10 * 1024 * 1024) {
    throw new Error('Размер файла не должен превышать 10 МБ.')
  }
  const url = URL.createObjectURL(file)
  try {
    const img = await new Promise<HTMLImageElement>((resolve, reject) => {
      const el = new Image()
      el.onload = () => resolve(el)
      el.onerror = () => reject(new Error('Не удалось прочитать изображение.'))
      el.src = url
    })
    const size = 192
    const side = Math.min(img.naturalWidth || img.width, img.naturalHeight || img.height)
    const sx = Math.max(0, Math.floor(((img.naturalWidth || img.width) - side) / 2))
    const sy = Math.max(0, Math.floor(((img.naturalHeight || img.height) - side) / 2))
    const canvas = document.createElement('canvas')
    canvas.width = size
    canvas.height = size
    const ctx = canvas.getContext('2d')
    if (!ctx) throw new Error('Не удалось обработать изображение.')
    ctx.drawImage(img, sx, sy, side, side, 0, 0, size, size)

    const tryFormat = (mime: string, quality: number) => canvas.toDataURL(mime, quality)
    for (const [mime, quality] of [['image/webp', 0.86], ['image/webp', 0.7], ['image/jpeg', 0.82], ['image/jpeg', 0.65]] as const) {
      const out = tryFormat(mime, quality)
      if ((out.startsWith('data:image/webp;base64,') || out.startsWith('data:image/jpeg;base64,') || out.startsWith('data:image/png;base64,')) && out.length <= 120_000) {
        return out
      }
    }
    throw new Error('Изображение слишком сложное для сжатия. Выберите другой файл.')
  } finally {
    URL.revokeObjectURL(url)
  }
}

function ShareForm({ w, chatId }: { w: Workspace; chatId: string }) {
  const mode = useChats(state => state.mode)
  const remote = useChats(state => state.remote)
  const room = w.chats.find(c => c.id === chatId)
  const demoOthers = w.users.filter(u => u.id !== w.user.id)
  const [searchQuery, setSearchQuery] = useState('')
  const [searchResults, setSearchResults] = useState<ServerDirectoryUser[]>([])
  const [searching, setSearching] = useState(false)
  const [selected, setSelected] = useState('')
  const [draft, setDraft] = useState<{ access: ShareAccess; trusted: boolean }>()
  const [status, setStatus] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (mode !== 'server' || !remote) return
    const trimmed = searchQuery.trim()
    if (trimmed.length < 2) {
      setSearchResults([])
      setSearching(false)
      return
    }
    const controller = new AbortController()
    setSearching(true)
    const timer = window.setTimeout(() => {
      void remote.searchUsers(trimmed, 10, controller.signal)
        .then(users => {
          if (!controller.signal.aborted) setSearchResults(users)
        })
        .catch(() => {
          if (!controller.signal.aborted) setSearchResults([])
        })
        .finally(() => {
          if (!controller.signal.aborted) setSearching(false)
        })
    }, 250)
    return () => {
      controller.abort()
      clearTimeout(timer)
    }
  }, [mode, remote, searchQuery])

  if (!room || room.access !== 'owner') return null

  const others = mode === 'server'
    ? searchResults.map(u => ({ id: u.id, name: u.displayName?.trim() || u.username }))
    : demoOthers
  const user = selected || others[0]?.id || ''
  const existing = room.grants.find(g => g.userId === user)
  const eligible = others.some(u => u.id === user) || (mode === 'server' && !!selected && !!existing)
  const access = draft?.access ?? existing?.access ?? 'read'
  const trusted = draft?.trusted ?? !!existing?.trustedDeviceId
  function choose(id: string) {
    setSelected(id)
    setDraft(undefined)
    setStatus('')
  }
  async function save(remove = false) {
    if (saving || (remove ? !existing : !eligible)) return
    setSaving(true)
    try {
      await setShare(chatId, user, remove ? null : access, mode === 'demo' && trusted)
      setDraft(undefined)
      if (remove && !eligible) setSelected('')
      setStatus(remove ? 'Доступ отозван.' : 'Права сохранены.')
    } catch (e) { setStatus(e instanceof Error ? e.message : 'Не удалось изменить доступ.') }
    finally { setSaving(false) }
  }
  return <form className="panel-form" onSubmit={e => { e.preventDefault(); void save() }}>
    {mode === 'server' ? (
      <label>Поиск пользователя
        <input
          type="search"
          placeholder="Введите логин или имя (от 2 символов)..."
          value={searchQuery}
          disabled={saving}
          onChange={e => setSearchQuery(e.target.value)}
        />
      </label>
    ) : (
      demoOthers.length === 0 && <p className="form-help">На сервере пока нет других пользователей</p>
    )}
    {mode === 'server' && searchQuery.trim().length >= 2 && !searching && others.length === 0 && (
      <p className="form-help">Пользователи не найдены.</p>
    )}
    {(others.length > 0 || existing) && <>
      <div className="field-group"><span>Пользователь сервера</span><SelectMenu ariaLabel="Пользователь сервера" variant="field" disabled={saving} value={user} options={[...(!others.some(u => u.id === user) && user ? [{ value: user, label: `Выбранный пользователь (${user})` }] : []), ...others.map(u => ({ value: u.id, label: mode === 'server' ? u.name : `${u.name} (@${u.id})` }))]} onChange={choose} /></div>
      <div className="field-group"><span>Права в чате</span><SelectMenu ariaLabel="Права в чате" variant="field" disabled={!eligible || saving} value={access} options={Object.entries(rights).map(([k, v]) => ({ value: k as ShareAccess, label: v }))} onChange={nextAccess => { setSelected(user); setDraft({ access: nextAccess, trusted: false }); setStatus('') }} /></div>
      {mode === 'demo' && <>
        <label className="trust-check"><input type="checkbox" disabled={access !== 'write' || saving} checked={trusted} onChange={e => { setSelected(user); setDraft({ access, trusted: e.target.checked }); setStatus('') }}/>Доверенный режим для команд на моём ПК</label>
        <p className="form-help">При полном доступе участник сможет запускать демо-команды на этом устройстве без отдельного подтверждения.</p>
      </>}
      <div className="form-actions">{eligible && <button disabled={saving} className="primary-action">Сохранить доступ</button>}{existing && <button type="button" disabled={saving} className="alt-action" onClick={() => void save(true)}>Отозвать доступ</button>}</div>
    </>}
    {status && <p role="status">{status}</p>}
    <div className="members"><h3>Участники с доступом</h3>{room.grants.length ? room.grants.map(g => <button type="button" disabled={saving} key={g.userId} onClick={() => choose(g.userId)}><span>{w.users.find(u => u.id === g.userId)?.name ?? g.userId}</span><small>{rights[g.access]}{g.trustedDeviceId ? ' · Доверенный ПК' : ''}</small></button>) : <p>Доступ есть только у вас.</p>}</div>
  </form>
}

const deviceActivityFormat = new Intl.DateTimeFormat('ru', { dateStyle: 'medium', timeStyle: 'short' })

function DevicesSection({ w }: { w: Workspace }) {
  const { mode, address, connected } = useChats()
  const [server, setServer] = useState(address)
  const [confirm, setConfirm] = useState('')
  const [confirmAll, setConfirmAll] = useState(false)
  const [error, setError] = useState('')
  const [revoking, setRevoking] = useState(false)
  useEffect(() => {
    if (mode !== 'server') return
    let open = true
    void refreshServerDevices().catch(e => {
      if (open) setError(e instanceof Error ? e.message : String(e))
    })
    return () => { open = false }
  }, [mode])
  async function revoke(id: string) {
    setError('')
    setRevoking(true)
    try { await revokeDevice(id); setConfirm('') } catch (e) {
      setError(e instanceof Error ? e.message : 'Не удалось отозвать устройство.')
    }
    finally { setRevoking(false) }
  }
  const devices = mode === 'server' ? w.devices : w.devices.filter(d => d.ownerId === w.user.id)
  return (
    <div className="settings-section-stack">
      <div className="settings-row">
        <div className="settings-row-info">
          <strong>Режим подключения</strong>
          <small>Переключение между локальной песочницей и вашим сервером</small>
        </div>
        <div className="settings-row-control">
          <ConnectionMode mode={mode} change={setConnectionMode}/>
        </div>
      </div>

      {mode === 'server' && (
        <form className="settings-row settings-row-stack" onSubmit={e => {
          e.preventDefault()
          setError('')
          try { changeServerAddress(server) } catch (e) { setError(e instanceof Error ? e.message : 'Некорректный адрес.') }
        }}>
          <div className="settings-row-info">
            <strong>Адрес сервера</strong>
            <small>Смена адреса завершит текущую сессию на этом устройстве</small>
          </div>
          <div className="settings-inline-form">
            <input type="url" required value={server} onChange={e => setServer(e.target.value)} aria-label="Адрес сервера"/>
            {server.trim() !== address && <button className="alt-action">Сменить и выйти</button>}
          </div>
        </form>
      )}

      <div className="settings-subhead">
        <h3>Активные устройства</h3>
        <span className="settings-meta-note">{w.user.name}{mode === 'demo' ? ' · @' + w.user.id : ''}</span>
      </div>

      <div className="device-list">
        {devices.map(d => {
          const current = d.id === w.deviceId
          const online = mode === 'server' ? connected : d.online
          const lastSeenAt = d.lastSeenAt ?? 0
          return (
            <div key={d.id} className="device-row">
              <Monitor size={17} className="device-row-icon" />
              <div className="device-row-meta">
                <div className="device-row-title">
                  <strong>{d.name}</strong>
                  {current && <span className="device-current-pill">Это устройство</span>}
                </div>
                {mode === 'demo' || current ? (
                  <small>{online ? 'Онлайн' : 'Офлайн'}</small>
                ) : (
                  lastSeenAt > 0 && <small>Активность: {deviceActivityFormat.format(lastSeenAt)}</small>
                )}
              </div>
              {confirm !== d.id ? (
                <button type="button" className="alt-action device-revoke-btn" disabled={revoking} onClick={() => setConfirm(d.id)}>Отозвать</button>
              ) : (
                <div className="device-confirm">
                  <span>{current ? 'Завершить сессию?' : 'Отозвать доступ?'}</span>
                  <button type="button" className="alt-action danger-action" disabled={revoking} onClick={() => void revoke(d.id)}>Да</button>
                  <button type="button" className="alt-action" disabled={revoking} onClick={() => setConfirm('')}>Отмена</button>
                </div>
              )}
            </div>
          )
        })}
      </div>

      {error && <p className="auth-error" role="alert">{error}</p>}

      {mode === 'server' && (
        <div className="settings-row">
          <div className="settings-row-info">
            <strong>Другие устройства</strong>
            <small>Завершить все активные сессии, кроме текущего устройства</small>
          </div>
          {!confirmAll ? (
            <button type="button" className="alt-action" disabled={revoking} onClick={() => setConfirmAll(true)}>
              Выйти со всех других устройств
            </button>
          ) : (
            <div className="device-confirm">
              <span>Завершить остальные сессии?</span>
              <button type="button" className="alt-action danger-action" disabled={revoking} onClick={async () => {
                setError('')
                setRevoking(true)
                try {
                  await logoutAllOtherDevices()
                  setConfirmAll(false)
                } catch (e) {
                  setError(e instanceof Error ? e.message : 'Не удалось завершить другие сессии.')
                } finally {
                  setRevoking(false)
                }
              }}>Да</button>
              <button type="button" className="alt-action" disabled={revoking} onClick={() => setConfirmAll(false)}>Отмена</button>
            </div>
          )}
        </div>
      )}

      <div className="settings-row settings-row-footer">
        <div className="settings-row-info">
          <strong>Сессия аккаунта</strong>
          <small>Выход отзовёт токен текущего устройства</small>
        </div>
        <button type="button" className="alt-action" onClick={logout}><LogOut size={14}/>Выйти из аккаунта</button>
      </div>
    </div>
  )
}

const textEncoder = new TextEncoder()

function IntegrationsBlock() {
  const telegramId = getTelegramUserId()
  const [copiedTg, setCopiedTg] = useState(false)
  const [githubNote, setGithubNote] = useState('')

  async function copyTelegramId() {
    if (!telegramId) return
    try {
      await navigator.clipboard.writeText(telegramId)
    } catch {
      // Fallback ignored if clipboard blocked.
    }
    setCopiedTg(true)
    window.setTimeout(() => setCopiedTg(false), 1800)
  }

  return (
    <div className="settings-group">
      <div className="settings-subhead">
        <h3>Интеграции</h3>
      </div>

      <div className="settings-row">
        <div className="settings-row-info">
          <strong>Telegram ID</strong>
          <small>Идентификатор пользователя в Telegram Mini App</small>
        </div>
        <div className="settings-row-control">
          {telegramId ? (
            <div className="settings-copy-pill">
              <code>{telegramId}</code>
              <button type="button" className="icon-btn" onClick={() => void copyTelegramId()} aria-label="Копировать Telegram ID" title="Копировать Telegram ID">
                {copiedTg ? <Check size={14} /> : <Copy size={14} />}
              </button>
            </div>
          ) : (
            <span className="settings-muted-pill">Не привязан</span>
          )}
        </div>
      </div>

      <div className="settings-row">
        <div className="settings-row-info settings-row-with-icon">
          <span className="settings-provider-badge"><GitHubIcon /></span>
          <div>
            <div className="settings-title-line">
              <strong>GitHub</strong>
              <span className="settings-soon-badge">Скоро</span>
            </div>
            <small>Синхронизация репозиториев и PR через OAuth</small>
          </div>
        </div>
        <div className="settings-row-control">
          <button
            type="button"
            className="alt-action"
            title="GitHub OAuth будет доступен в следующем обновлении"
            onClick={() => setGithubNote('Интеграция с GitHub OAuth будет доступна в следующем обновлении.')}
          >
            Подключить
          </button>
        </div>
      </div>
      {githubNote && <p className="settings-inline-notice" role="status">{githubNote}</p>}
    </div>
  )
}

function ProfileSection({ w, remote }: { w: Workspace; remote: RemoteTransport | null }) {
  const [saved, setSaved] = useState<UserProfile>()
  const [draft, setDraft] = useState<UserProfilePatch>({})
  const [status, setStatus] = useState('')
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)
  const [saving, setSaving] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const isServer = remote !== null && w.user.role !== 'pending'

  useEffect(() => {
    if (!isServer) return
    let current = true
    void remote.getProfile().then(profile => {
      if (current) {
        setSaved(profile)
        updateSelfProfileMember({ displayName: profile.displayName, avatarData: profile.avatarData })
      }
    }).catch(e => {
      if (current) setError(e instanceof Error ? e.message : 'Не удалось загрузить профиль.')
    })
    return () => { current = false }
  }, [remote, isServer, attempt])

  function edit<K extends keyof UserProfilePatch>(key: K, value: UserProfilePatch[K]) {
    if (!isServer) {
      if (key === 'displayName') updateSelfProfileMember({ displayName: String(value ?? '') })
      if (key === 'avatarData') updateSelfProfileMember({ avatarData: String(value ?? '') })
      setStatus('Обновлено в демо-сессии')
      setError('')
      return
    }
    setDraft(previous => {
      const next = { ...previous }
      if (value === saved?.[key]) delete next[key]
      else next[key] = value
      return next
    })
    setStatus('')
    setError('')
  }

  async function save() {
    if (!isServer || saving || !Object.keys(draft).length) return
    setSaving(true)
    setError('')
    try {
      const profile = await remote.updateProfile(draft)
      setSaved(profile)
      setDraft({})
      updateSelfProfileMember({ displayName: profile.displayName, avatarData: profile.avatarData })
      setStatus('Сохранено')
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Не удалось сохранить настройки.')
    } finally {
      setSaving(false)
    }
  }

  async function handleAvatarChange(file?: File) {
    if (!file) return
    setError('')
    try {
      const compressed = await compressAvatarFile(file)
      if (!isServer) {
        updateSelfProfileMember({ avatarData: compressed })
        setStatus('Аватар обновлён в демо-сессии')
      } else {
        setDraft(prev => ({ ...prev, avatarData: compressed }))
        setStatus('')
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Не удалось обработать фото.')
    }
  }

  if (isServer && !saved) {
    return error ? (
      <div className="profile-load">
        <p role="alert">{error}</p>
        <button type="button" className="alt-action" onClick={() => { setError(''); setAttempt(n => n + 1) }}>Повторить</button>
      </div>
    ) : (
      <div className="skeleton-stack" role="status" aria-label="Загрузка настроек…">
        <span className="sr-only">Загрузка настроек…</span>
        <div className="skeleton-line is-short" />
        <div className="skeleton-line is-long" />
        <div className="skeleton-line is-medium" />
      </div>
    )
  }

  const profile: UserProfile = isServer
    ? { ...saved!, ...draft }
    : {
        displayName: w.user.displayName ?? '',
        avatarData: w.user.avatarData ?? '',
        profileMd: '',
        memoryMd: '',
        memoryEnabled: true,
        memoryUpdatedAt: 0,
      }

  const avatarPreviewName = profile.displayName.trim() || w.user.name
  const lengthy = (['profileMd', 'memoryMd'] as const).some(key =>
    draft[key] !== undefined && textEncoder.encode(draft[key]).length > 4096)

  return (
    <form className="settings-section-stack" onSubmit={e => { e.preventDefault(); void save() }}>
      <div className="settings-row">
        <div className="settings-row-info">
          <strong>Фото профиля</strong>
          <small>PNG, JPEG или WebP до 10 МБ</small>
        </div>
        <div className="settings-avatar-controls">
          <UserAvatar name={avatarPreviewName} avatarData={profile.avatarData} className="settings-avatar-preview" />
          <input
            ref={fileInputRef}
            type="file"
            accept="image/png,image/jpeg,image/webp"
            className="sr-only"
            onChange={e => {
              const file = e.target.files?.[0]
              e.target.value = ''
              void handleAvatarChange(file)
            }}
          />
          <button type="button" className="alt-action" disabled={saving} onClick={() => fileInputRef.current?.click()}>
            <Upload size={13} />
            {profile.avatarData ? 'Заменить' : 'Загрузить'}
          </button>
          {profile.avatarData && (
            <button
              type="button"
              className="alt-action danger-action"
              disabled={saving}
              aria-label="Удалить фото профиля"
              title="Удалить фото профиля"
              onClick={() => edit('avatarData', '')}
            >
              <Trash2 size={13} />
            </button>
          )}
        </div>
      </div>

      <div className="settings-row">
        <div className="settings-row-info">
          <label htmlFor="profile-display-name"><strong>Как к вам обращаться</strong></label>
          <small>Отображается в сообщениях, профиле и инструкциях агента</small>
        </div>
        <div className="settings-row-control settings-input-col">
          <input
            id="profile-display-name"
            value={profile.displayName}
            disabled={saving}
            placeholder={w.user.name}
            onChange={e => edit('displayName', [...e.target.value].slice(0, 80).join(''))}
          />
        </div>
      </div>

      {isServer ? (
        <>
          <div className="settings-row settings-row-stack">
            <div className="settings-row-info">
              <label htmlFor="profile-prefs"><strong>Личные предпочтения</strong></label>
              <small>Стиль ответов, ваш стек и формат объяснений</small>
            </div>
            <textarea
              id="profile-prefs"
              rows={3}
              maxLength={4000}
              disabled={saving}
              value={profile.profileMd}
              placeholder="Стиль ответов, ваш стек, формат объяснений…"
              onChange={e => edit('profileMd', e.target.value)}
            />
          </div>

          <label className="settings-row memory-toggle">
            <span className="settings-row-info">
              <strong>Память из чатов</strong>
              <small id="memory-description">Ассистент автоматически запоминает важные факты из личных чатов</small>
            </span>
            <input
              type="checkbox"
              role="switch"
              disabled={saving}
              checked={profile.memoryEnabled}
              aria-label="Память из чатов"
              aria-describedby="memory-description"
              onChange={e => edit('memoryEnabled', e.target.checked)}
            />
          </label>

          <details className="profile-memory">
            <summary><ChevronRight size={16}/>Что ассистент помнит о вас</summary>
            <textarea
              rows={4}
              maxLength={4000}
              disabled={saving}
              value={profile.memoryMd.trim() === '[]' ? '' : profile.memoryMd}
              placeholder="Пока нет сохранённых фактов"
              aria-label="Что ассистент помнит о вас"
              onChange={e => edit('memoryMd', e.target.value)}
            />
            <div className="memory-actions">
              {saved && saved.memoryUpdatedAt > 0 && <small>Обновлено {deviceActivityFormat.format(saved.memoryUpdatedAt)}</small>}
              <button type="button" className="text-action" disabled={saving || !profile.memoryMd || profile.memoryMd.trim() === '[]'} onClick={() => edit('memoryMd', '')}>
                Очистить память
              </button>
            </div>
          </details>

          {lengthy && <p className="auth-error" role="alert">Сократите текст в настройках, чтобы сохранить изменения.</p>}
          {error && <p className="auth-error" role="alert">{error}</p>}

          <div className="profile-actions">
            <button className="primary-action" disabled={saving || lengthy || !Object.keys(draft).length}>
              {saving ? 'Сохраняю…' : 'Сохранить'}
            </button>
            <span role="status">{status}</span>
          </div>
        </>
      ) : (
        <>
          {error && <p className="auth-error" role="alert">{error}</p>}
          {status && <p className="settings-inline-notice" role="status">{status}</p>}
        </>
      )}

      <IntegrationsBlock />
    </form>
  )
}

function AppearanceSection() {
  const {
    theme,
    accent,
    accentCustomColor,
    ambientGradient,
    gradientPalette,
    gradientCustomColor,
    gradientCustomColor2,
    setTheme,
    setAccent,
    setAccentCustomColor,
    setAmbientGradient,
    setGradientPalette,
    setGradientCustomColor,
    setGradientCustomColor2,
  } = useAppearance()
  const [accentTab, setAccentTab] = useState<'presets' | 'custom'>(() => (accent === 'custom' ? 'custom' : 'presets'))
  const [paletteTab, setPaletteTab] = useState<'presets' | 'custom'>(() => (gradientPalette === 'custom' ? 'custom' : 'presets'))
  const [gradientSlot, setGradientSlot] = useState<1 | 2>(1)
  const isLight = resolveTheme(theme) === 'light'
  const themeOptions: readonly { id: ThemePreference; label: string }[] = [
    { id: 'dark', label: 'Тёмная' },
    { id: 'light', label: 'Светлая' },
    { id: 'system', label: 'Системная' },
  ]

  return (
    <div className="settings-section-stack">
      <div className="settings-row">
        <div className="settings-row-info">
          <strong>Тема интерфейса</strong>
          <small>Подстраивается под систему или фиксируется вручную</small>
        </div>
        <div className="settings-segmented" role="group" aria-label="Тема интерфейса">
          {themeOptions.map(opt => (
            <button
              key={opt.id}
              type="button"
              aria-pressed={theme === opt.id}
              onClick={() => setTheme(opt.id)}
            >
              {opt.label}
            </button>
          ))}
        </div>
      </div>

      <div className="settings-row settings-row-col">
        <div className="settings-row-top">
          <div className="settings-row-info">
            <strong>Цветовая гамма</strong>
            <small>Акцентные элементы и слайдер рассуждений</small>
          </div>
          <div className="settings-segmented color-mode-tabs" role="tablist" aria-label="Режим акцента">
            <button
              type="button"
              role="tab"
              aria-selected={accentTab === 'presets'}
              onClick={() => setAccentTab('presets')}
            >
              Основные
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={accentTab === 'custom'}
              onClick={() => {
                setAccentTab('custom')
                if (accent !== 'custom') setAccentCustomColor(accentCustomColor)
              }}
            >
              Свои цвета
            </button>
          </div>
        </div>
        {accentTab === 'presets' ? (
          <div className="swatch-row-wrap">
            <div className="accent-swatches" role="group" aria-label="Цветовая гамма">
              {ACCENT_PRESETS.map(preset => (
                <button
                  key={preset.id}
                  type="button"
                  className={`accent-swatch${accent === preset.id ? ' is-selected' : ''}`}
                  style={{ '--swatch': preset.swatch } as React.CSSProperties}
                  aria-label={preset.label}
                  title={preset.label}
                  aria-pressed={accent === preset.id}
                  onClick={() => setAccent(preset.id)}
                >
                  {accent === preset.id && <Check size={12} />}
                </button>
              ))}
            </div>
            {accent !== 'custom' && (
              <span className="swatch-selected-name">
                {ACCENT_PRESETS.find(p => p.id === accent)?.label}
              </span>
            )}
          </div>
        ) : (
          <ColorWheelPicker
            value={accentCustomColor}
            onChange={setAccentCustomColor}
            label="HEX акцента"
          />
        )}
      </div>

      <div className={`settings-group${isLight ? ' is-light-disabled' : ''}`}>
        <div className="settings-subhead">
          <h3>Эффекты и фон</h3>
          {isLight && <span className="settings-light-note">Доступно в тёмной теме</span>}
        </div>
        <label className="settings-row memory-toggle">
          <span className="settings-row-info">
            <strong>Анимированный градиент на фоне</strong>
            <small>
              {isLight
                ? 'Светлая тема использует чистый ровный фон без градиентов'
                : 'Мягкое свечение орбов при генерации ответа, статичное в покое'}
            </small>
          </span>
          <input
            type="checkbox"
            role="switch"
            checked={!isLight && ambientGradient}
            disabled={isLight}
            aria-label="Анимированный градиент на фоне"
            onChange={e => setAmbientGradient(e.target.checked)}
          />
        </label>
        <div className="settings-row palette-row settings-row-col">
          <div className="settings-row-top">
            <div className="settings-row-info">
              <strong>Палитра градиента</strong>
              <small>Готовые сочетания или два своих оттенка для фоновых орбов</small>
            </div>
            <div className="settings-segmented color-mode-tabs" role="tablist" aria-label="Режим палитры градиента">
              <button
                type="button"
                role="tab"
                disabled={isLight || !ambientGradient}
                aria-selected={paletteTab === 'presets'}
                onClick={() => setPaletteTab('presets')}
              >
                Основные
              </button>
              <button
                type="button"
                role="tab"
                disabled={isLight || !ambientGradient}
                aria-selected={paletteTab === 'custom'}
                onClick={() => {
                  setPaletteTab('custom')
                  if (gradientPalette !== 'custom') setGradientCustomColor(gradientCustomColor)
                }}
              >
                Свои цвета
              </button>
            </div>
          </div>
          {paletteTab === 'presets' ? (
            <div className="swatch-row-wrap">
              <div className="accent-swatches palette-picker" role="group" aria-label="Палитра градиента">
                {GRADIENT_PALETTES.map(preset => (
                  <button
                    key={preset.id}
                    type="button"
                    className={`accent-swatch palette-swatch${preset.id === 'accent' ? ' is-accent-link' : ''}${gradientPalette === preset.id ? ' is-selected' : ''}`}
                    style={{ '--swatch': preset.swatch } as React.CSSProperties}
                    aria-label={preset.label}
                    title={preset.label}
                    aria-pressed={gradientPalette === preset.id}
                    disabled={isLight || !ambientGradient}
                    onClick={() => setGradientPalette(preset.id)}
                  >
                    {gradientPalette === preset.id ? (
                      <Check size={12} />
                    ) : preset.id === 'accent' ? (
                      <Link2 size={11} aria-hidden="true" />
                    ) : null}
                  </button>
                ))}
              </div>
              {gradientPalette !== 'custom' && (
                <span className="swatch-selected-name">
                  {GRADIENT_PALETTES.find(p => p.id === gradientPalette)?.label}
                </span>
              )}
            </div>
          ) : (
            <div className="custom-gradient-editor">
              <div className="gradient-stop-tabs" role="group" aria-label="Цвета градиента">
                <button
                  type="button"
                  className={`gradient-stop-btn${gradientSlot === 1 ? ' is-active' : ''}`}
                  disabled={isLight || !ambientGradient}
                  onClick={() => setGradientSlot(1)}
                >
                  <span className="palette-dot" style={{ background: gradientCustomColor }} aria-hidden="true" />
                  <span>Цвет 1</span>
                </button>
                <button
                  type="button"
                  className={`gradient-stop-btn${gradientSlot === 2 ? ' is-active' : ''}`}
                  disabled={isLight || !ambientGradient}
                  onClick={() => setGradientSlot(2)}
                >
                  <span className="palette-dot" style={{ background: gradientCustomColor2 }} aria-hidden="true" />
                  <span>Цвет 2</span>
                </button>
              </div>
              <ColorWheelPicker
                value={gradientSlot === 1 ? gradientCustomColor : gradientCustomColor2}
                onChange={hex => (gradientSlot === 1 ? setGradientCustomColor(hex) : setGradientCustomColor2(hex))}
                disabled={isLight || !ambientGradient}
                label={gradientSlot === 1 ? 'HEX цвета градиента 1' : 'HEX цвета градиента 2'}
              />
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

type SettingsTab = 'profile' | 'appearance' | 'devices'

export function WorkspaceDialog({ kind, w, chatId, close }: { kind: 'share' | 'devices'; w: Workspace; chatId: string; close: () => void }) {
  const ref = useRef<HTMLDialogElement>(null)
  const remote = useChats(state => state.remote)
  const [tab, setTab] = useState<SettingsTab>('profile')

  useEffect(() => {
    const el = ref.current!
    el.showModal()
    return () => el.close()
  }, [])

  if (kind === 'share') {
    return (
      <dialog className="workspace-dialog" ref={ref} aria-labelledby="panel-heading" onCancel={close} onClick={e => { if (e.target === ref.current) close() }}>
        <section>
          <div className="panel-heading">
            <h2 id="panel-heading">Поделиться чатом</h2>
            <button className="icon-btn" aria-label="Закрыть" onClick={close}><X size={18}/></button>
          </div>
          <ShareForm key={chatId} w={w} chatId={chatId}/>
        </section>
      </dialog>
    )
  }

  const navItems: readonly { id: SettingsTab; label: string; icon: typeof User }[] = [
    { id: 'profile', label: 'Профиль и память', icon: User },
    { id: 'appearance', label: 'Оформление', icon: Palette },
    { id: 'devices', label: 'Устройства', icon: Laptop },
  ]
  const currentTitle = navItems.find(i => i.id === tab)?.label ?? 'Настройки'

  return (
    <dialog
      className="workspace-dialog settings-dialog"
      ref={ref}
      aria-labelledby="settings-heading"
      onCancel={close}
      onClick={e => { if (e.target === ref.current) close() }}
    >
      <div className="settings-shell">
        <aside className="settings-nav">
          <div className="settings-nav-head">
            <h2 id="settings-heading">Настройки</h2>
            <button type="button" className="icon-btn settings-mobile-close" aria-label="Закрыть" onClick={close}><X size={18}/></button>
          </div>
          <nav className="settings-nav-list" aria-label="Разделы настроек">
            {navItems.map(item => {
              const Icon = item.icon
              return (
                <button
                  key={item.id}
                  type="button"
                  className={`settings-nav-item${tab === item.id ? ' is-active' : ''}`}
                  aria-pressed={tab === item.id}
                  onClick={() => setTab(item.id)}
                >
                  <Icon size={15} />
                  <span>{item.label}</span>
                </button>
              )
            })}
          </nav>
        </aside>
        <section className="settings-content">
          <div className="settings-content-head">
            <h3>{currentTitle}</h3>
            <button type="button" className="icon-btn settings-desktop-close" aria-label="Закрыть" onClick={close}><X size={18}/></button>
          </div>
          <div className="settings-content-body">
            {tab === 'profile' && <ProfileSection w={w} remote={remote} />}
            {tab === 'appearance' && <AppearanceSection />}
            {tab === 'devices' && <DevicesSection w={w} />}
          </div>
        </section>
      </div>
    </dialog>
  )
}
