#!/usr/bin/env bash
# Installed root-owned on the saved VPS. The Actions key may execute only this
# receiver, with port forwarding, PTYs and agent forwarding disabled.
set -Eeuo pipefail
umask 077
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
cd /opt/sub2api
exec 9>/run/lock/wengao-sub2api-deploy.lock
flock -n 9 || { echo 'Another Sub2API deployment is running.' >&2; exit 1; }

read -r operation tag revision extra <<< "${SSH_ORIGINAL_COMMAND:-}"
[[ "$tag" =~ ^(v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)|prod-v[0-9]{4}\.[0-9]{2}\.[0-9]{2}\.[1-9][0-9]*)$ ]]
[[ -z "${extra:-}" ]]
release="/opt/sub2api/releases/$tag"
base=(docker compose -f docker-compose.local.yml -f docker-compose.proxy.yml)
compose=("${base[@]}" -f docker-compose.release.yml)

wait_healthy() {
  local attempt
  for attempt in {1..40}; do
    if [[ "$(docker inspect sub2api --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}')" == healthy ]] &&
      curl --fail --silent --max-time 5 http://127.0.0.1:8080/health >/dev/null; then
      return 0
    fi
    sleep 3
  done
  return 1
}

restore_application() {
  local previous
  previous=$(cat "$release/previous-image.txt")
  if [[ -f "$release/previous-compose-release.yml" ]]; then
    cp "$release/previous-compose-release.yml" docker-compose.release.yml
  else
    printf 'services:\n  sub2api:\n    image: %s\n' "$previous" > docker-compose.release.yml
  fi
  "${compose[@]}" up -d --no-deps --pull never sub2api
  wait_healthy
}

if [[ "$operation" == rollback ]]; then
  [[ -z "${revision:-}" && -s "$release/previous-image.txt" ]]
  [[ -f "$release/success" ]]
  current_revision=$(docker inspect sub2api --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')
  [[ "$current_revision" == "$(cat "$release/revision.txt")" ]] || {
    echo 'Refusing to roll back a release that is no longer current.' >&2; exit 1;
  }
  restore_application
  date -u +%FT%TZ > "$release/rolled-back"
  echo "Rolled back $tag; application is healthy."
  exit 0
fi

[[ "$operation" == deploy && "$revision" =~ ^[0-9a-f]{40}$ ]]
[[ ! -e "$release" ]] || { echo 'Release already exists; use a new production tag.' >&2; exit 1; }
image="ghcr.io/yowaimono/sub2api:$tag"
incoming=$(mktemp /opt/sub2api/releases/.incoming.XXXXXXXX)
switch_started=false
finish() {
  local status=$?
  trap - EXIT
  rm -f "$incoming"
  if (( status != 0 )) && [[ "$switch_started" == true ]]; then
    echo 'Deployment verification failed; restoring previous application image.' >&2
    if restore_application; then
      echo 'Previous application restored and healthy.' >&2
    else
      echo 'Automatic rollback failed; immediate operator inspection required.' >&2
    fi
  fi
  exit "$status"
}
trap finish EXIT

# Receive before changing the running service; no registry credentials are
# installed on the server. The same image is published to GHCR by Actions.
cat > "$incoming"
test -s "$incoming"
gzip -t "$incoming"
docker load -i "$incoming" >/dev/null
[[ "$(docker image inspect "$image" --format '{{.Architecture}}')" == amd64 ]]
[[ "$(docker image inspect "$image" --format '{{index .Config.Labels "org.opencontainers.image.source"}}')" == https://github.com/yowaimono/sub2api ]]
[[ "$(docker image inspect "$image" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')" == "$revision" ]]
docker run --rm --entrypoint /app/sub2api "$image" -version 2>&1 | grep -F "$revision"

install -m 700 -d "$release"
printf '%s\n' "$revision" > "$release/revision.txt"
old_id=$(docker inspect sub2api --format '{{.Image}}')
previous_image="wengao/sub2api:rollback-$tag"
docker tag "$old_id" "$previous_image"
printf '%s\n' "$previous_image" > "$release/previous-image.txt"
printf '%s\n' "$old_id" > "$release/previous-image-id.txt"
if [[ -f docker-compose.release.yml ]]; then
  cp docker-compose.release.yml "$release/previous-compose-release.yml"
fi
docker exec sub2api-postgres sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$release/database.dump"
test -s "$release/database.dump"
docker exec -i sub2api-postgres pg_restore --list < "$release/database.dump" > "$release/database-contents.txt"
backup_paths=(.env docker-compose.local.yml docker-compose.proxy.yml data)
for path in Caddyfile caddy_data caddy_config docker-compose.release.yml; do
  if [[ -e "$path" ]]; then backup_paths+=("$path"); fi
done
tar -czf "$release/config-data.tar.gz" "${backup_paths[@]}"
tar -tzf "$release/config-data.tar.gz" >/dev/null
db_counts() {
  docker exec sub2api-postgres sh -c 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "SELECT (SELECT count(*) FROM accounts), (SELECT count(*) FROM groups), (SELECT count(*) FROM api_keys);"'
}
db_counts > "$release/counts-before.txt"
docker inspect sub2api --format '{{json .Mounts}}' > "$release/mounts-before.json"
sha256sum .env > "$release/env-before.sha256"
printf 'services:\n  sub2api:\n    image: %s\n' "$image" > docker-compose.release.yml
"${compose[@]}" config --quiet
switch_started=true
"${compose[@]}" up -d --no-deps --pull never sub2api
wait_healthy
[[ "$(docker inspect sub2api --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')" == "$revision" ]]
[[ "$(docker inspect sub2api --format '{{.HostConfig.RestartPolicy.Name}}')" == always ]]
[[ "$(docker inspect sub2api --format '{{.RestartCount}}')" == 0 ]]
[[ "$(docker inspect sub2api --format '{{.State.OOMKilled}}')" == false ]]
[[ "$(docker inspect sub2api --format '{{json .HostConfig.PortBindings}}')" == '{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"8080"}]}' ]]
docker inspect sub2api --format '{{json .Mounts}}' > "$release/mounts-after.json"
cmp "$release/mounts-before.json" "$release/mounts-after.json"
sha256sum -c "$release/env-before.sha256" >/dev/null
db_counts > "$release/counts-after.txt"
cmp "$release/counts-before.txt" "$release/counts-after.txt"
curl --fail --silent --show-error --retry 3 --retry-all-errors --max-time 20 https://aiaccpool.iviker.com/health >/dev/null
date -u +%FT%TZ > "$release/success"
switch_started=false
printf 'Deployed %s commit=%s; health=OK; accounts|groups|keys=' "$tag" "$revision"
cat "$release/counts-after.txt"
