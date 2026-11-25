# Agent Console — передача контекста

## Статус

- React 19 / TypeScript / Vite / Zustand, Tauri v2 и опубликованный Telegram Mini App.
- Go-сервер поддерживает вход с ограничением частоты запросов, REST, WebSocket и восстановление истории. SQLite работает в режиме WAL. `MAX_CONCURRENT_STREAMS` ограничивает число одновременных потоков ответов.
- В режиме Server работает реальный `codex exec --json`, включая resume и динамический список моделей через `model/list`. Сквозной запрос из Telegram подтверждён.
- Раздел «Чаты Codex» показывает локальные обычные и архивные чаты владельца через Codex App Server. Проверены 46 чатов, включая 2 архивных. Импортированная история доступна для чтения.
- Для Telegram Mini App сервер доступен по постоянному HTTPS через Tailscale Funnel. [Agent Console Preview](https://t.me/AgentConsolePreview26Bot). Сборка для Sites размещена в отдельном checkout `telegram-demo-site/`.
- Go-сервер обслуживает режим «Только чат». Демо-учёт провайдеров и перевод квот доступны в MockTransport.
- Серверные лимиты за 5 ч и 7 д суммируют стоимость успешных ответов из `usage_events`. Шапка получает текущую квоту через `GET /api/quotas/current`, а базовые лимиты Pro задаются `PRO_LIMIT_5H_CENTS` и `PRO_LIMIT_7D_CENTS`.

## Запуск из корня проекта

```sh
(cd server && go run ./cmd/server create-user --username demo --password 'demo-password')
(cd server && SERVER_ADDR=127.0.0.1:8080 PROVIDER=stub go run ./cmd/server)
# Для настоящего Codex и собственного индекса вместо StubProvider:
(cd server && SERVER_ADDR=127.0.0.1:8080 PROVIDER=codex CODEX_HISTORY_USERNAME=demo go run ./cmd/server)
npm ci
npm run dev
npm run build
npm test
(cd server && go test ./...)
(cd server && go test -race ./...)
npm run tauri -- build --debug --bundles app
```

Фронтенд: http://127.0.0.1:1420/ → «Сервер» → http://127.0.0.1:8080. Codex должен быть установлен и авторизован на машине сервера. Для Telegram нужен доступный HTTPS-бэкенд и CORS_ORIGIN с точным origin Mini App. Настройки и права подробно описаны в AGENTS.md.

## Исправления истории Codex

- Go-парсер выносит вложения в `attachments`; интерфейс показывает их чипсами.
- `durationMs` отображается и без промежуточных сообщений. Для новых ответов значение записывается в SQLite и передаётся в WebSocket-событии `done`.

## Администрирование аккаунтов

- Роль `admin` выдаётся через `go run ./cmd/server set-role --username ИМЯ --role admin` из `server/` с тем же `DB_PATH`, что у запущенного сервера.
- После входа администратору доступна «Админка». Через неё можно создавать и блокировать аккаунты, менять роли, сбрасывать пароли и отзывать устройства.

## Устройства и WebSocket

- «Мои устройства» обновляет список через `GET /api/devices`. Отзыв через `DELETE /api/devices/{id}` завершает выбранную сессию. Выход отзывает токен текущего устройства через `POST /api/auth/logout` и очищает локальную сессию даже при недоступном сервере.
- Для каждого подключения клиент запрашивает одноразовый тикет через `POST /api/ws/ticket` с Bearer-токеном и передаёт тикет в `/ws?ticket=…`.

## Профиль и память

- «Настройки» → «Профиль и память» сохраняет имя, предпочтения и память через `GET/PATCH /api/profile`. В общие чаты передаётся имя автора сообщения.
- Фоновое сжатие личных чатов через CLI регулируют `MEMORY_AUTO_UPDATE` (true), `MEMORY_MODEL` (default), `MEMORY_CHECK_INTERVAL` (5m) и `MEMORY_MIN_NEW_MESSAGES` (6). Запуски используют общий лимит потоков и не записываются в квоту клиента.
