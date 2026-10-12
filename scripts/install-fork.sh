#!/usr/bin/env bash
#
# mikan-форк: одна команда вместо прежней, переход с обычного mikan на форк без потерь.
#
#   curl -fsSL https://raw.githubusercontent.com/<твой-репозиторий>/<ветка>/scripts/install-fork.sh \
#     | sudo bash -s -- --image ghcr.io/<твой-репозиторий>/mikan:0.5.99.99
#
# Что он делает и почему так:
#
#   * Переносить данные не нужно. Форк добавляет одну миграцию — 0016_referrals.sql, пять
#     ПУСТЫХ таблиц; ни одна существующая строка не переписывается. Панель на старте сама
#     применяет недостающие миграции (serve.go → store.OpenPostgres → migrateUp). Поэтому
#     здесь нет «переливки данных»: есть безопасное переключение образа с резервной копией.
#
#   * Порядок отката противоречит интуиции: образ переключается на обычный mikan ПЕРЕД
#     возвратом базы. «database restore» после загрузки копии поднимает схему до версии
#     бинаря, который её выполняет (store/restore.go, RestorePostgres → migrateUp). Вернуть
#     базу бинарём форка — значит тут же накатить 0016 обратно, и обычный mikan откажется
#     стартовать («schema 16 is newer than this binary supports»).
#
#   * Главная опасность форка — не миграция, а автообновление: адрес релизов зашит в коде
#     и указывает на апстрим (internal/release/release.go, Repo). Панель форка сравнивает
#     свою версию с ЕГО релизами и, если её версия не новее, заменит себя обычным образом.
#     То же сделает с удалёнными нодами при включённом nodes_follow_panel. Скрипт сравнивает
#     версии, предупреждает и выключает оба переключателя.
#
#   * Отдельный установщик с нуля не переписывается: для чистой установки сначала пройди
#     штатный install.sh (он ставит Docker, файрвол, compose и саму панель), потом запусти
#     эту команду ещё раз — она переключит уже установленный mikan на форк.

set -Eeuo pipefail

MIKAN_DIR=${MIKAN_DIR:-/opt/mikan}
FORK_IMAGE=${FORK_IMAGE:-}
STOCK_IMAGE=${STOCK_IMAGE:-ghcr.io/miroshka000/mikan:latest}
BACKUP=${BACKUP:-}
DRY_RUN=0
KEEP_AUTO_UPDATE=0
ASSUME_YES=0
WAIT_SECONDS=${WAIT_SECONDS:-180}

die() { printf '\n\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }
step() { printf '\n\033[1;36m▸ %s\033[0m\n' "$*"; }
ok() { printf '  \033[32m✓\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
info() { printf '  \033[2m%s\033[0m\n' "$*"; }
run() { # run <описание> <команда...>
  if [ "$DRY_RUN" = 1 ]; then
    printf '  \033[2m(пробный запуск) %s\033[0m\n' "$1"
    return 0
  fi
  printf '  %s … ' "$1"; shift
  if "$@"; then printf '\033[32mок\033[0m\n'; else printf '\033[31mошибка\033[0m\n'; return 1; fi
}
usage() {
  # Шапка скрипта и есть справка: со строк комментариев снимается решётка.
  sed -n '2,/^$/p' "$0" | sed 's/^#//' | sed 's/^ //' | sed '/^$/d'
  cat <<'EOF'

Ключи:
  --image ОБРАЗ          образ форка (или FORK_IMAGE=... в окружении)
  --rollback АРХИВ        вернуться на обычный mikan из копии базы
  --dry-run              ничего не менять, только показать
  --keep-auto-update     не выключать автообновление (обычно не надо)
  --yes                  не спрашивать подтверждения
Примеры:
  curl -fsSL <url> | sudo bash -s -- --image ghcr.io/o0hhs/mikan:0.5.99.99
  curl -fsSL <url> | sudo bash -s -- --rollback /opt/mikan/data/panel/backup-*.dump
EOF
}

while [ $# -gt 0 ]; do
  case $1 in
    --image)
      [ $# -ge 2 ] || die "--image требует значение"
      FORK_IMAGE=$2; shift 2 ;;
    --image=*) FORK_IMAGE=${1#*=}; shift ;;
    --rollback)
      [ $# -ge 2 ] || die "--rollback требует значение"
      BACKUP=$2; shift 2 ;;
    --rollback=*) BACKUP=${1#*=}; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    --keep-auto-update) KEEP_AUTO_UPDATE=1; shift ;;
    --yes|-y) ASSUME_YES=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "неизвестный ключ: $1 (--help)" ;;
  esac
done

[ "$(id -u)" = 0 ] || die "запусти от root: curl -fsSL <url> | sudo bash -s -- --image <образ>"
command -v docker >/dev/null || die "docker не найден"
docker compose version >/dev/null 2>&1 || die "нужен docker compose v2"

dc() { docker compose --project-directory "$MIKAN_DIR" -f "$MIKAN_DIR/compose.yaml" "$@"; }
env_get() { sed -n "s/^$1=//p" "$MIKAN_DIR/.env" | head -n1; }
# Кто и что в базе: compose инсталлера задаёт их константами, но читать их из .env надёжнее.
DBPASS=$(env_get MIKAN_POSTGRES_PASSWORD)
DBUSER=$(env_get MIKAN_POSTGRES_USER); DBUSER=${DBUSER:-mikan}
DBNAME=$(env_get MIKAN_POSTGRES_DB); DBNAME=${DBNAME:-mikan}
SQL_ERR=""
env_set() { # в пробном запуске не пишет ничего
  local key=$1 val=$2
  [ "$DRY_RUN" = 1 ] && return 0
  if grep -q "^$key=" "$MIKAN_DIR/.env"; then
    sed -i.bak "s|^$key=.*|$key=$val|" "$MIKAN_DIR/.env"
  else
    printf '%s=%s\n' "$key" "$val" >>"$MIKAN_DIR/.env"
  fi
}
# psql через контейнер базы, а не через панель: панель может быть остановлена (её как раз и
# собираемся менять), а psql в образе postgres есть всегда. Если не вышло — пробуем панель.
sql() { # sql ЗАПРОС → строка ответа; при ошибке возвращает 1, а саму ошибку — в SQL_ERR
  local out
  if out=$(dc exec -T -e PGPASSWORD="$DBPASS" postgres psql -U "$DBUSER" -d "$DBNAME" -tAX -v ON_ERROR_STOP=1 -c "$1" 2>&1); then
    printf '%s' "$out" | tr -d '[:space:]'
    return 0
  fi
  if out=$(dc exec -T panel sh -c 'psql "$MIKAN_DATABASE_URL" -tAX -v ON_ERROR_STOP=1 -c "$1"' _ "$1" 2>&1); then
    printf '%s' "$out" | tr -d '[:space:]'
    return 0
  fi
  SQL_ERR="$out"
  return 1
}
newer_than() { # newer_than A B → 0, если A строго новее B
  local a b
  a=$(printf '%s' "${1#v}" | awk -F'[.-]' '{printf "%d.%d.%d.%d", $1, $2, $3, ($4 ~ /^[0-9]+$/ ? $4 : 0)}')
  b=$(printf '%s' "${2#v}" | awk -F'[.-]' '{printf "%d.%d.%d.%d", $1, $2, $3, ($4 ~ /^[0-9]+$/ ? $4 : 0)}')
  [ -n "$a" ] && [ -n "$b" ] || return 1
  [ "$a" = "$b" ] && return 1
  [ "$(printf '%s\n%s\n' "$b" "$a" | sort -V | tail -n1)" = "$a" ]
}
switch_off() { # выключает переключатель, только если он был включён; 0 — был и выключен
  local was
  was=$(sql "SELECT value FROM settings WHERE key='$1'" 2>/dev/null)
  if [ "$was" = "true" ]; then
    sql "UPDATE settings SET value='false' WHERE key='$1'" >/dev/null
    warn "$1 был включён и выключен"
    return 0
  fi
  return 1
}

# ─────────────────────────────── откат на обычный mikan ───────────────────────────────
if [ -n "$BACKUP" ]; then
  [ -f "$BACKUP" ] || die "нет файла $BACKUP"
  head -c 5 "$BACKUP" | grep -q PGDMP || die "$BACKUP не архив PostgreSQL (нужен pg_dump --format=custom)"
  [ -f "$MIKAN_DIR/compose.yaml" ] || die "не найден $MIKAN_DIR/compose.yaml"
  BASE=$(basename "$BACKUP")
  [ -f "$MIKAN_DIR/data/panel/$BASE" ] || die "архив вне $MIKAN_DIR/data/panel — положи его туда: cp '$BACKUP' '$MIKAN_DIR/data/panel/'"

  printf 'Сейчас образ: %s\nВозврат к:   %s\nБаза из:    %s\n' "$(env_get MIKAN_IMAGE)" "$STOCK_IMAGE" "$BASE"
  printf 'Все платежи, клиенты и тарифы вернутся к состоянию на момент копии.\n'
  printf 'Реферальная программа (таблицы referral_*) исчезнет: её создаёт форк.\n'
  if [ "$ASSUME_YES" = 0 ] && [ "$DRY_RUN" = 0 ]; then
    read -r -p 'Продолжить? (yes/no) ' answer
    [ "$answer" = "yes" ] || die "отменено"
  fi
  [ "$DRY_RUN" = 1 ] || cp -a "$MIKAN_DIR/.env" "$MIKAN_DIR/.env.rollback-$(date +%Y%m%d-%H%M%S)"

  step "Откат 1/3. Образ обычного mikan — ДО возврата базы"
  env_set MIKAN_IMAGE "$STOCK_IMAGE"
  [ "$DRY_RUN" = 1 ] || docker pull "$STOCK_IMAGE" >/dev/null 2>&1 || docker image inspect "$STOCK_IMAGE" >/dev/null 2>&1 || die "нет образа $STOCK_IMAGE"
  ok "MIKAN_IMAGE=$STOCK_IMAGE"

  step "Откат 2/3. База из копии"
  dc stop panel node >/dev/null 2>&1 || true
  if [ "$DRY_RUN" = 1 ]; then
    info "(пробный запуск) pg_restore обычным бинарём"
  elif dc run --rm --no-deps -T panel database restore "/data/panel/$BASE" >/tmp/mikan-restore.log 2>&1; then
    ok "база возвращена и поднята до схемы обычного mikan"
  else
    sed -n '1,20p' /tmp/mikan-restore.log >&2
    die "возврат не удался — база не тронута, прежняя схема на месте"
  fi

  step "Откат 3/3. Запуск и проверка"
  dc up -d --wait --wait-timeout "$WAIT_SECONDS" || warn "стек не стал здоровым — docker compose logs panel"
  [ "$DRY_RUN" = 1 ] && exit 0
  sleep 5
  dc exec -T panel mikan health >/dev/null 2>&1 || { dc logs panel --tail 15 2>&1 | sed 's/^/    /' >&2; die "панель не поднялась — база на месте, логи выше"; }
  ok "панель отвечает"
  SCHEMA=$(sql "SELECT COALESCE(max(version_id),0) FROM goose_db_version WHERE is_applied" 2>/dev/null)
  ok "схема: ${SCHEMA:-?}"
  printf '\n\033[32mГотово.\033[0m Панель на обычном mikan, база — на состояние копии %s.\n' "$BASE"
  exit 0
fi

# ─────────────────────────────── переход на форк ───────────────────────────────
if [ ! -f "$MIKAN_DIR/compose.yaml" ]; then
  cat >&2 <<EOF

mikan не найден в $MIKAN_DIR.

Форк — это тот же mikan, только с реферальной программой, поэтому с нуля его ставят так же,
как обычный mikan, а потом переключают образ:

  1) установка обычного mikan (Docker, файрвол, compose, панель):
     curl -fsSL https://github.com/Miroshka000/mikan/releases/latest/download/install.sh | sudo bash

  2) переход на форк (эта же команда, уже установленного mikan):
     curl -fsSL <url-этого-скрипта> | sudo bash -s -- --image <образ-форка>

EOF
  exit 1
fi
[ -f "$MIKAN_DIR/.env" ] || die "не найден $MIKAN_DIR/.env"

[ -n "$FORK_IMAGE" ] || die "укажи образ форка: --image ghcr.io/<репозиторий>/mikan:<тег> (или FORK_IMAGE=...)"

STAMP=$(date +%Y%m%d-%H%M%S)
# Копия для отката — ПЕРВАЯ, снятая до перехода. Копии, снятые позже, уже содержат
# миграцию форка, и обычный mikan на них не поднимется («schema 16 is newer than this
# binary supports»), поэтому на повторных запусках новую не делаем, а помним первую.
FIRST_BACKUP=""
# `ls` без совпадений возвращает 1, а с pipefail и set -e это молча оборвало бы весь
# скрипт, поэтому пустой результат — обычный случай, а не ошибка.
for f in "$MIKAN_DIR"/data/panel/backup-*-before-fork.dump; do
  [ -e "$f" ] || break
  FIRST_BACKUP="$f"; break
done
if [ -n "$FIRST_BACKUP" ]; then
  BACKUP_NAME=$(basename "$FIRST_BACKUP"); BACKUP_HOST=$FIRST_BACKUP
  [ "$DRY_RUN" = 0 ] && ok "копия для отката уже есть: $BACKUP_NAME"
else
  BACKUP_NAME="backup-${STAMP}-before-fork.dump"
  BACKUP_HOST="$MIKAN_DIR/data/panel/$BACKUP_NAME"
fi
ENV_BACKUP="$MIKAN_DIR/.env.before-fork-${STAMP}"

OLD_IMAGE=$(env_get MIKAN_IMAGE)
[ -n "$OLD_IMAGE" ] || die "в .env нет MIKAN_IMAGE — не знаю, что сейчас запущено"
# Повторный запуск не должен забыть, куда откатываться: образ, с которого ушли, запоминается
# один раз и дальше переиспользуется.
STATE_FILE="$MIKAN_DIR/.fork-original-image"
if [ -f "$STATE_FILE" ]; then
  SAVED=$(cat "$STATE_FILE")
  OLD_IMAGE=$SAVED
  warn "скрипт уже запускался: прежним образом считается $OLD_IMAGE"
elif [ "$DRY_RUN" != 1 ]; then
  printf '%s\n' "$OLD_IMAGE" >"$STATE_FILE"
fi

step "1/6. Что стоит сейчас"
info "каталог $MIKAN_DIR · образ $OLD_IMAGE"

# Стек может быть не поднят — тогда читать схему нечем. Поднимаем базу и пробуем ещё раз.
# Время ограничено: сервер без доступа к реестру иначе висел бы тут минутами.
if ! dc ps --status running --services 2>/dev/null | grep -qx postgres; then
  warn "база не запущена — поднимаю (не дольше двух минут)"
  if command -v timeout >/dev/null 2>&1; then
    timeout 60 dc up -d postgres >/dev/null 2>&1 || true
  else
    dc up -d postgres >/dev/null 2>&1 || true
  fi
  for _ in $(seq 1 20); do
    dc exec -T postgres pg_isready -q >/dev/null 2>&1 && break
    sleep 3
  done
fi
if ! BEFORE_SCHEMA=$(sql "SELECT COALESCE(max(version_id),0) FROM goose_db_version WHERE is_applied"); then
  printf '\n  \033[31mне читаю базу.\033[0m\n'
  if [ -n "$SQL_ERR" ]; then printf '%s\n' "$SQL_ERR" | sed 's/^/    /' | head -8; else printf '    \033[2m(postgres и панель не ответили)\033[0m\n'; fi
  printf '\n  \033[2mсостояние стека:\033[0m\n'
  dc ps 2>&1 | sed 's/^/    /'
  printf '\n'
  die "база недоступна. Если панель не запущена — cd $MIKAN_DIR && docker compose up -d, потом повтори."
fi
USERS=$(sql "SELECT count(*) FROM users")
TARIFFS=$(sql "SELECT count(*) FROM tariffs")
PAYMENTS=$(sql "SELECT count(*) FROM payments")
ok "схема PostgreSQL $BEFORE_SCHEMA · $USERS клиентов · $TARIFFS тарифов · $PAYMENTS платежей"

step "2/6. Защита от тихого отката на обычный mikan"
STOCK_VERSION=$(dc exec -T panel mikan version 2>/dev/null | tr -d '[:space:]' || true)
FORK_VERSION=$(docker run --rm --entrypoint mikan "$FORK_IMAGE" version 2>/dev/null | tr -d '[:space:]' || true)
ok "сейчас ${STOCK_VERSION:-?}, в форке ${FORK_VERSION:-?}"
if [ -n "$STOCK_VERSION" ] && [ -n "$FORK_VERSION" ] && ! newer_than "$FORK_VERSION" "$STOCK_VERSION"; then
  warn "версия форка не новее стоящей — форк сочтёт обычный mikan обновлением и вернётся на него"
  warn "пересобери образ: docker buildx build --build-arg VERSION=0.5.99.99 -t <образ> . && docker push <образ>"
fi
if [ "$KEEP_AUTO_UPDATE" = 1 ]; then
  warn "автообновление оставлено как есть (--keep-auto-update)"
else
  switch_off auto_update || ok "автообновление выключено"
  switch_off nodes_follow_panel || ok "автообновление нод выключено"
fi

step "3/6. Резервная копия"
if [ "$DRY_RUN" = 1 ]; then
  info "(пробный запуск) копия .env и pg_dump всей схемы в $BACKUP_NAME"
else
  cp -a "$MIKAN_DIR/.env" "$ENV_BACKUP"
  if [ ! -e "$BACKUP_HOST" ]; then
    printf '  pg_dump … '
    dc run --rm --no-deps -T panel database backup "/data/panel/$BACKUP_NAME" >/dev/null ||
      { printf '\033[31mошибка\033[0m\n'; die "резервная копия не удалась — не продолжаю"; }
    printf '\033[32mок\033[0m\n'
  fi
  [ -s "$BACKUP_HOST" ] || die "копия пуста"
  head -c 5 "$BACKUP_HOST" | grep -q PGDMP || die "$BACKUP_HOST не архив PostgreSQL"
  ok "база: $BACKUP_HOST ($(du -h "$BACKUP_HOST" | cut -f1))"
  ok ".env:  $(basename "$ENV_BACKUP")"
fi

step "4/6. Переключение на образ форка"
if [ "$DRY_RUN" = 1 ]; then
  info "(пробный запуск) MIKAN_IMAGE=$FORK_IMAGE"
else
  env_set MIKAN_IMAGE "$FORK_IMAGE"
  ok "MIKAN_IMAGE=$FORK_IMAGE"
fi
if docker image inspect "$FORK_IMAGE" >/dev/null 2>&1; then
  ok "образ есть локально, тянуть нечего"
else
  run "подтянуть образ" dc pull panel node
fi
run "перезапустить стек" dc up -d --wait --wait-timeout "$WAIT_SECONDS"

step "5/6. Проверка"
FAILED=0
if [ "$DRY_RUN" = 1 ]; then
  info "(пробный запуск) проверка пропущена"
else
  sleep 5
  dc exec -T panel mikan health >/dev/null 2>&1 ||
    { warn "health не проходит, панель ещё поднимается"; sleep 20; dc exec -T panel mikan health >/dev/null 2>&1 || FAILED=1; }
  AFTER=$(sql "SELECT COALESCE(max(version_id),0) FROM goose_db_version WHERE is_applied" 2>/dev/null)
  if [ -n "$AFTER" ] && [ "$AFTER" -ge 16 ] 2>/dev/null; then ok "схема $BEFORE_SCHEMA → $AFTER"; else warn "схема ${AFTER:-?}, ожидалось ≥ 16"; FAILED=1; fi
  T=$(sql "SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name LIKE 'referral%'")
  [ "$T" = "5" ] && ok "пять таблиц referral_* созданы" || { warn "таблиц referral_*: ${T:-0}"; FAILED=1; }
  for pair in "users:$USERS" "tariffs:$TARIFFS"; do
    table=${pair%%:*}; want=${pair##*:}
    got=$(sql "SELECT count(*) FROM $table")
    [ "$got" = "$want" ] && ok "$table на месте: $got" || { warn "$table: стало $got, было $want"; FAILED=1; }
  done
  P=$(sql "SELECT count(*) FROM payments")
  [ -n "$P" ] && [ "$P" -ge "$PAYMENTS" ] 2>/dev/null && ok "платежей на месте: $P" || { warn "платежей стало ${P:-0}, было $PAYMENTS"; FAILED=1; }
fi

if [ "$FAILED" = 1 ] && [ "$DRY_RUN" != 1 ]; then
  printf '\n\033[31m✗ проверка не прошла — откатываюсь на %s\033[0m\n' "$OLD_IMAGE"
  env_set MIKAN_IMAGE "$OLD_IMAGE"
  dc stop panel node >/dev/null 2>&1 || true
  dc run --rm --no-deps -T panel database restore "/data/panel/$BACKUP_NAME" ||
    die "откат не удался: верни $ENV_BACKUP и $BACKUP_HOST вручную"
  dc up -d --wait --wait-timeout "$WAIT_SECONDS" || true
  die "откат выполнен, панель на обычном mikan"
fi

step "6/6. Готово"
[ "$DRY_RUN" = 1 ] || info "$(dc ps --format '{{.Service}}: {{.Status}}' 2>/dev/null | tr '\n' ' ')"
cat <<EOF

  Включить программу: Панель → Платежи → Рефералы (тумблер, правила, тарифы).
  Обновлять форк вручную: та же команда с новым --image (автообновление выключено намеренно).
  Откат: та же команда с --rollback $BACKUP_NAME

  Образ прежней версии  $OLD_IMAGE
  копия базы            $BACKUP_HOST
  копия .env             $ENV_BACKUP
EOF