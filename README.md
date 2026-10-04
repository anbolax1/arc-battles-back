# Битва за Респект — backend (Go)

Сервер сайта турниров по Arc Raiders: матчи, рейтинг MMR, профили игроков, кабинет организатора
и живой оверлей для OBS. Данные хранятся в PostgreSQL.

> **Сайт состоит из двух проектов в разных репозиториях:**
> - **backend** — этот репозиторий: сервер на Go и база данных;
> - **frontend** — [arc-battles-front](https://github.com/anbolax1/arc-battles-front): страницы сайта (Next.js).
>
> Чтобы открыть сайт у себя на компьютере, нужно запустить **оба**: сначала этот бэкенд (шаги ниже),
> потом фронтенд по инструкции из его README. Без бэкенда сайт откроется пустым, без фронтенда
> не будет страниц — только API.

## Как запустить у себя (пошагово)

Команды вводятся в терминале. На Windows используйте **Git Bash** — он ставится вместе с Git:
правый клик по папке → «Open Git Bash here».

### 1. Установите программы

| Программа | Зачем | Где взять | Как проверить |
|---|---|---|---|
| Git | скачать код | https://git-scm.com/downloads | `git --version` |
| Docker Desktop | база данных PostgreSQL | https://www.docker.com/products/docker-desktop | `docker --version` |
| Go 1.25 или новее | собрать и запустить сервер | https://go.dev/dl | `go version` |
| Node.js 20 или новее | нужен фронтенду | https://nodejs.org (версия LTS) | `node -v` |

Если `go version` показывает версию старше 1.25 — ничего страшного: при первом запуске Go сам
скачает нужную (нужен интернет).

Docker Desktop после установки нужно запустить и дождаться надписи, что он работает (Engine running).

### 2. Скачайте оба проекта в одну папку

```bash
mkdir arc-battles
cd arc-battles
git clone https://github.com/anbolax1/arc-battles-back.git backend
git clone https://github.com/anbolax1/arc-battles-front.git frontend
```

Должна получиться папка `arc-battles` с двумя папками внутри: `backend` и `frontend`.

### 3. Запустите базу данных

```bash
cd backend
docker compose up -d
```

Docker скачает PostgreSQL и запустит его в фоне. Проверка: команда `docker ps` показывает
контейнер `respect_db`. База слушает порт **5433** (не стандартный 5432 — чтобы не мешать другим
базам на компьютере). После перезагрузки компьютера достаточно снова запустить Docker Desktop —
контейнер поднимется сам.

### 4. Настройте бэкенд

Скопируйте пример настроек:

```bash
cp .env.example .env
```

Откройте файл `.env` в любом текстовом редакторе и заполните три строки:

```ini
JWT_SECRET=любая-случайная-строка-не-короче-32-символов
SUPERADMIN_PASSWORD=придумайте-пароль
FRONTEND_URL=http://localhost:3100
```

- `JWT_SECRET` — ключ, которым сервер подписывает входы на сайт. Подойдёт любая длинная случайная
  строка, например результат команды `openssl rand -base64 48`.
- `SUPERADMIN_PASSWORD` — пароль организатора. С ним вы войдёте на сайт под логином `Istwood`
  (логин задан в строке `SUPERADMIN_LOGIN`) и попадёте в кабинет.
- `FRONTEND_URL` — адрес, на котором откроется фронтенд по инструкции из его README.

Остальные строки трогать не нужно. Файл `.env` в git не попадает — пароли останутся только у вас.

### 5. Запустите сервер

```bash
go run ./cmd/server
```

Первый запуск занимает пару минут: Go скачивает библиотеки. Сервер готов, когда в терминале
появится строка `API слушает на :8080`. Таблицы в базе при этом создаются сами.

Проверка: откройте в браузере http://localhost:8080/api/health — должно показать `{"ok":true,...}`.

Терминал не закрывайте: пока он открыт, сервер работает. Остановить — `Ctrl+C`.

### 6. Загрузите данные с сайта (по желанию)

Сразу после запуска база пустая: на сайте не будет ни игроков, ни матчей. Можно скопировать к себе
все текущие данные с настоящего сайта brouhub.ru — матчи, игроков, рейтинг, теги и видео хайлайтов.
Для этого нужен доступ к серверу по SSH-ключу — его выдаёт владелец проекта.

В **новом** терминале (сервер из шага 5 пусть работает):

```bash
cd backend
./scripts/pull-prod.sh --media
```

- При первом подключении ssh спросит, доверять ли серверу, — ответьте `yes`.
- Без `--media` скопируется только база, без видео хайлайтов.
- Если ключ лежит не в стандартном месте: `PROD_SSH_KEY=путь/к/ключу ./scripts/pull-prod.sh --media`.

⚠️ Скрипт полностью заменяет локальную базу копией с сайта: всё, что вы меняли локально, пропадёт.

После загрузки перезапустите сервер (в его терминале `Ctrl+C`, затем снова `go run ./cmd/server`):
на старте он задаст организатору `Istwood` пароль из вашего `.env`.

### 7. Запустите фронтенд

Дальше — по инструкции из README фронтенда:
[arc-battles-front](https://github.com/anbolax1/arc-battles-front#как-запустить-у-себя-пошагово)
(это папка `frontend` рядом с этой). Коротко: `npm ci`, файл `.env.local`, `npm run dev` — и сайт
открывается на http://localhost:3100.

### Если что-то не работает

- **`docker compose` пишет, что не может подключиться к Docker** — не запущен Docker Desktop.
  Запустите его, дождитесь Engine running и повторите команду.
- **Сервер пишет `address already in use` (порт 8080 занят)** — на Windows этот порт иногда занимает
  сам Docker. Добавьте в `.env` строку `PORT=8081` и запустите сервер снова. Тогда и во фронтенде
  в `.env.local` укажите `8081` вместо `8080`.
- **Сервер ругается на `JWT_SECRET`** — строка в `.env` пустая или короче 32 символов.
- **`connection refused` при подключении к базе** — база не запущена: `docker compose up -d`
  в папке `backend`.
- **`pull-prod.sh` пишет `Permission denied (publickey)`** — нет доступа к серверу: попросите
  владельца добавить ваш SSH-ключ.
- **Хочется начать с чистой базы** — `docker compose down -v`, затем `docker compose up -d`
  (все локальные данные удалятся).

### Для разработчика

- Тесты: `go test ./...`. Интеграционные тесты запускаются на отдельной базе — создайте её один раз
  (`docker exec respect_db createdb -U respect respect_it`) и запускайте так:
  `RESPECT_TEST_DB=postgres://respect:respect@localhost:5433/respect_it?sslmode=disable go test ./...`
- Перед коммитом: `gofmt -l .` должно вывести пустоту, `go vet ./...` — без ошибок. То же проверяет CI
  перед выкладкой.
- Перенос матчей 3 сезона из таблицы организатора и с arcarena.ru:
  `go run ./cmd/importseason3 -csv /tmp/s3 -arena /tmp/arena -fetch`.

## Стек

- **Go** + **chi** (роутер)
- **PostgreSQL** + **pgx** (пул) + **goose** (встроенные миграции, применяются на старте)
- **Логин/пароль** (bcrypt, `golang.org/x/crypto`) + **JWT** в httpOnly-cookie + иерархический RBAC
- **coder/websocket** — рассылка состояния оверлея

## Вход по логину и паролю

- Регистрация: `POST /api/auth/register` `{login, password}` — создаёт обычного пользователя и заводит сессию.
- Вход: `POST /api/auth/login` `{login, password}` — заводит сессию (httpOnly-cookie `rsp_session`).
- Пароли — bcrypt-хеши (cost 12). Вход/регистрация троттлятся (по IP и по логину) — защита от перебора.
- Вход/выход реальны на сервере: `logout` двигает эпоху сессий (`users.tokens_valid_after`), старые токены становятся недействительными.
- Стойкий `JWT_SECRET` (≥32 символов) обязателен; слабый/дефолтный допустим только при `APP_ENV=dev`.
- IP для троттлинга берётся из TCP-соединения; за nginx включите `TRUST_PROXY=true` (тогда из `X-Real-IP`). Подделываемые заголовки (`True-Client-IP`, клиентский `X-Forwarded-For`) НЕ используются.

## Роли (иерархический RBAC)

Роли упорядочены по уровню; роль выше имеет все доступы ролей ниже (`models.Role.AtLeast`).
Уровни заданы с разрывами (10, 100), чтобы добавлять промежуточные роли без переписывания проверок.

- `user` (10) — обычный зарегистрированный пользователь (по умолчанию).
- `superadmin` (100) — организатор: полный доступ к управлению турнирами и оверлеем.

**Назначение ролей.** Открытая регистрация НИКОГДА не выдаёт superadmin (нет самоназначения). Первый
организатор создаётся при старте из `SUPERADMIN_LOGIN` + `SUPERADMIN_PASSWORD` (аккаунт обеспечивается
до приёма запросов — логин нельзя перехватить). Пароль организатора управляется через `SUPERADMIN_PASSWORD`:
он задаётся/обновляется на каждом старте (сменить пароль = поправить `.env` и перезапустить; пусто —
бутстрап пропускается). Дальше организатор назначает роли другим в кабинете (`PATCH /api/users/{id}/role`);
снять роль у последнего организатора нельзя.

## Эндпоинты

| Метод | Путь | Доступ | Назначение |
|-------|------|--------|-----------|
| GET | `/api/health` | все | проверка живости + число подключений оверлея |
| POST | `/api/auth/register` | все | регистрация `{login, password}` → сессия |
| POST | `/api/auth/login` | все | вход `{login, password}` → сессия |
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
| PATCH | `/api/users/{id}/role` | superadmin | назначить роль пользователю (`{role}`) |
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
internal/auth             — пароли (bcrypt) + JWT
internal/ws               — WebSocket-хаб рассылки
internal/api              — роутер, middleware (CORS/JWT/RBAC), хендлеры
```

## Оверлей в реальном времени

Организатор шлёт `PUT /api/overlay/state` (полный `LiveState`), сервер сохраняет его в таблицу
`live_state` и рассылает всем подключённым к `/api/ws/overlay` клиентам конверт
`{"type":"state","state":{…}}`. OBS-оверлей подключается к WS и получает текущее состояние сразу,
затем — каждое обновление. Это заменяет прежний `server_v2.js` + localStorage.

## Дальше

- Привязать к фронту (Next.js): страницы рейтинга/архива (SSR), вход по логину/паролю, кабинет организатора, страница `/overlay`.
- При необходимости — перейти со «строкового стейта оверлея» на вычисление из реляционных данных турнира.
