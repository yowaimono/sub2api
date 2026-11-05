#!/usr/bin/env bash
# Run inside an isolated Linux container, never on the production host:
# docker run --rm -v "$PWD/deploy/wengao:/src:ro" postgres:18-alpine bash /src/receive-release-test.sh
set -euo pipefail
[[ ! -e /opt/sub2api ]] || { echo 'Requires a disposable container.' >&2; exit 1; }
mkdir -p /opt/sub2api/releases /opt/sub2api/data /run/lock
cd /opt/sub2api
printf 'secret=test-only\n' > .env
touch docker-compose.local.yml docker-compose.proxy.yml data/preserved
cat > /usr/local/bin/docker <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> /tmp/docker-calls
case "$1" in
  load|tag) exit 0 ;;
  run) echo "Sub2API test (commit: $TEST_SHA)" ;;
  image)
    case "$*" in
      *Architecture*) echo amd64 ;;
      *image.source*) echo https://github.com/yowaimono/sub2api ;;
      *image.revision*)
        if [[ "${TEST_MODE:-}" == bad-label ]]; then echo invalid; else echo "$TEST_SHA"; fi ;;
      *) exit 1 ;;
    esac ;;
  inspect)
    case "$*" in
      *'{{.Image}}'*) echo sha256:oldimage ;;
      *image.revision*)
        if [[ "$(cat /tmp/app-state)" == new ]]; then echo "$TEST_SHA"; else echo oldrevision; fi ;;
      *Health.Status*) echo healthy ;;
      *RestartPolicy.Name*) echo always ;;
      *RestartCount*) echo 0 ;;
      *OOMKilled*) echo false ;;
      *PortBindings*) echo '{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"8080"}]}' ;;
      *Mounts*) echo '[]' ;;
      *) exit 1 ;;
    esac ;;
  exec)
    case "$*" in
      *pg_dump*) echo test-database-dump ;;
      *pg_restore*) cat >/dev/null; echo test-archive-list ;;
      *psql*)
        if [[ "${TEST_MODE:-}" == bad-counts && "$(cat /tmp/app-state)" == new ]]; then
          echo '12|15|18'
        else echo '13|15|18'; fi ;;
      *) exit 1 ;;
    esac ;;
  compose)
    if [[ "$*" == *'up -d --no-deps --pull never sub2api' ]]; then
      if grep -q 'image: wengao/sub2api:rollback-' docker-compose.release.yml; then
        echo old > /tmp/app-state
      else echo new > /tmp/app-state; fi
    elif [[ "$*" != *'config --quiet' ]]; then exit 1; fi ;;
  *) exit 1 ;;
esac
MOCK
cat > /usr/local/bin/curl <<'MOCK'
#!/usr/bin/env bash
exit 0
MOCK
chmod 755 /usr/local/bin/docker /usr/local/bin/curl
export TEST_SHA=1111111111111111111111111111111111111111
echo old > /tmp/app-state

# Invalid commands do not invoke Docker or create a release.
if SSH_ORIGINAL_COMMAND='deploy invalid invalid' bash /src/receive-release.sh </dev/null; then exit 1; fi
test ! -e /tmp/docker-calls

# Label failure leaves the running service and Compose configuration untouched.
if printf 'image archive' | gzip | TEST_MODE=bad-label SSH_ORIGINAL_COMMAND="deploy prod-v2026.11.05.1 $TEST_SHA" bash /src/receive-release.sh; then exit 1; fi
test "$(cat /tmp/app-state)" = old
test ! -e docker-compose.release.yml
test ! -e releases/prod-v2026.11.05.1

# Successful release preserves data and creates validated backup/metadata files.
printf 'image archive' | gzip | SSH_ORIGINAL_COMMAND="deploy prod-v2026.11.05.2 $TEST_SHA" bash /src/receive-release.sh
test "$(cat /tmp/app-state)" = new
test -f releases/prod-v2026.11.05.2/success
test -s releases/prod-v2026.11.05.2/database.dump
tar -tzf releases/prod-v2026.11.05.2/config-data.tar.gz | grep -q data/preserved
test -f data/preserved

# Manual rollback restores only the app image.
SSH_ORIGINAL_COMMAND='rollback prod-v2026.11.05.2' bash /src/receive-release.sh </dev/null
test "$(cat /tmp/app-state)" = old
test -f releases/prod-v2026.11.05.2/rolled-back

# A post-switch invariant failure automatically restores the old app.
if printf 'image archive' | gzip | TEST_MODE=bad-counts SSH_ORIGINAL_COMMAND="deploy prod-v2026.11.05.3 $TEST_SHA" bash /src/receive-release.sh; then exit 1; fi
test "$(cat /tmp/app-state)" = old
test ! -e releases/prod-v2026.11.05.3/success
test -s releases/prod-v2026.11.05.3/database.dump
test -f data/preserved
! grep -E 'compose .* (down|restart)|up .* (postgres|redis|caddy|kiro)' /tmp/docker-calls
echo 'PASS: invalid command, invalid label, backup/deploy, manual rollback, automatic rollback.'
