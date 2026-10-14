#!/usr/bin/env bash
#
# mikan-форк: перевод сервера, собранного из ветки ДО мержа с апстримом (например
# feature/olcrtc), на релиз 0.5.1.1 — починка учёта миграций в базе.
#
#   curl -fsSL https://raw.githubusercontent.com/o0hhs/mikanaddons/main/scripts/migrate-pre-merge-db.sh \
#     | sudo bash
#
# Зачем это нужно. В таких сборках миграции форка были пронумерованы 0016_referrals.sql и
# 0017_node_olcrtc.sql. В релизе номера 0016..0031 заняты миграциями апстрима (0.5.1.0), а
# наши стали 0032..0035 (0032_referrals.sql — это тот же файл, переименованный ровно, sha256
# совпадает). goose (internal/panel/store) не даёт пометить 0032 применённой, оставив 0016..0031
# неприменёнными: он отказывается работать с «дырой» в нумерации (out-of-order). Поэтому скрипт:
#
#   * применяет апстримовые 0016..0031 сам — теми же файлами из тега релиза, со сверкой sha256
#     каждого файла (таблица внизу скрипта), каждый файл одной транзакцией, как это делает goose;
#   * переносит строку 16 → 32 в goose_db_version: файл миграции тот же самый, изменений в схеме
#     это не делает, зато строка перестаёт конфликтовать с апстримовой 0016;
#   * удаляет строку 17 (это была olcrtc-миграция; её таблица node_olcrtc остаётся мусором в базе,
#     её видно и можно удалить после обновления);
#   * дописывает строки 16..31 как применённые.
#
# Данные не трогаются вовсе: ни пользователи, ни платежи, ни подписки, ни настройки, ни
# реферальные коды и балансы (строки в referral_* скрипт сверяет до и после). Образ скрипт НЕ
# меняет — после него нужно ещё обновить его на релиз (команда печатается в конце).
#
# Порядок действий целиком:
#   1. sudo bash migrate-pre-merge-db.sh          # этот скрипт: база готова к релизу
#   2. sudo mikan update ghcr.io/o0hhs/mikan@sha256:5163b007...   # образ на релиз
#   3. заменить /usr/local/bin/mikan установщиком из релиза        # новый ключ подписи
#
# Ключи:
#   --check    только показать, что скрипт собирается делать (ничего не меняет)
#   --yes      не спрашивать подтверждения
#   --trace    показывать каждую команду
#   -h, --help эта справка
#
# Примеры:
#   curl -fsSL <url> | sudo bash -s -- --check
#   curl -fsSL <url> | sudo bash -s -- --yes

set -Eeuo pipefail

MIKAN_DIR=${MIKAN_DIR:-/opt/mikan}
UPSTREAM_TAG=${UPSTREAM_TAG:-v0.5.1.1}
REPO_RAW=${REPO_RAW:-https://raw.githubusercontent.com/o0hhs/mikanaddons}
RELEASE_IMAGE=${RELEASE_IMAGE:-ghcr.io/o0hhs/mikan@sha256:5163b0070613d0c6ba011fa45d6c7cfcea66168a567b6aa1b84af6a6c22f2c6c}

# Почему всё перезапускается: переданный по пайпу скрипт (curl … | sudo bash -s -- …) bash читает
# из stdin, и любая команда внутри наследует этот stdin. `docker compose exec` съедает остаток
# скрипта, и он молча останавливается на середине — с кодом 0, будто всё прошло. Поэтому скрипт,
# пришедший не с терминала, читается целиком один раз и запускается заново с закрытым stdin.
if [ "${MIKAN_PREMERGE_REEXEC:-0}" != 1 ] && [ ! -t 0 ] && [ ! -f "$0" ]; then
  _self=$(mktemp) || exit 1
  cat >"$_self"
  if [ ! -s "$_self" ]; then
    rm -f "$_self"
    echo "migrate-pre-merge-db: не удалось прочитать скрипт из stdin" >&2
    exit 1
  fi
  MIKAN_PREMERGE_REEXEC=1 MIKAN_DIR="$MIKAN_DIR" UPSTREAM_TAG="$UPSTREAM_TAG" REPO_RAW="$REPO_RAW" \
    RELEASE_IMAGE="$RELEASE_IMAGE" bash "$_self" "$@" </dev/null
  _rc=$?
  rm -f "$_self"
  exit "$_rc"
fi

DIED=0
TMP=""
die() { DIED=1; printf '\n\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }
on_exit() {
  local rc=$?
  [ -n "$TMP" ] && rm -rf "$TMP"
  if [ "$rc" -eq 0 ] || [ "$DIED" = 1 ]; then
    return 0
  fi
  printf '\n\033[31m✗\033[0m скрипт остановился (%s:%s, код %s)\n' "${BASH_SOURCE[0]##*/}" "${BASH_LINENO[0]}" "$rc" >&2
  cat <<'EOF' >&2
  Панель могла остаться остановленной, база — наполовину правленой. Ничего страшного:
  бэкап уже снят (путь в выводе выше), а вернуть всё можно так:

    cd /opt/mikan && sudo docker compose up -d panel
    # если и это не поднялось — восстановить базу дампом (образ при этом ДОЛЖЕН быть прежним):
    sudo docker compose run --rm --no-deps -T panel database restore /data/panel/backup-<дата>-pre-merge.dump

EOF
}
trap on_exit EXIT

step() { printf '\n\033[1;36m▸ %s\033[0m\n' "$*"; }
ok() { printf '  \033[32m✓\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
info() { printf '  \033[2m%s\033[0m\n' "$*"; }

usage() {
  sed -n '2,/^set -Eeuo/p' "$0" | sed 's/^#//' | sed 's/^ //' | sed '$d'
}

CHECK=0
ASSUME_YES=0
TRACE=0
while [ $# -gt 0 ]; do
  case $1 in
    --check) CHECK=1; shift ;;
    --yes|-y) ASSUME_YES=1; shift ;;
    --trace) TRACE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "неизвестный ключ: $1 (--help)" ;;
  esac
done
[ "$TRACE" = 1 ] && set -x

[ "$(id -u)" = 0 ] || die "запусти от root: curl -fsSL <url> | sudo bash"
command -v docker >/dev/null || die "docker не найден"
docker compose version >/dev/null 2>&1 || die "нужен docker compose v2"
[ -f "$MIKAN_DIR/compose.yaml" ] || die "не вижу $MIKAN_DIR/compose.yaml — это установленный mikan?"
[ -f "$MIKAN_DIR/.env" ] || die "не вижу $MIKAN_DIR/.env — это установленный mikan?"

dc() { docker compose --project-directory "$MIKAN_DIR" -f "$MIKAN_DIR/compose.yaml" "$@"; }
env_get() { sed -n "s/^$1=//p" "$MIKAN_DIR/.env" | head -n1; }
DBPASS=$(env_get MIKAN_POSTGRES_PASSWORD)
DBUSER=$(env_get MIKAN_POSTGRES_USER); DBUSER=${DBUSER:-mikan}
DBNAME=$(env_get MIKAN_POSTGRES_DB); DBNAME=${DBNAME:-mikan}

# psql через контейнер базы (панель может быть остановлена), при неудаче — через панель.
sql() { # sql ЗАПРОС → строка ответа; при ошибке — сама ошибка в stderr и код 1
  local out err
  err=$(mktemp) || return 1
  if out=$(dc exec -T -e PGPASSWORD="$DBPASS" postgres psql -U "$DBUSER" -d "$DBNAME" -tAX -v ON_ERROR_STOP=1 -c "$1" 2>"$err"); then
    rm -f "$err"; printf '%s' "$out" | tr -d '[:space:]'; return 0
  fi
  if out=$(dc exec -T panel sh -c 'psql "$MIKAN_DATABASE_URL" -tAX -v ON_ERROR_STOP=1 -c "$1"' _ "$1" 2>>"$err"); then
    rm -f "$err"; printf '%s' "$out" | tr -d '[:space:]'; return 0
  fi
  cat "$err" >&2
  rm -f "$err"
  return 1
}

# Каждому файлу апстримовой миграции — свой sha256 из тега релиза: файлы едут по сети, и без
# сверки подменённый или битый файл молча уехал бы в базу.
expected_sha() {
  case $1 in
    0016_device_names_bans.sql) echo 49d22422ae20f96aafefe0515dcb62cbb4929ec291d2ad6b3f3c09a50aef2e87 ;;
    0017_speed_limits.sql) echo 544537f8f442a788791710de527736b373814fe958984376e0c4ab344249c406 ;;
    0018_slot_traffic.sql) echo 9004bab5551717e11a48f99a0374c52e3ab07a060683ffeb4f92d5e27b111d5b ;;
    0019_unbind_rules.sql) echo b265dd6c9bb1bbf5a29fec638e1d1737257c6505046165d09fddc71091e193e6 ;;
    0020_foreign_key_indexes.sql) echo fec4cc2d6c166bcb8860aed651259c45db0a9e6a13480081c6657ceab3c61597 ;;
    0021_trial_payments.sql) echo 885a4a08edc82387780967b5232308bbc8789645d96aebfdaff97cebcec9e5aa ;;
    0022_free_plans.sql) echo 9e9ad2ace261382084bfc37b715f4c68f11b00487ab64e0e71e3c17de7f18b7f ;;
    0023_tariff_description.sql) echo 17a67d2a6d2eb717107b97f70cf885e64c20d36c9cfcd5cfd9abc2eb731415a5 ;;
    0024_reissue_device_mode.sql) echo 9404bb0f4ecf926c19c1a894fd1955e710efa93dc963a07ce8527e498c2dadf9 ;;
    0025_addon_grants.sql) echo 7f93020e9523c3c306c94f914d4e9d60fcf1700f4e8ed2cbc599e190d1510314 ;;
    0026_addon_kv.sql) echo 3051af8bda2acb84fe39bdecd722570cce22a571e2a8656e87b2ee69c7daff64 ;;
    0027_addon_events.sql) echo 8d800cf21db3a9d2b5cd0916aeeb75bc7d26cc5f8766ebaac080796b74ca6434 ;;
    0028_addon_updates.sql) echo d978112f622017379b5ff2acd694bd849f4c5f160e1b24d06410fc22cc2e2214 ;;
    0029_addon_sources.sql) echo 5d724742130161343a16be6717aefd4fc838c37236a8484ad02ebee0620df4f6 ;;
    0030_addon_grant_keys.sql) echo 1eb5d13db1887baca0d72998461b14c2fbbde5be419fbe52050a9d32bd7ed1ea ;;
    0031_addon_id_claims.sql) echo eef5ae46ff4c355bca9bde14e4dd5fc0c74760c6af49b5456617efebade22f51 ;;
    *) return 1 ;;
  esac
}
MIGRATIONS="0016_device_names_bans.sql 0017_speed_limits.sql 0018_slot_traffic.sql 0019_unbind_rules.sql
0020_foreign_key_indexes.sql 0021_trial_payments.sql 0022_free_plans.sql 0023_tariff_description.sql
0024_reissue_device_mode.sql 0025_addon_grants.sql 0026_addon_kv.sql 0027_addon_events.sql
0028_addon_updates.sql 0029_addon_sources.sql 0030_addon_grant_keys.sql 0031_addon_id_claims.sql"

step "Что в базе сейчас"
MAXV=$(sql "select coalesce(max(version_id),0) from goose_db_version where is_applied") || die "база недоступна"
COUNT16=$(sql "select count(*) from goose_db_version where version_id in (16,17) and is_applied") || COUNT16=?
info "goose: старшая применённая миграция — $MAXV, из них 16/17 — $COUNT16"
BEFORE=$(sql "select (select count(*) from referral_accounts),(select count(*) from referrals),(select count(*) from referral_rewards),(select count(*) from referral_spends),(select count(*) from referral_counters)") \
  || die "не вижу таблиц реферальной программы (это точно сборка из до-мержевой ветки?)"
info "referral_accounts|referrals|referral_rewards|referral_spends|referral_counters = $BEFORE"

if [ "$MAXV" = 32 ] || { [ "$MAXV" -gt 32 ] 2>/dev/null; }; then
  ok "учёт миграций уже в порядке (старшая — $MAXV): починка не нужна"
  info "переходи к обновлению образа: sudo mikan update $RELEASE_IMAGE"
  exit 0
fi
[ "$MAXV" = 16 ] || [ "$MAXV" = 17 ] || die "неожиданная история миграций (старшая — $MAXV, жду 16 или 17): этот скрипт не для неё"

if [ "$COUNT16" != 2 ]; then
  warn "строк 16/17 не две ($COUNT16) — проверь: select version_id, is_applied from goose_db_version order by 1"
fi

step "Что скрипт сделает"
cat <<EOF
  1. Снимет бэкап: .env и дамп базы (pg_dump) внутрь $MIKAN_DIR/data/panel/.
  2. Остановит панель.
  3. Скачает апстримовые миграции 0016..0031 из тега $UPSTREAM_TAG и сверит sha256 каждой.
  4. Применит их по порядку (каждый файл — одной транзакцией).
  5. Поправит goose_db_version: 16 → 32 (тот же файл миграции), удалит 17 (olcrtc),
     допишет 16..31 как применённые.
  6. Проверит: старшая миграция 32, строки referral_* те же, что были ($BEFORE).
  7. Покажет, что делать дальше (обновиться на релиз и заменить хостовый установщик).
Данные (пользователи, платежи, подписки, настройки, реферальные коды и балансы) не меняются.
EOF

if [ "$CHECK" = 1 ]; then
  ok "это --check: ничего не менялось"
  exit 0
fi
if [ "$ASSUME_YES" = 0 ]; then
  read -r -p 'Продолжить? (yes/no) ' answer </dev/tty || answer=no
  [ "$answer" = yes ] || die "отменено"
fi

step "1/6 Бэкап"
STAMP=$(date +%Y%m%d-%H%M%S)
cp -a "$MIKAN_DIR/.env" "$MIKAN_DIR/.env.before-pre-merge-$STAMP"
ok ".env.before-pre-merge-$STAMP"
DUMP="backup-$STAMP-pre-merge.dump"
dc run --rm --no-deps -T panel database backup "/data/panel/$DUMP" >/dev/null || die "бэкап не вышел — ничего не менялось"
[ -s "$MIKAN_DIR/data/panel/$DUMP" ] || die "дамп пуст или не виден в $MIKAN_DIR/data/panel/$DUMP"
ok "дамп: $MIKAN_DIR/data/panel/$DUMP ($(du -h "$MIKAN_DIR/data/panel/$DUMP" | cut -f1))"

step "2/6 Панель останавливается"
dc stop panel >/dev/null
ok "панель остановлена (вернётся на шаге 6 обновлением)"

step "3/6 Апстримовые миграции 0016..0031"
TMP=$(mktemp -d) || die "нет места для временных файлов"
for f in $MIGRATIONS; do
  curl -fsSL "$REPO_RAW/$UPSTREAM_TAG/internal/panel/store/postgres/$f" -o "$TMP/$f" || die "не скачался $f"
  want=$(expected_sha "$f") || die "нет эталона sha256 для $f"
  got=$(sha256sum "$TMP/$f" | cut -d' ' -f1)
  [ "$want" = "$got" ] || die "sha256 не совпал у $f (ждали $want, получили $got) — файл подменён или тег съехал"
done
ok "16 файлов скачаны, sha256 каждого сходится"

step "4/6 Применение (каждый файл — одной транзакцией)"
for f in $MIGRATIONS; do
  awk '/^-- \+goose Up/{f=1;next} /^-- \+goose Down/{f=0} f' "$TMP/$f" |
    dc exec -T -e PGPASSWORD="$DBPASS" postgres psql -U "$DBUSER" -d "$DBNAME" -q -1 -v ON_ERROR_STOP=1 ||
    die "$f не применился (база не тронута: транзакция откатилась)"
  ok "$f"
done

step "5/6 Учёт goose"
sql "update goose_db_version set version_id = 32 where version_id = 16 and is_applied" >/dev/null || die "не удалось перенести строку 16 → 32"
sql "delete from goose_db_version where version_id = 17" >/dev/null || die "не удалось убрать строку 17"
sql "insert into goose_db_version (version_id, is_applied)
     select v, true from generate_series(16,31) as v
     where not exists (select 1 from goose_db_version g where g.version_id = v and g.is_applied)" >/dev/null ||
  die "не удалось дописать строки 16..31"
ok "16 → 32, строка 17 убрана, 16..31 отмечены применёнными"

step "6/6 Проверка"
MAXV=$(sql "select coalesce(max(version_id),0) from goose_db_version where is_applied")
[ "$MAXV" = 32 ] || die "старшая миграция $MAXV, жду 32"
ok "старшая применённая миграция — 32 (дальше goose сам применит 33, 34, 35 на старте релиза)"
N=$(sql "select count(*) from goose_db_version where version_id between 16 and 31 and is_applied")
[ "$N" = 16 ] || die "строк 16..31 — $N, жду 16"
ok "16..31 отмечены применёнными"
AFTER=$(sql "select (select count(*) from referral_accounts),(select count(*) from referrals),(select count(*) from referral_rewards),(select count(*) from referral_spends),(select count(*) from referral_counters)")
[ "$AFTER" = "$BEFORE" ] || die "строки referral_* изменились: было $BEFORE, стало $AFTER"
ok "реферальные таблицы не тронуты: $AFTER"
[ "$(sql "select count(*) from information_schema.columns where table_name='bound_devices' and column_name='name'")" = 1 ] ||
  die "нет bound_devices.name — миграция 0016 не применилась?"
[ "$(sql "select to_regclass('public.addon_sources') is not null")" = t ] ||
  die "нет таблицы addon_sources — миграция 0029 не применилась?"
ok "апстримовые объекты на месте (bound_devices.name, addon_sources)"

trap - EXIT
step "Готово: база ждёт релиз"
cat <<EOF
  Дальше три шага (порядок важен):

  1. Обновить образ на релиз (позиционный аргумент не сверяет версии — это и нужно):
       sudo mikan update $RELEASE_IMAGE

  2. Заменить хостовый установщик на релизный — в старом ключ подписи прежний, и без этого
     автоматические обновления не увидят наших релизов (на arm64-сервере возьми mikan-aarch64,
     sha256 f337f159f375c9814bccd922f273dd2d859252c0ec7db74c3cda0d51f46a2ed1):
       curl -fL -o /tmp/mikan-x86_64 https://github.com/o0hhs/mikanaddons/releases/download/v0.5.1.1/mikan-x86_64
       echo "0ca7c9adaa1692845923fb26ae115ada586cb87d65b2821f0720016c50ba0b12  /tmp/mikan-x86_64" | sha256sum -c -
       sudo cp -a /usr/local/bin/mikan /usr/local/bin/mikan.before-0511
       sudo install -m 0755 /tmp/mikan-x86_64 /usr/local/bin/mikan

  3. Проверить:
       cd $MIKAN_DIR && sudo mikan status && sudo mikan update --check

  Бэкапы:
    $MIKAN_DIR/.env.before-pre-merge-$STAMP
    $MIKAN_DIR/data/panel/$DUMP   (свой дамп сделает ещё и mikan update)

  Откат, если что-то не так. Важно: восстанавливать дамп надо ПРЕЖНИМ образом — restore
  поднимает схему до версии того бинаря, который его выполняет:

    # 1. вернуть прежний образ в .env (он записан в .env.before-pre-merge-$STAMP)
    grep '^MIKAN_IMAGE=' "$MIKAN_DIR/.env.before-pre-merge-$STAMP"
    # 2. прописать его в $MIKAN_DIR/.env и поднять стек:
    cd $MIKAN_DIR && sudo docker compose up -d panel
    # 3. если база успела уехать далеко — восстановить из дампа:
    sudo docker compose run --rm --no-deps -T panel database restore /data/panel/$DUMP

  Мусор от olcrtc (никому не мешает; можно удалить после успешного обновления):
    drop table if exists node_olcrtc;
EOF
