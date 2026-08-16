#!/bin/sh
set -eu

layer=${1:-integration}
compose_file=docker-compose.test.yml

cleanup() {
  docker compose -f "$compose_file" down --volumes --remove-orphans
}

cleanup >/dev/null 2>&1 || true
trap cleanup EXIT INT TERM
docker compose -f "$compose_file" up -d --wait

export POSTHOUSE_TEST_GREENMAIL_SMTP=127.0.0.1:3025
export POSTHOUSE_TEST_GREENMAIL_IMAP=127.0.0.1:3143
export POSTHOUSE_TEST_RADICALE=http://127.0.0.1:5232/
export POSTHOUSE_CACHE_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA
export POSTHOUSE_TEST_WORK_PASSWORD=work-pass
export POSTHOUSE_TEST_PERSONAL_PASSWORD=personal-pass

case "$layer" in
  integration)
    GOCACHE=${GOCACHE:-/tmp/posthouse-go-cache} go test -count=1 -race -tags=integration ./internal/integration
    ;;
  e2e)
    GOCACHE=${GOCACHE:-/tmp/posthouse-go-cache} go test -count=1 -race -tags=e2e ./internal/e2e
    ;;
  *)
    echo "unknown test layer: $layer" >&2
    exit 2
    ;;
esac
