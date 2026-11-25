# Agent Console — передача контекста

## Статус

- React 19 / TypeScript / Vite / Zustand, Tauri v2 и опубликованный Telegram Mini App.
- Go-сервер поддерживает вход с ограничением частоты запросов, REST, WebSocket и восстановление истории. SQLite работает в режиме WAL. `MAX_CONCURRENT_STREAMS` ограничивает число одновременных потоков ответов.
- В режиме Server работает реальный `codex exec --json`, включая resume и динамический список моделей через `model/list`. Сквозной запрос из Telegram подтверждён.
- Поддерживается серверный провайдер `PROVIDER=antigravity` через локальный Antigravity CLI (`ANTIGRAVITY_BIN=agy`, модель `ANTIGRAVITY_MODEL=gemini-3.8-flash-high`). Провайдер использует авторизованный аккаунт Antigravity и его квоты, поддерживает сохранение и продолжение сессий (`--conversation`), потоковый вывод `stream-json`, sandbox-режим «Только чат» и каталог моделей через `GET /api/models` (`agy models`). Неподдерживаемые/нереализованные провайдеры (`claude`, `paid_api`) отклоняются при старте (`config.Load` / `httpapi.New`) без фоллбэка на `StubProvider`.
- Поддерживаемые идентификаторы пользовательских провайдеров в протоколе и БД: `antigravity`, `codex`, `claude`, `paid_api` (плюс `stub` для dev/тестов).
- Раздел «Чаты Codex» показывает локальные обычные и архивные чаты владельца через Codex App Server. Проверены 46 чатов, включая 2 архивных. Импортированная история доступна для чтения.
- Для Telegram Mini App сервер доступен по постоянному HTTPS через Tailscale Funnel. [Agent Console Preview](https://t.me/AgentConsolePreview26Bot). Сборка для Sites размещена в отдельном checkout `telegram-demo-site/`.
- Go-сервер обслуживает режим «Только чат», включая общий доступ к чатам (`read`/`fork`/`write`, `PUT/DELETE /api/chats/{id}/shares/{userId}`, `POST /api/chats/{id}/fork`, `GET /api/users`), тарифные планы (`none`/`pro`/`2x_pro`/`unlimited`) и серверный учёт квот. Инструменты устройств (MCP) и переводы квот между пользователями остаются сценариями `MockTransport`.
- Серверные лимиты за 5 ч и 7 д суммируют стоимость успешных ответов из `usage_events`. Шапка получает текущую квоту через `GET /api/quotas/current`, а базовые лимиты Pro задаются `PRO_LIMIT_5H_CENTS` и `PRO_LIMIT_7D_CENTS`.

## Запуск из корня проекта

```sh
(cd server && go run ./cmd/server create-user --username demo --password 'demo-password')
(cd server && SERVER_ADDR=127.0.0.1:8080 PROVIDER=stub go run ./cmd/server)
# Для Antigravity CLI (agy):
(cd server && SERVER_ADDR=127.0.0.1:8080 PROVIDER=antigravity ANTIGRAVITY_BIN=agy ANTIGRAVITY_MODEL=gemini-3.8-flash-high go run ./cmd/server)
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

Фронтенд: http://127.0.0.1:1420/ → «Сервер» → http://127.0.0.1:8080. Codex или Antigravity должны быть установлены и авторизованы на машине сервера. Для Telegram нужен доступный HTTPS-бэкенд и CORS_ORIGIN с точным origin Mini App. Настройки и права подробно описаны в AGENTS.md.

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

## Docker

- Локальный запуск (Windows / macOS / Linux): `cp .env.example .env && docker compose up -d --build` (веб-клиент на `http://127.0.0.1:3000`, бэкенд на `http://127.0.0.1:8080`). На Apple Silicon (ARM64) для работы с анлокером раскомментируйте `platform: linux/amd64` в `docker-compose.yml` или соберите нативный ARM64-образ со `stub`/другими провайдерами через `--build-arg INSTALL_AG_UNLOCKER=false`.
- Встроенный Antigravity Unlocker (`AG_UNLOCKER_ENABLED=true`, зафиксированная версия `AG_UNLOCKER_VERSION=v2.35.0`) используется как локально, так и в продакшене (VPS в Австрии): при старте контейнера `backend` автоматически патчит `agy` (`ineligible` → `inexigible`, `https_proxy` → `AG_LS_PROXY`), запускает `ag_unlocker --proxy` в supervisor-цикле на `127.0.0.1:53129` (с пересылкой `SIGTERM`/`SIGINT` и проверкой порта в `healthcheck`) и экспортирует `AG_LS_PROXY`. При `PROVIDER=antigravity` и `AG_UNLOCKER_ENABLED=true` ошибка патча или запуска прокси завершает контейнер с ненулевым кодом.
- Код прокси из Telegram (`t.me/nova_txt`): при необходимости задайте `AG_UNLOCKER_CODE=agk1....` в `.env` (не коммитя `.env` в Git) и выполните `docker compose up -d backend`. Настройки анлокера сохраняются в volume `ag_unlocker_data` (`/home/app/.local/share/agunlocker`).
- При переезде на другой компьютер или сервер: ключ `agk1....` привязан к `machine-id`/UUID хоста; получите новый код для новой машины в Telegram-группе/боте, обновите `AG_UNLOCKER_CODE` в `.env` и перезапустите `docker compose up -d backend`.
- Одноразовая авторизация Antigravity CLI (`agy`) внутри контейнера (данные сохраняются в volume `agy_auth`): `docker compose exec -it backend agy`.
- Горячий бэкап SQLite из volume:
  ```sh
  docker compose exec backend sqlite3 /app/data/app.db ".backup /tmp/backup.db"
  docker compose cp backend:/tmp/backup.db ./backup.db
  ```
- Деплой на Linux VPS (Австрия) с автоматическим HTTPS через Caddy, строгим `CORS_ORIGIN=https://${DOMAIN}` и включённым анлокером (`AG_UNLOCKER_ENABLED=true`): `docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d --build` (предварительно задайте `DOMAIN` и при необходимости `AG_UNLOCKER_CODE` в `.env`).
