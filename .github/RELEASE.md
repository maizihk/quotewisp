# Release gate configuration

The `publish` job runs only for a `vMAJOR.MINOR.PATCH` tag when GitHub reports
that the ref is protected. Configure a repository tag ruleset for `v*` and
protect the `release` environment with required reviewers. Configure the
`integration` environment so its `MYSQL_TEST_DSN` secret is available only to
trusted branch, protected-tag, and manually dispatched runs. Pull requests run
the integration test package without a DSN to verify its explicit skip path and
never bind the secret-bearing environment.

The current external database gate targets MariaDB 11.8. The workflow does not
claim MySQL 8.4 integration coverage until a separate trusted MySQL 8.4 DSN is
configured and run. The `integration` job uses an external DSN secret and never
starts a database container. The `image` job starts MariaDB 11.8 only to smoke
the production image (`web` subcommand and HTTP probes); that service is not a
substitute for the external integration gate.

Current images serve both the read API and `web` from one digest. The read API
supports reads at schema versions 1–5; writes require schema 5. Normal startup
runs migrations automatically to schema 5. SQLite is the default backend;
MariaDB 11.8 remains supported through explicit database configuration.

For SQLite, stop the single application instance and back up the complete
`DATA_DIR` (database, WAL/SHM if present, and secret) before replacing the image.
Start the new image against the existing volume, then verify readiness, login,
settings and data. Do not run an old schema-4 application against the upgraded
volume. Roll back with the matching old image and the pre-upgrade backup.
For MariaDB, back up the database and application secret, coordinate all readers
and writers, and plan the automatic migration before starting the new image.
See `docs/operations.md` and `docs/sqlite-release-validation.md`.

Set `IMAGE_REGISTRY`, `IMAGE_NAME`, `REGISTRY_USERNAME`, and
`REGISTRY_PASSWORD` in the protected `release` environment. A successful
publish records the immutable registry digest in the GitHub job summary. The
release environment and protected tag are repository settings and cannot be
created by workflow YAML.

The publish metadata check accepts stable tags only (`vMAJOR.MINOR.PATCH`).
After pushing, the job pulls the resulting immutable digest and runs
`scripts/smoke-release-image.py`: exact version/commit/build-time checks,
API/Web smoke, schema-5 initialization, and persistence across two containers.
A failed smoke fails the job; the already-pushed image is not automatically
removed and must not be promoted or deployed. This job does not deploy.

## Current release preparation (2026-09-29)

The intended first stable version is `v1.0.0`. Local candidate acceptance and
external publication are separate; see `docs/release-readiness.md`.
A read-only GitHub check found only the `integration` environment, with no
listed secrets. The `release` environment is absent (404), and the rulesets
endpoint returns 403 with a GitHub Pro/public-repository requirement. Existing
protection checks remain enabled. Configure a supported protection setup,
release credentials/variables and the integration DSN before publishing; never
make the repository public merely to work around this requirement.
