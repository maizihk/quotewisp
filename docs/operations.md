# 运行与回滚

默认不设置数据库变量，使用 `DATA_DIR/quotewisp.db`（SQLite WAL）。`DATA_DIR` 默认 `/var/lib/quotewisp`，同时保存 `web-secret.key`。仓库 Compose 使用命名卷，镜像中的目录归 UID/GID `65532:65532` 所有，空命名卷首次挂载后可直接写入。绑定宿主机目录时需手动创建并授予相同 UID/GID，权限设为 `700`。

## 首次运行与升级

从源码运行 `docker compose up -d --build`，或使用本次改动发布后的明确镜像版本。旧 `1.0.0-rc.*` 镜像不包含 SQLite 支持。应用首次启动自动迁移并写入少量自编示例；管理员用 `web admin create --password-stdin` 在部署终端创建，不提供默认密码或公开安装页。此命令仅用于首次初始化，已有任何管理员（包括已停用账号）时会拒绝；后续账号在后台添加，恢复访问使用 `reset-password` 或 `enable`，不要重复初始化。服务不依赖预先导入句库，删除全部语句后也不会重新生成示例。

当前仅支持 SQLite。旧 `DB_*`、`MYSQL_DSN`、`MYSQL_*` 连接池变量会被拒绝，避免旧部署误启动空 SQLite 库。外部数据库不会自动转换；应先通过旧版本导出受支持的语句 JSON，在新的 SQLite 部署中导入并核对。语句导出不包含账号、会话、站点设置或投稿记录，这些需另行处理。不要直接用新版替换仍依赖外部数据库的部署。

升级前备份数据库、密钥、`.env` 和 Compose。已有绑定目录部署应继续使用原目录，不能因仓库 Compose 默认改为命名卷就丢弃原密钥。需要外部 `1panel-network` 的既有反向代理部署，应在自己的 Compose 中保留网络配置。迁移旧显式 `WEB_SECRET_KEY` 时，首次保持原值，让程序保存到原数据目录；确认成功后才移除环境变量。冲突或损坏的密钥不能静默重建。

显式 `migrate up` 可在启动前执行。数据库版本高于程序支持范围、迁移失败或配置错误时应停止升级，查看安全错误阶段与分类；不要尝试通过移除 DB 配置绕过错误。不要把 DSN、令牌或数据写入镜像。

SQLite 大批量写入可能使用临时文件。镜像预建了运行用户可写的 `/tmp`，Compose 额外挂载 默认 256 MiB 的私有 tmpfs（可通过 `SQLITE_TMPFS_SIZE` 调整）。自行使用 `docker run --read-only` 时，也要提供可写临时目录，例如 `--tmpfs /tmp:rw,noexec,nosuid,size=256m,uid=65532,gid=65532,mode=0700`；仅挂载数据库目录不足以覆盖所有大批量操作。临时目录可以清空，数据库与密钥仍只持久化在 `DATA_DIR`。若临时空间不足，应按实际数据规模调整容量。SQLite 的临时文件用途见 [官方说明](https://www.sqlite.org/tempfiles.html)。

## SQLite 备份与恢复

SQLite WAL 依赖本地磁盘，不把数据库放到网络文件系统。备份可使用 SQLite 一致性备份工具；最简单的做法是停止所有使用该数据库的应用与 CLI，再复制整个 `DATA_DIR`。复制时保留数据库、可能存在的 `-wal`/`-shm` 文件和密钥。运行中单独复制 `quotewisp.db` 不能保证包含所有已提交数据。

命名卷备份示例（使用本次本地构建镜像）：

```bash
docker compose stop quotewisp
mkdir -p backup
chmod 700 backup
docker compose cp quotewisp:/var/lib/quotewisp/. ./backup/
docker compose start quotewisp
```

为每次备份使用新的私有目录，并检查命令返回值。恢复时停止服务，保留当前目录用于回退，然后恢复整套备份并还原 UID/GID `65532:65532` 和目录 `700` 权限。启动后检查管理员登录、站点设置、数据集版本与随机查询。不得使用 `docker compose down -v` 保留数据，`-v` 会删除命名卷。

## 验收与回滚

上线检查 `/healthz`、`/readyz`、`/version`、分类、随机语句、投稿、管理员登录与审核。空库有效时 `/readyz` 仍为 200，随机查询返回 404。确认重建容器后数据、账号和密钥保留。`/metrics` 和 `/internal/reload` 应限制在受信任网络；代理应正确覆盖或追加 `X-Forwarded-For`，仅把实际受控代理网段写入 `TRUSTED_PROXY_CIDRS`。

生产 HTTPS 保持安全 Cookie；仅本地 HTTP 使用 `COOKIE_SECURE=false`。修改 `.env` 后重建容器使环境变量生效，不能只执行 `docker restart`。Compose 的 raw 环境文件要求 Compose >= 2.30，值不要添加包裹引号。

回滚先确认旧镜像支持目标 SQLite schema。SQLite 不自动降级；需要回退时停止应用，恢复升级前整个数据目录备份并使用匹配镜像。不能只切回不兼容旧镜像而继续使用升级后的数据库。

收到 SIGTERM/SIGINT 后 readiness 失败，后台任务取消，HTTP 请求在 `SHUTDOWN_TIMEOUT` 内排空。超时会强制关闭连接并以失败退出。后台 API 调用统计属于进程内计数，重启后从零开始。

全部数据库测试使用临时 SQLite 文件，不依赖外部数据库或凭证。执行 `go test -race ./...`、`go vet ./...` 和 `make e2e-web`。历史 `docs/deployment-*.md` 与单容器部署记录不代表当前 SQLite-only 版本已部署。

可复现的命名卷升级、后台导入、离线备份恢复和旧镜像回滚演练见 [SQLite 发布验收记录](sqlite-release-validation.md)，对应脚本为 `scripts/verify-sqlite-release.py`。该演练使用独立测试卷，不修改现有部署。

数据库观测指标已统一为 `sentence_api_database_last_operation_success`、`sentence_api_database_last_check_timestamp_seconds` 和 `sentence_api_database_last_success_timestamp_seconds`；旧 `sentence_api_mysql_*` 仪表盘查询需同步更新。
