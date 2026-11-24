import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { X } from 'lucide-react'
import type { AdminAdapter, AdminUser, UserRole } from '../protocol/types'

const roleLabel = { user: 'Пользователь', admin: 'Администратор' }
const emptyForm = { username: '', password: '', role: 'user' as UserRole }
const failure = (error: unknown) => error instanceof Error ? error.message : 'Операция не выполнена.'
const generatePassword = () => Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, '0')).join('')

type UserAction = { user: AdminUser; type: 'password' | 'block' | 'revoke' }

export function AdminDialog({ admin, userId, close }: { admin: AdminAdapter; userId: string; close: () => void }) {
  const ref = useRef<HTMLDialogElement>(null)
  const [users, setUsers] = useState<AdminUser[]>([])
  const [form, setForm] = useState(emptyForm)
  const [action, setAction] = useState<UserAction | null>(null)
  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [status, setStatus] = useState('')
  const [reload, setReload] = useState(0)

  useEffect(() => {
    const dialog = ref.current!
    dialog.showModal()
    return () => dialog.close()
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    void admin.list(controller.signal).then(result => {
      if (!controller.signal.aborted) setUsers(result)
    }).catch(reason => {
      if (!controller.signal.aborted) setError(failure(reason))
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false)
    })
    return () => controller.abort()
  }, [admin, reload])

  async function mutate(run: () => Promise<unknown>, message: string, after?: () => void) {
    setBusy(true)
    setError('')
    setStatus('')
    try {
      await run()
      after?.()
      setStatus(message)
      setUsers(await admin.list())
    } catch (reason) {
      setError(failure(reason))
    } finally {
      setBusy(false)
    }
  }

  function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    void mutate(() => admin.create({ ...form, username: form.username.trim() }), 'Пользователь создан.', () => setForm(emptyForm))
  }

  function confirm(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!action) return
    const { type, user } = action
    const run = type === 'revoke' ? () => admin.revokeDevices(user.id)
      : () => admin.update(user.id, type === 'password' ? { newPassword: password } : { isActive: false })
    const message = type === 'revoke' ? 'Сессии завершены.' : type === 'password' ? 'Пароль изменён. Сессии завершены.' : 'Пользователь заблокирован.'
    void mutate(run, message, () => { setAction(null); setPassword('') })
  }

  return <dialog ref={ref} className="workspace-dialog admin-dialog" aria-labelledby="admin-heading" onCancel={close} onClick={event => { if (event.target === ref.current) close() }}>
    <section>
      <header className="panel-heading"><h2 id="admin-heading">Админка</h2><button type="button" className="icon-btn" onClick={close} aria-label="Закрыть"><X size={18}/></button></header>
      {error && <p className="notice" role="alert">{error}</p>}
      {status && <p className="form-help" role="status">{status}</p>}
      <form className="panel-form admin-create" onSubmit={create}>
        <h3>Создать пользователя</h3>
        <fieldset disabled={busy || loading}>
          <label>Логин<input required minLength={1} maxLength={64} autoCapitalize="none" autoComplete="off" value={form.username} onChange={event => setForm({ ...form, username: event.target.value })}/></label>
          <label>Роль<select value={form.role} onChange={event => setForm({ ...form, role: event.target.value as UserRole })}><option value="user">Пользователь</option><option value="admin">Администратор</option></select></label>
          <div className="admin-password"><label>Пароль<input required maxLength={72} autoComplete="new-password" spellCheck={false} value={form.password} onChange={event => setForm({ ...form, password: event.target.value })}/></label>
            <button type="button" className="alt-action" onClick={() => setForm({ ...form, password: generatePassword() })}>Сгенерировать</button></div>
          <button className="alt-action" type="submit">Создать</button>
        </fieldset>
      </form>
      <div className="panel-heading admin-list-heading">
        <h3>Пользователи</h3><button type="button" className="alt-action" disabled={busy || loading} onClick={() => { setError(''); setLoading(true); setReload(value => value + 1) }}>Обновить</button>
      </div>
      {loading && <p className="form-help" role="status">Загрузка…</p>}
      {!loading && !users.length && !error && <p className="form-help">Пользователей пока нет.</p>}
      <ul className="admin-users">
        {users.map(user => <li key={user.id} className="admin-user">
          <div className="admin-user-heading"><strong>{user.username}{user.id === userId && ' (вы)'}</strong><div className="admin-badges"><span>{roleLabel[user.role]}</span><span data-active={user.isActive}>{user.isActive ? 'Активен' : 'Заблокирован'}</span></div></div>
          <p className="form-help">Устройств: {user.devicesCount} · Чатов: {user.chatsCount}</p>
          {user.id !== userId && <fieldset className="admin-actions" disabled={busy || loading}>
            <label className="admin-role">Роль<select aria-label={'Роль пользователя ' + user.username} value={user.role} onChange={event => {
              const role = event.target.value as UserRole
              void mutate(() => admin.update(user.id, { role }), 'Роль обновлена.')
            }}><option value="user">Пользователь</option><option value="admin">Администратор</option></select></label>
            <button type="button" className="alt-action" onClick={() => { setPassword(''); setAction({ user, type: 'password' }) }}>Сбросить пароль</button>
            <button type="button" className="alt-action" onClick={() => {
              if (user.isActive) setAction({ user, type: 'block' })
              else void mutate(() => admin.update(user.id, { isActive: true }), 'Пользователь разблокирован.')
            }}>{user.isActive ? 'Заблокировать' : 'Разблокировать'}</button>
            <button type="button" className="alt-action" onClick={() => setAction({ user, type: 'revoke' })}>Выгнать со всех устройств</button>
          </fieldset>}
          {action?.user.id === user.id && <form className="panel-form admin-confirm" onSubmit={confirm}>
            <fieldset disabled={busy}>
              {action.type === 'password' ? <div className="admin-password">
                <label>Новый пароль<input required maxLength={72} autoComplete="new-password" spellCheck={false} value={password} onChange={event => setPassword(event.target.value)}/></label>
                <button type="button" className="alt-action" onClick={() => setPassword(generatePassword())}>Сгенерировать</button>
              </div> : <p>{action.type === 'block' ? 'Заблокировать аккаунт и закрыть соединения?' : 'Завершить все сессии пользователя?'}</p>}
              <div className="admin-confirm-actions">
                <button className="alt-action" type="submit">{action.type === 'password' ? 'Сохранить пароль' : 'Подтвердить'}</button>
                <button className="alt-action" type="button" onClick={() => { setAction(null); setPassword('') }}>Отмена</button>
              </div>
            </fieldset>
          </form>}
        </li>)}
      </ul>
    </section>
  </dialog>
}
