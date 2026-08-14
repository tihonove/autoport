# Образ только для сборки — бинарь забираем на хост, контейнер ничего не запускает.
# Go на машине не установлен намеренно, тулчейн живёт здесь.
FROM golang:1.25-alpine

# git нужен go-тулчейну, если появятся зависимости из VCS.
RUN apk add --no-cache git

# Сборка идёт от имени хостового пользователя (--user в build.sh), поэтому
# каталоги кэша должны быть доступны на запись кому угодно.
ENV GOCACHE=/tmp/go-build \
    GOMODCACHE=/tmp/go-mod \
    GOFLAGS=-buildvcs=false

WORKDIR /src
