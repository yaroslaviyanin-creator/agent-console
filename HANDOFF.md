# Agent Console — передача контекста

## Статус

- React 19 / TypeScript / Vite / Zustand, Tauri v2 и опубликованный Telegram Mini App.
- Go-сервер поддерживает вход, устройства с хешированными токенами, REST, WebSocket, SQLite и восстановление истории.
- В режиме Server работает реальный `codex exec --json`, включая resume и динамический список моделей через `model/list`. Сквозной запрос из Telegram подтверждён.
- Раздел «Чаты Codex» показывает локальные обычные и архивные чаты владельца через Codex App Server. Проверены 46 чатов, включая 2 архивных. Импортированная история доступна для чтения.
- Telegram: https://t.me/AgentConsolePreview26Bot. Временный HTTPS из сессии рассчитан до 11:19 по Екатеринбургу 22 ноября, постоянного адреса пока нет. Сборка для Sites размещена в отдельном checkout `telegram-demo-site/`.
- MCP, права совместного доступа, квоты и биллинг доступны в MockTransport. Go-сервер обслуживает режим «Только чат».

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

## Баг 1 — служебный текст вложения

- `server/internal/provider/history.go` → `splitIndexedTurn`. Берёт текст из `userMessage.content` целиком. В воспроизведённом ходе текст начинается с `# Files mentioned by the user:`, содержит локальный путь и служебную инструкцию, затем `## My request:` и реальный текст. Изображение сохранено отдельным элементом `localImage`.
- `server/internal/httpapi/history.go` → `indexedMessages` отдаёт этот текст как user message. `src/app/chatStore.ts` → `restoreTurns` переносит его в `turn.prompt`. `src/app/App.tsx` → `Transcript` выводит его в `.user-prompt`.
- Следующий шаг: распознать известную обёртку Codex у начала текста и сохранить тело после `## My request:`. Нужен узкий регрессионный тест в `server/internal/provider/history_test.go`. Обычный Markdown пользователя должен сохраниться.

## Баг 2 — пропала строка длительности

- `server/internal/provider/history.go` → `splitIndexedTurn` читает `phase`, статус и длительность. В ходе со скриншота Codex вернул `completed`, пять `commentary`, один `final_answer` и `durationMs: 276608` (4 мин 36 с).
- `server/internal/httpapi/history.go` → `indexedMessages`, `server/internal/store/store.go` → `Message` и `src/protocol/types.ts` → `ServerMessage` передают `progress` и `durationMs`.
- `src/app/chatStore.ts` → `restoreTurns`; `src/app/App.tsx` → `Transcript` и `progressLabel`; `src/style.css` → `.response-progress`.
- Заголовок сейчас отображается только при наличии `turn.progress`. Длительность в исходном ходе подтверждена. Причину отсутствия именно у пользователя ещё не изолировали. Проверь тот же ход через опубликованный HTTPS API и в актуально загруженном Mini App, затем проверь отдельный случай с длительностью и пустым progress.
- Исправления этих двух случаев ещё не внесены и не опубликованы.
