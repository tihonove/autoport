#!/usr/bin/env bash
# Сборка бинарей в контейнере. Go на хосте не нужен.
#
#   ./build/build.sh            собрать всё в ./bin
#   ./build/build.sh vet        go vet
#   ./build/build.sh fmt        gofmt -l (пусто = всё отформатировано)
#   ./build/build.sh sh         шелл внутри образа
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE=autoport-build

# Кэш модулей и сборки — на хосте, иначе каждая сборка тянет всё заново.
CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/autoport-build"
mkdir -p "$CACHE/go-build" "$CACHE/go-mod" "$REPO/bin"

docker build -q -t "$IMAGE" "$REPO/build" >/dev/null

run() {
  # --user: иначе артефакты и кэш лягут на хост под root.
  docker run --rm \
    --user "$(id -u):$(id -g)" \
    -v "$REPO:/src" \
    -v "$CACHE/go-build:/tmp/go-build" \
    -v "$CACHE/go-mod:/tmp/go-mod" \
    -w /src \
    "$IMAGE" "$@"
}

case "${1:-build}" in
  build)
    run go build -trimpath -o bin/autoport ./cmd/autoport
    echo "готово: $REPO/bin/autoport"
    ;;
  vet) run go vet ./... ;;
  fmt) run gofmt -l . ;;
  sh)  docker run --rm -it --user "$(id -u):$(id -g)" -v "$REPO:/src" -w /src "$IMAGE" sh ;;
  *)   echo "не знаю команду: $1" >&2; exit 1 ;;
esac
