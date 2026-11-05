# Wengao production releases

This fork deploys only Sub2API on the saved account-pool VPS through the manual
**Wengao Production** Actions workflow. PostgreSQL, Redis, Caddy and kiro-rs are
not recreated. Budget policies are not changed by a release.

## One-time operator setup

Install `receive-release.sh` root-owned, mode `0700`, at
`/opt/sub2api/releases/receive-release.sh` using the saved SSH helper. Create the
`releases` directory with mode `0700`. Authorize a dedicated Ed25519 SSH key with:

```text
restrict,command="/opt/sub2api/releases/receive-release.sh" ssh-ed25519 <public-key> wengao-sub2api-actions
```

Configure repository variables `WENGAO_DEPLOY_HOST`, `WENGAO_DEPLOY_PORT` and
secrets `WENGAO_DEPLOY_SSH_KEY`, `WENGAO_DEPLOY_KNOWN_HOSTS`. Obtain and pin the
host key through the existing authenticated connection. Do not commit secrets,
copy the root password into Actions, or give this key an unrestricted shell.
The dedicated key may be revoked and its Actions secret removed after a release;
provision a fresh key with the helper before the next deployment or rollback.

## Deploy

After merging the intended source to this fork's `main`, create an immutable Git
tag `prod-vYYYY.MM.DD.N` pointing to that exact commit. Dispatch the workflow on
`main` with `operation=deploy` and that tag. The workflow builds Linux/amd64,
checks the embedded commit, publishes the same immutable image to GHCR, and
streams the image over SSH. No registry password is stored on the VPS.

Before switching the application, the receiver saves a PostgreSQL custom-format
dump, application/configuration archive, previous image alias, mounts and record
counts under `/opt/sub2api/releases/<tag>/`, accessible only to root. It uses
`docker-compose.local.yml`, `docker-compose.proxy.yml` and the new
`docker-compose.release.yml`; use all three for subsequent Compose operations.
Only `sub2api` is recreated with `--no-deps --pull never`.

The receiver checks health, revision, restart policy, port binding, mounts,
unchanged environment and account/group/key counts. A failed switch triggers an
application-image rollback. Database migrations are not automatically reversed;
review compatibility and the SQL delta before every release. Pre-switch failures
leave the running application unchanged. Failed tag directories are retained as
evidence; retry with a new production tag.

## Roll back

Dispatch **Wengao Production** on `main` with `operation=rollback` and the tag of
the currently deployed release. It restores that release's previous image and
Compose override, then checks health. It refuses to undo a release superseded by
a later one. Database restoration is a separate explicit operator operation;
never restore the dump over new user writes as an automatic rollback action.
