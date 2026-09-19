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
accepts schema versions 1–4; `web` requires 4. Upgrade by replacing every read
API instance with the current image first, running the packaged migration to
schema 4, and then starting the current web container.

Set `IMAGE_REGISTRY`, `IMAGE_NAME`, `REGISTRY_USERNAME`, and
`REGISTRY_PASSWORD` in the protected `release` environment. A successful
publish records the immutable registry digest in the GitHub job summary. The
release environment and protected tag are repository settings and cannot be
created by workflow YAML.
