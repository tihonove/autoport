#!/usr/bin/env bash
# Установщик autoport.
#
#   curl -fsSL https://raw.githubusercontent.com/tihonove/autoport/main/install.sh | bash
#
# Ставит бинарь в ~/.local/bin, расширение в ~/.local/share/autoport/extension,
# поднимает systemd --user юнит и печатает, что доделать руками в Chrome.
# Root не нужен (кроме отдельного шага про порт 443, см. конец вывода).
set -euo pipefail

REPO=tihonove/autoport
BIN_DIR="${XDG_BIN_HOME:-$HOME/.local/bin}"
DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/autoport"
UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
ADDR="${AUTOPORT_ADDR:-127.0.0.1:8777}"

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
info() { printf '  %s\n' "$*"; }
die() { printf '\033[31mОшибка:\033[0m %s\n' "$*" >&2; exit 1; }

# --- окружение -------------------------------------------------------------

case "$(uname -s)" in
Linux) ;;
*) die "поддерживается только Linux, а тут $(uname -s)" ;;
esac

case "$(uname -m)" in
x86_64 | amd64) ARCH=x86_64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*) die "неизвестная архитектура $(uname -m) — собери из исходников" ;;
esac

command -v curl >/dev/null || die "нужен curl"
command -v tar >/dev/null || die "нужен tar"
command -v ssh >/dev/null || die "нужен ssh — без него пробрасывать нечем"

bold "autoport → $ARCH"

# --- версия ----------------------------------------------------------------

VERSION="${AUTOPORT_VERSION:-}"
if [ -z "$VERSION" ]; then
  # Без jq: тег вытаскиваем регуляркой. Код ответа отделяем от тела, потому что
  # анонимный GitHub API легко упирается в лимит (403/429) — с общего или
  # корпоративного адреса это обычное дело, и падать голым curl-ом тут нельзя.
  api=$(curl -sSL -w '\n%{http_code}' "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null) || api=""
  code=$(printf '%s' "$api" | tail -n1)
  if [ "$code" = "200" ]; then
    VERSION=$(printf '%s' "$api" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
  fi
fi
if [ -z "$VERSION" ]; then
  printf '\033[31mОшибка:\033[0m не смог узнать последнюю версию (GitHub ответил %s).\n' "${code:-?}" >&2
  printf 'Похоже на лимит запросов. Укажи версию явно:\n\n' >&2
  printf '  curl -fsSL https://raw.githubusercontent.com/%s/main/install.sh | AUTOPORT_VERSION=v0.1.0 bash\n\n' "$REPO" >&2
  printf 'Список версий: https://github.com/%s/releases\n' "$REPO" >&2
  exit 1
fi
info "версия $VERSION"

# --- скачивание ------------------------------------------------------------

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

TARBALL="autoport_${VERSION}_linux_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/$VERSION/$TARBALL"
info "качаю $TARBALL"
curl -fsSL "$URL" -o "$TMP/$TARBALL" || die "не скачался $URL"

# Сверяем с checksums.txt из релиза, если есть чем: скрипт запускают через
# curl | bash, так что лишняя проверка целостности не повредит.
if command -v sha256sum >/dev/null; then
  if curl -fsSL "https://github.com/$REPO/releases/download/$VERSION/checksums.txt" -o "$TMP/checksums.txt" 2>/dev/null; then
    expected=$(grep " $TARBALL\$" "$TMP/checksums.txt" | awk '{print $1}')
    actual=$(sha256sum "$TMP/$TARBALL" | awk '{print $1}')
    if [ -n "$expected" ] && [ "$expected" != "$actual" ]; then
      die "контрольная сумма не сошлась — архив побился или подменён"
    fi
    [ -n "$expected" ] && info "контрольная сумма сошлась"
  fi
fi

tar -C "$TMP" -xzf "$TMP/$TARBALL"
[ -f "$TMP/autoport" ] || die "в архиве нет бинаря"

# --- раскладка -------------------------------------------------------------

mkdir -p "$BIN_DIR" "$DATA_DIR" "$UNIT_DIR"

# systemd --user есть не везде: контейнеры, WSL, не-systemd дистрибутивы, вход
# по ssh без lingering. Проверяем не наличие бинаря, а рабочий ли менеджер —
# systemctl может стоять, а сессионной шины не быть.
HAVE_SYSTEMD=0
if command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
  HAVE_SYSTEMD=1
fi

# Бинарь может быть занят работающим сервисом — сначала гасим.
if [ "$HAVE_SYSTEMD" = 1 ] && systemctl --user is-active --quiet autoport.service 2>/dev/null; then
  info "останавливаю запущенный сервис"
  systemctl --user stop autoport.service
fi

install -m 755 "$TMP/autoport" "$BIN_DIR/autoport"
rm -rf "$DATA_DIR/extension"
cp -r "$TMP/extension" "$DATA_DIR/extension"
info "бинарь → $BIN_DIR/autoport"
info "расширение → $DATA_DIR/extension"

cat >"$UNIT_DIR/autoport.service" <<EOF
[Unit]
Description=autoport — пробрасывалка локальных портов на удалённые машины
Documentation=https://github.com/$REPO
After=graphical-session.target
PartOf=graphical-session.target

[Service]
Type=simple
ExecStart=$BIN_DIR/autoport -addr $ADDR
Restart=always
RestartSec=3
Environment=PATH=/usr/local/bin:/usr/bin:/bin
# Пробросы — дочерние ssh-процессы, они должны умирать вместе с сервисом.
KillMode=control-group
TimeoutStopSec=15

[Install]
WantedBy=default.target
EOF

if [ "$HAVE_SYSTEMD" = 1 ]; then
  systemctl --user daemon-reload
  systemctl --user enable --now autoport.service
  info "сервис запущен"
else
  info "systemd --user недоступен — юнит положил, но не запускал"
fi

# --- что дальше ------------------------------------------------------------

echo
bold "Готово. Осталось доделать руками."

# Шаги нумеруем счётчиком, а не арифметикой на месте: часть из них печатается
# по условию, и вручную посчитанные номера начинают прыгать.
STEP=0
step() {
  STEP=$((STEP + 1))
  echo
  bold "$STEP. $*"
}

step "Расширение в Chrome"
info "chrome://extensions → включить «Режим разработчика»"
info "→ «Загрузить распакованное расширение» → указать:"
echo
printf '     \033[1m%s\033[0m\n' "$DATA_DIR/extension"
echo
info "Chrome при каждом старте будет показывать бабл про режим разработчика —"
info "это нормально для распакованных расширений."

if ! printf '%s' "$PATH" | tr ':' '\n' | grep -qx "$BIN_DIR"; then
  step "$BIN_DIR не в PATH"
  info "добавь в ~/.bashrc:  export PATH=\"$BIN_DIR:\$PATH\""
fi

# Про 443 напоминаем, только если он и правда недоступен: у того, кто уже
# настроил, лишний шаг в выводе выглядит как «ты что-то не доделал».
UNPRIV=$(cat /proc/sys/net/ipv4/ip_unprivileged_port_start 2>/dev/null || echo 1024)
if [ "$UNPRIV" -gt 443 ]; then
  step "Порт 443 (только если пробрасываешь https-домены)"
  info "ssh под юзером не биндит порты ниже $UNPRIV. Разово, под root:"
  echo
  info "  echo 'net.ipv4.ip_unprivileged_port_start = 443' |"
  info "    sudo tee /etc/sysctl.d/50-autoport.conf && sudo sysctl --system"
  echo
  info "Побочка: 443–1023 станут доступны всем процессам машины."
fi

if [ "$HAVE_SYSTEMD" = 0 ]; then
  step "Запуск без systemd"
  info "на этой системе нет рабочего systemd --user, запускать самому:"
  echo
  info "  $BIN_DIR/autoport -addr $ADDR"
  echo
  info "юнит лежит в $UNIT_DIR/autoport.service — пригодится, если systemd появится"
fi

echo
bold "Смотрелка: http://$ADDR/"
if [ "$HAVE_SYSTEMD" = 1 ]; then
  info "логи:      journalctl --user -u autoport -f"
fi

# Без записей в ~/.ssh/config пробрасывать некуда, а в интерфейсе это выглядит
# как пустой список без объяснений. Предупреждаем сразу.
if [ ! -f "$HOME/.ssh/config" ]; then
  echo
  printf '\033[33mПредупреждение:\033[0m %s\n' "нет ~/.ssh/config — список машин будет пуст."
  info "autoport берёт хосты только оттуда. Опиши хотя бы одну запись:"
  echo
  info "  Host myserver"
  info "    HostName 10.0.0.1"
  info "    User me"
fi
