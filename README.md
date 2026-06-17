# Битва за Респект — backend (Go)

API для сайта турниров по Arc Raiders: публичные данные, рейтинги, регистрация через Twitch,
кабинет организатора и живой стейт оверлея для OBS (по WebSocket).

## Стек

- **Go** + **chi** (роутер)
- **PostgreSQL** + **pgx** (пул) + **goose** (встроенные миграции, применяются на старте)
- **Twitch OAuth2** (`golang.org/x/oauth2`) + **JWT** в httpOnly-cookie + RBAC
- **coder/websocket** — рассылка состояния оверлея

## Быстрый старт

```bash
cd backend
cp .env.example .env          # заполни TWITCH_CLIENT_ID/SECRET (для логина)
docker compose up -d          # поднимет Postgres на :5433 (чтобы не конфликтовать с другим локальным PG)
go mod tidy
go run ./cmd/server           # миграции прогонятся автоматически
```

API: `http://localhost:8080/api`. Проверка: `GET /api/health`.

### Twitch OAuth
1. Создай приложение: https://dev.twitch.tv/console/apps
2. OAuth Redirect URL: `http://localhost:8080/api/auth/twitch/callback`
3. Впиши `TWITCH_CLIENT_ID` / `TWITCH_CLIENT_SECRET` в `.env`.
4. Свой Twitch-логин добавь в `ORGANIZER_TWITCH_LOGINS`, чтобы получить роль организатора при входе.

## Роли (RBAC)

- `viewer` — по умолчанию после входа.
- `participant` — выдаётся автоматически при одобрении заявки на турнир.
- `organizer` — логины из `ORGANIZER_TWITCH_LOGINS`; доступ к управлению турнирами и оверлеем.

## Эндпоинты

| Метод | Путь | Доступ | Назначение |
|-------|------|--------|-----------|
| GET | `/api/health` | все | проверка живости + число подключений оверлея |
| GET | `/api/auth/twitch/login` | все | редирект на Twitch OAuth |
| GET | `/api/auth/twitch/callback` | все | колбэк: создаёт сессию, редиректит на фронт |
| POST | `/api/auth/logout` | все | выход (сброс cookie) |
| GET | `/api/auth/me` | auth | текущий пользователь |
| PATCH | `/api/me` | auth | обновить Embark ID |
| GET | `/api/me/registrations` | auth | мои заявки |
| GET | `/api/tournaments` | все | список турниров (`?status=`) |
| GET | `/api/tournaments/{id}` | все | турнир с участниками и раундами |
| POST | `/api/tournaments/{id}/register` | auth | подать заявку (Embark ID, заметка) |
| GET | `/api/leaderboard?mode=1x1\|2x2` | все | рейтинг |
| GET | `/api/rules` | все | задания (пул бонусных) и усложнения с типом значения |
| GET | `/api/overlay/state` | все | текущее состояние оверлея |
| GET | `/api/ws/overlay` | все | WebSocket: поток состояния для OBS |
| POST | `/api/tournaments` | organizer | создать турнир |
| PATCH | `/api/tournaments/{id}` | organizer | статус / победитель |
| POST | `/api/tournaments/{id}/participants` | organizer | добавить участника/команду |
| POST | `/api/tournaments/{id}/rounds` | organizer | создать/обновить раунд |
| PATCH | `/api/rounds/{id}` | organizer | статус/карта раунда (B2) |
| PUT | `/api/rounds/{id}/entries/{participantId}` | organizer | результат участника в раунде → пересчёт очков (B2) |
| GET | `/api/rounds/{id}/entries` | organizer | результаты раунда (B2) |
| PATCH | `/api/participants/{id}` | organizer | правка участника: очки/имя/состав (B1) |
| DELETE | `/api/participants/{id}` | organizer | удалить участника |
| GET | `/api/tournaments/{id}/registrations` | organizer | заявки турнира |
| POST | `/api/registrations/{id}/decide` | organizer | `{status: accepted\|declined}` |
| PUT | `/api/overlay/state` | organizer | заменить стейт оверлея (+рассылка по WS) |
| POST | `/api/catalog/tasks` | organizer | добавить задание |
| PATCH | `/api/catalog/tasks/{id}` | organizer | изменить задание |
| DELETE | `/api/catalog/tasks/{id}` | organizer | удалить задание |
| POST | `/api/catalog/complications` | organizer | добавить усложнение |
| PATCH | `/api/catalog/complications/{id}` | organizer | изменить усложнение |
| DELETE | `/api/catalog/complications/{id}` | organizer | удалить усложнение |

### Баллы и проценты

У задания и усложнения есть поле `valueType`:
- `fixed` — `points`/`penalty` это число баллов (например, усложнение −1 балл);
- `percent` — это процент (0..100) от **текущих** очков участника в турнире
  (например, усложнение со штрафом 10% снимет 10% набранных баллов).

Фактическая величина считается в момент начисления: `models.EffectiveValue` /
`CatalogTask.Reward(current)` / `CatalogComplication.PenaltyFor(current)`.
Поля `source` (`official|boosty`), `author`, `title` хранят авторство заданий/усложнений
от подписчиков Boosty.

## Структура

```
cmd/server/main.go        — точка входа, graceful shutdown
internal/config           — конфиг из env/.env
internal/db               — пул pgx + встроенные миграции (goose)
internal/db/migrations    — *.sql (схема + сид справочников из правил турнира)
internal/models           — доменные типы
internal/store            — слой доступа к данным (pgx)
internal/auth             — Twitch OAuth2 + JWT
internal/ws               — WebSocket-хаб рассылки
internal/api              — роутер, middleware (CORS/JWT/RBAC), хендлеры
```

## Оверлей в реальном времени

Организатор шлёт `PUT /api/overlay/state` (полный `LiveState`), сервер сохраняет его в таблицу
`live_state` и рассылает всем подключённым к `/api/ws/overlay` клиентам конверт
`{"type":"state","state":{…}}`. OBS-оверлей подключается к WS и получает текущее состояние сразу,
затем — каждое обновление. Это заменяет прежний `server_v2.js` + localStorage.

## Дальше

- Привязать к фронту (Next.js): страницы рейтинга/архива (SSR), вход через Twitch, кабинет организатора, страница `/overlay`.
- При необходимости — перейти со «строкового стейта оверлея» на вычисление из реляционных данных турнира.
