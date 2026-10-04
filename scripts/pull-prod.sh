#!/usr/bin/env bash
# Переносит данные с прода в локальное окружение: базу целиком в контейнер Postgres из
# docker-compose.yml, с флагом --media - ещё и файлы хайлайтов. Локальная база пересоздаётся.
#
#   ./scripts/pull-prod.sh [--media]
#
# PROD_HOST - пользователь и сервер (по умолчанию admin2@brouhub.ru), PROD_SSH_KEY - путь
# к ключу, если вход не ключом по умолчанию. LOCAL_DB, DB_CONTAINER, MEDIA_DIR - куда класть.
set -euo pipefail

cd "$(dirname "$0")/.."
PROD_HOST="${PROD_HOST:-admin2@brouhub.ru}"
LOCAL_DB="${LOCAL_DB:-respect}"
DB_CONTAINER="${DB_CONTAINER:-respect_db}"
MEDIA_DIR="${MEDIA_DIR:-./media}"
SSH=(ssh)
if [ -n "${PROD_SSH_KEY:-}" ]; then
  SSH+=(-i "$PROD_SSH_KEY" -o IdentitiesOnly=yes)
fi

dump="$(mktemp)"
trap 'rm -f "$dump"' EXIT

echo "Снимаю дамп прод-базы с $PROD_HOST..."
# Адрес базы берётся из .env бэкенда на сервере, в лог не попадает.
"${SSH[@]}" "$PROD_HOST" 'set -a; . ~/back/.env; set +a; pg_dump --no-owner --no-privileges "$DATABASE_URL"' >"$dump"
echo "Дамп: $(du -h "$dump" | cut -f1)"

echo "Пересоздаю локальную базу $LOCAL_DB в контейнере $DB_CONTAINER..."
docker exec "$DB_CONTAINER" dropdb -U respect --if-exists --force "$LOCAL_DB"
docker exec "$DB_CONTAINER" createdb -U respect "$LOCAL_DB"
docker exec -i "$DB_CONTAINER" psql -q -U respect -d "$LOCAL_DB" -v ON_ERROR_STOP=1 <"$dump" >/dev/null

if [ "${1:-}" = "--media" ]; then
  echo "Копирую файлы хайлайтов в $MEDIA_DIR..."
  mkdir -p "$MEDIA_DIR"
  "${SSH[@]}" "$PROD_HOST" 'tar -C ~/media -czf - .' | tar -C "$MEDIA_DIR" -xzf -
fi

echo "Готово. Перезапусти бэкенд: на старте он задаст организатору пароль из локального .env."
