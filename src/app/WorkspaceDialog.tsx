import { useEffect, useRef, useState } from 'react'
import { ChevronRight, LogOut, Monitor, X } from 'lucide-react'
import type { ShareAccess, UserProfile, UserProfilePatch, Workspace } from '../protocol/types'
import { changeServerAddress, logout, refreshServerDevices, revokeDevice, setConnectionMode, setShare, useChats } from './chatStore'
import { ConnectionMode } from './ConnectionMode'
import type { RemoteTransport } from '../transport/RemoteTransport'

export const rights = { read: 'Чтение', fork: 'Разветвление (Fork)', write: 'Полный доступ' }
function ShareForm({ w, chatId }: { w: Workspace; chatId: string }) {
  const mode = useChats(state => state.mode)
  const room = w.chats.find(c => c.id === chatId)
  const others = w.users.filter(u => u.id !== w.user.id)
  const [selected, setSelected] = useState('')
  const [draft, setDraft] = useState<{ access: ShareAccess; trusted: boolean }>()
  const [status, setStatus] = useState('')
  const [saving, setSaving] = useState(false)
  if (!room || room.access !== 'owner') return null

  const user = selected || others[0]?.id || ''
  const existing = room.grants.find(g => g.userId === user)
  const eligible = others.some(u => u.id === user)
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
    {others.length === 0 && <p className="form-help">На сервере пока нет других пользователей</p>}
    {(others.length > 0 || existing) && <>
      <label>Пользователь сервера<select disabled={saving} value={user} onChange={e => choose(e.target.value)}>
        {!eligible && <option value={user}>Недоступный пользователь ({user})</option>}
        {others.map(u => <option key={u.id} value={u.id}>{mode === 'server' ? u.name : `${u.name} (@${u.id})`}</option>)}
      </select></label>
      <label>Права в чате<select disabled={!eligible || saving} value={access} onChange={e => {
        setSelected(user)
        setDraft({ access: e.target.value as ShareAccess, trusted: false })
        setStatus('')
      }}>{Object.entries(rights).map(([k,v]) => <option value={k} key={k}>{v}</option>)}</select></label>
      {mode === 'demo' && <>
        <label className="trust-check"><input type="checkbox" disabled={access !== 'write' || saving} checked={trusted} onChange={e => {
          setSelected(user)
          setDraft({ access, trusted: e.target.checked })
          setStatus('')
        }}/>Доверенный режим для команд на моём ПК</label>
        <p className="form-help">При полном доступе участник сможет запускать демо-команды на этом устройстве без отдельного подтверждения.</p>
      </>}
      <div className="form-actions">{eligible && <button disabled={saving} className="primary-action">Сохранить доступ</button>}{existing && <button type="button" disabled={saving} className="alt-action" onClick={() => void save(true)}>Отозвать доступ</button>}</div>
    </>}
    {status && <p role="status">{status}</p>}
    <div className="members"><h3>Участники с доступом</h3>{room.grants.length ? room.grants.map(g => <button type="button" disabled={saving} key={g.userId} onClick={() => choose(g.userId)}><span>{w.users.find(u => u.id === g.userId)?.name ?? g.userId}</span><small>{rights[g.access]}{g.trustedDeviceId ? ' · Доверенный ПК' : ''}</small></button>) : <p>Доступ есть только у вас.</p>}</div>
  </form>
}

const deviceActivityFormat = new Intl.DateTimeFormat('ru', { dateStyle: 'medium', timeStyle: 'short' })

function Devices({ w }: { w: Workspace }) {
  const { mode, address, connected } = useChats()
  const [server, setServer] = useState(address)
  const [confirm, setConfirm] = useState('')
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
  return <>
    <ConnectionMode mode={mode} change={setConnectionMode}/>
    {mode === 'server' && <form className="panel-form server-settings" onSubmit={e => {
      e.preventDefault()
      setError('')
      try { changeServerAddress(server) } catch (e) { setError(e instanceof Error ? e.message : 'Некорректный адрес.') }
    }}>
      <label>Адрес сервера<input type="url" required value={server} onChange={e => setServer(e.target.value)}/></label>
      {server.trim() !== address && <button className="alt-action">Сменить сервер и выйти</button>}
    </form>}
    <p className="form-help">{w.user.name}{mode === 'demo' && ' · @' + w.user.id}</p>
    <div className="device-list">{devices.map(d => {
      const current = d.id === w.deviceId
      const online = mode === 'server' ? connected : d.online
      const lastSeenAt = d.lastSeenAt ?? 0
      return <div key={d.id} className="device-row"><Monitor size={18}/><div><strong>{d.name}</strong>
        {mode === 'demo' || current ? <small>{online ? 'Онлайн' : 'Офлайн'}{current ? ' · Это устройство' : ''}</small> :
          lastSeenAt > 0 && <small>Активность: {deviceActivityFormat.format(lastSeenAt)}</small>}</div>
        {confirm !== d.id ? <button className="text-action" disabled={revoking} onClick={() => setConfirm(d.id)}>Отозвать</button> :
          <div className="device-confirm"><span>{current ? 'Завершить эту сессию?' : 'Отозвать доступ?'}</span>
            <button disabled={revoking} onClick={() => void revoke(d.id)}>Да, отозвать</button><button disabled={revoking} onClick={() => setConfirm('')}>Отмена</button>
          </div>}
      </div>
    })}</div>
    {error && <p role="alert">{error}</p>}<button className="alt-action" onClick={logout}><LogOut size={14}/>Выйти из аккаунта</button></>
}

const textEncoder = new TextEncoder()

function ProfileSettings({ remote }: { remote: RemoteTransport }) {
  const [saved, setSaved] = useState<UserProfile>()
  const [draft, setDraft] = useState<UserProfilePatch>({})
  const [status, setStatus] = useState('')
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    let current = true
    void remote.getProfile().then(profile => {
      if (current) setSaved(profile)
    }).catch(e => {
      if (current) setError(e instanceof Error ? e.message : 'Не удалось загрузить профиль.')
    })
    return () => { current = false }
  }, [remote, attempt])

  function edit<K extends keyof UserProfilePatch>(key: K, value: UserProfilePatch[K]) {
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
    if (saving || !Object.keys(draft).length) return
    setSaving(true)
    setError('')
    try {
      const profile = await remote.updateProfile(draft)
      setSaved(profile)
      setDraft({})
      setStatus('Сохранено')
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Не удалось сохранить настройки.')
    } finally {
      setSaving(false)
    }
  }

  if (!saved) return error
    ? <div className="profile-load"><p role="alert">{error}</p><button className="alt-action" onClick={() => {
      setError('')
      setAttempt(n => n + 1)
    }}>Повторить</button></div>
    : <p className="form-help" role="status">Загрузка настроек…</p>

  const profile = { ...saved, ...draft }
  const lengthy = (['profileMd', 'memoryMd'] as const).some(key =>
    draft[key] !== undefined && textEncoder.encode(draft[key]).length > 4096)
  return <form className="panel-form profile-form" onSubmit={e => { e.preventDefault(); void save() }}>
    <fieldset disabled={saving}>
      <legend>Профиль</legend>
      <label>Как к вам обращаться
        <input value={profile.displayName} placeholder="Например, Ярослав"
          onChange={e => edit('displayName', [...e.target.value].slice(0, 80).join(''))}/>
      </label>
      <label>Личные предпочтения
        <textarea rows={3} maxLength={4000} value={profile.profileMd} placeholder="Стиль ответов, ваш стек, формат объяснений…"
          onChange={e => edit('profileMd', e.target.value)}/>
      </label>
    </fieldset>
    <fieldset disabled={saving}>
      <legend>Память</legend>
      <label className="memory-toggle">
        <span>Память из чатов<small id="memory-description">Ассистент автоматически запоминает важные факты из ваших личных чатов</small></span>
        <input type="checkbox" role="switch" checked={profile.memoryEnabled} aria-label="Память из чатов" aria-describedby="memory-description"
          onChange={e => edit('memoryEnabled', e.target.checked)}/>
      </label>
      <details className="profile-memory">
        <summary><ChevronRight size={16}/>Что ассистент помнит о вас</summary>
        <textarea rows={4} maxLength={4000} value={profile.memoryMd} aria-label="Что ассистент помнит о вас"
          onChange={e => edit('memoryMd', e.target.value)}/>
        <div className="memory-actions">
          {saved.memoryUpdatedAt > 0 && <small>Обновлено {deviceActivityFormat.format(saved.memoryUpdatedAt)}</small>}
          <button type="button" className="text-action" disabled={!profile.memoryMd}
            onClick={() => edit('memoryMd', '')}>Очистить память</button>
        </div>
      </details>
    </fieldset>
    {lengthy && <p role="alert">Сократите текст в настройках, чтобы сохранить изменения.</p>}
    {error && <p role="alert">{error}</p>}
    <div className="profile-actions">
      <button className="primary-action" disabled={saving || lengthy || !Object.keys(draft).length}>{saving ? 'Сохраняю…' : 'Сохранить'}</button>
      <span role="status">{status}</span>
    </div>
  </form>
}

export function WorkspaceDialog({ kind, w, chatId, close }: { kind: 'share' | 'devices'; w: Workspace; chatId: string; close: () => void }) {
  const ref = useRef<HTMLDialogElement>(null)
  const remote = useChats(state => state.remote)
  const serverSettings = kind === 'devices' && remote !== null && w.user.role !== 'pending'
  const [tab, setTab] = useState<'profile' | 'devices'>('profile')
  useEffect(() => { const el = ref.current!; el.showModal(); return () => el.close() }, [])
  return <dialog className={serverSettings ? 'workspace-dialog profile-dialog' : 'workspace-dialog'} ref={ref} aria-labelledby="panel-heading"
    onCancel={close} onClick={e => { if (e.target === ref.current) close() }}>
    <section>
      <div className="panel-heading"><h2 id="panel-heading">{kind === 'share' ? 'Поделиться чатом' : serverSettings ? 'Настройки' : 'Мои устройства'}</h2>
        <button className="icon-btn" aria-label="Закрыть" onClick={close}><X size={18}/></button>
      </div>
      {kind === 'share' ? <ShareForm key={chatId} w={w} chatId={chatId}/> : serverSettings ? <>
        <div className="settings-tabs" role="group" aria-label="Раздел настроек">
          <button type="button" aria-pressed={tab === 'profile'} aria-controls="settings-profile" onClick={() => setTab('profile')}>Профиль и память</button>
          <button type="button" aria-pressed={tab === 'devices'} aria-controls="settings-devices" onClick={() => setTab('devices')}>Устройства</button>
        </div>
        <div id="settings-profile" hidden={tab !== 'profile'}><ProfileSettings remote={remote}/></div>
        <div id="settings-devices" hidden={tab !== 'devices'}>{tab === 'devices' && <Devices w={w}/>}</div>
      </> : <Devices w={w}/>}
    </section>
  </dialog>
}
