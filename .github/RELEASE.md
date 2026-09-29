# Personal GitHub Free release workflow

This repository supports a personal private repository on GitHub Free. Tag
rulesets, protected environments, required reviewers, external database secrets
and a paid account are not required. This replaces the previous protected-tag
release policy at the owner's request.

## Checks and publication

Pushes to main, `v*` tag pushes, pull requests and manual runs execute quality,
fuzz, integration and image checks. All database tests use isolated temporary
SQLite files without external services, DSNs or secrets. The integration job runs
SQLite business contracts and the submission/approval HTTP flow; the image job
verifies SQLite initialization, persistence and API/Web behavior. MySQL/MariaDB
support has been removed.

Publishing requires a manual `workflow_dispatch` with `publish=true`, on an
existing stable tag matching `vMAJOR.MINOR.PATCH`. Branches and prerelease tags
are rejected before registry login. Default `publish=false` runs checks only.
The publish job depends on all four check jobs and checks out the event commit
explicitly. Ordinary pushes never publish images.

The workflow must first exist on the default branch (`main`) to enable manual
runs. After pushing the reviewed commit and its intended release tag, run:

```sh
gh workflow run ci.yml --repo maizihk/quotewisp --ref v1.0.0 -f publish=true
```

Users with repository write access can manually run workflows. This is not an
independent approval or a protected-tag guarantee: on the free private repository,
writers may modify tags or workflow code. Keep write access limited to trusted
maintainers. The workflow serializes publishes for a given ref; it does not make
Git or registry tags immutable, and a rerun can replace a version tag. Record and
deploy the successful digest, not a mutable tag.

## Registry and final artifact

The target is `ghcr.io/<lowercase owner>/<lowercase repository>` (currently
`ghcr.io/maizihk/quotewisp`). Authentication uses the workflow's `GITHUB_TOKEN`
with `packages: write` only in the publish job; other jobs have `contents: read`.
No `release`/`integration` environment, `IMAGE_*` variables, `REGISTRY_*` secrets
or `MYSQL_TEST_DSN` secret needs configuration. Existing unused environments may
remain; this change does not delete them.

New GHCR packages default to private; the workflow does not change repository
or package visibility. If that package already exists, grant this repository
Actions write access to it. Private deployment hosts need their own registry
read credentials; never embed the ephemeral workflow token in deployment files.
Actions must be enabled and have available account minutes/quota.

The image gets the version, complete commit SHA and UTC build time, and is pushed
with version and commit tags. The job records its registry digest, pulls that
digest and runs `scripts/smoke-release-image.py`: exact metadata, API/Web smoke,
schema-5 initialization and persistence across two containers. A smoke failure
fails publication acceptance; the pushed image remains and must not be deployed.
The workflow does not deploy or create a GitHub Release page.

Current images serve both the read API and `web` from one digest. The read API
supports reads at schema versions 1–5; writes require schema 5. Normal startup
runs migrations automatically to schema 5. SQLite is the only backend.

For SQLite, stop the single application instance and back up the complete
`DATA_DIR` (database, WAL/SHM if present, and secret) before replacing the image.
Start the new image against the existing volume, then verify readiness, login,
settings and data. Do not run an old schema-4 application against the upgraded
volume. Roll back with the matching old image and the pre-upgrade backup.
See `docs/operations.md` and `docs/sqlite-release-validation.md`.

## Evidence

See `docs/release-readiness.md` for current status and
`docs/release-v1.0.0-validation.md` for the prior locally tested candidate.
A new tagged commit needs its own version metadata and digest acceptance; the
previous candidate image must not be relabeled as built from a later commit.

References: [manual workflow runs](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow),
[GHCR authentication and visibility](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry).
