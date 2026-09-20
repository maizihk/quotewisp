# 运行与回滚

默认 `.env` 需要四个数据库变量：`DB_HOST`、`DB_NAME`、`DB_USER`、`DB_PASSWORD`；`DB_PORT` 默认 `3306`，`DB_TLS` 可选。旧 `MYSQL_DSN` 仍兼容，但不可与 `DB_*` 混用；新部署应把原来的 DSN 拆成这四项。`DATA_DIR` 默认 `/var/lib/quotewisp`，Compose 中对应挂载目录 `./data`。密钥首次启动自动保存到该目录；目录初始化见 README 的 Compose 部署步骤。联系方式可在后台设置。

首次部署按“迁移、导入、启动服务”执行。生产形态是一个 `quotewisp` 容器监听 `:8080`，同一进程提供 API、前台和管理后台；不要把 DSN、令牌或导入数据写入镜像。统一运行账号沿用 `sa_test_write`，审核后仅补目标库所需 DDL 权限，使同一组数据库变量可用于迁移、导入和运行。旧 read/migrate 账号在新部署验收和回滚兼容性确认前保留。

从旧环境升级时，先备份 `.env` 和 Compose 文件。使用旧 `WEB_SECRET_KEY`、旧数据库 DSN（或等价的四项 `DB_*`）和新的 `./data` 挂载启动一次，让服务把相同密钥保存到 `data/web-secret.key`；确认启动成功后，移除 `.env` 中的 `WEB_SECRET_KEY`，再按新的四项数据库变量配置并重建容器。密钥文件损坏或显式密钥与已保存密钥冲突时，启动必须失败，不能静默覆盖或重新生成。

镜像使用不可变版本或 digest。发布前注入程序版本、完整提交哈希和 UTC 构建时间；缺少真实 Git 元数据的本地构建只能作为开发验证，不能发布。容器监听 8080，可使用只读根文件系统。Nginx 应覆盖或正确追加 `X-Forwarded-For`，`TRUSTED_PROXY_CIDRS` 只配置实际受控代理网段。

上线检查顺序：确认迁移状态为版本 1、2、3 或 4 且非 dirty。当前单进程支持 API 与 Web，执行 `migrate up` 后导入数据，再启动 `quotewisp`（默认 `:8080`）。仅早期旧读 API 只接受 schema 1–3；当前 RC1 支持 schema 4，RC1 应用回滚无需执行 `migrate down`。执行 import dry-run；执行正式导入；检查 `/healthz`、`/readyz`、`/version`、分类和随机查询；观察刷新、数据库和请求指标；最后由外部代理逐步切流。指标和 `/internal/reload` 应限制在受信任网络。OpenResty 将 `/api/` 和其余路径都转到单容器 `:8080`。后台概况的接口调用次数使用同一进程内统计，不再配置 `API_METRICS_URL`，重启后从 0 累加。

应用回滚通过代理切回前一个已验证镜像 digest。RC2 回滚时必须同时还原旧 `.env` 和旧镜像；需要回到只支持 schema 3 的旧 web 时，先完成备份并停止依赖品牌新字段的 web，再显式执行 `migrate down --steps 1`；000004 回滚会删除英文名称和标语两列，但保留 `site_name` 及其他站点设置。数据库迁移回滚与应用切流是独立操作。数据回滚应通过遵循版本锁协议的补偿写入或数据库恢复完成；恢复后需递增版本、手动 reload 或重启，避免相同版本号掩盖内容变化。

收到 SIGTERM/SIGINT 后 readiness 会失败，后台轮询和刷新被取消，HTTP 请求在同一个 `SHUTDOWN_TIMEOUT` 内排空。超时会强制关闭连接并以失败退出。

当前部署目录为 `/home/andan/deploy/quotewisp`，其中放置 `compose.yaml` 与 `.env`；目录权限为 `700`，`.env` 权限为 `600`。`.env` 的值不加包裹引号，密码中的 `$` 按 literal 保留。Compose 使用 `env_file` 的 `format: raw`，要求 Compose `>=2.30`。当前镜像为 rc2，默认绑定 IP 为 `172.16.99.100`、主机端口为 `18080`、网络为外部 `1panel-network`。新机器需修改 IP、DSN 和网络，并先确保外部网络已存在。

Compose 迁移已完成。在该目录执行 `docker compose config -q` 可校验配置；修改 `.env` 后执行 `docker compose up -d --force-recreate`，不要只执行 `docker restart`。当前只运行一个 app 服务，不创建数据库或 Nginx。活动环境文件为 `/home/andan/deploy/quotewisp/.env`；旧 `app.env` 仅作为历史/旧回滚配置。迁移状态和一次性回滚容器信息位于 `/home/andan/deploy/quotewisp/migration-state.json`；旧 `quotewisp-rollback-086f29f26c23442dafdfac788d398564` 已停止且 `restart=no`。

本机 MariaDB 11.8.9 验收可运行 `scripts/test-mariadb.sh`。脚本默认连接本机 `127.0.0.1:3306` 的 `MariaDB` 容器，只在子进程内读取已有 root 密码，不输出或保存凭证；它创建加密随机命名的专用数据库并清理。无参数时运行详细集成测试；传入参数时原样交给 `go test`，例如 `scripts/test-mariadb.sh -race ./...`。可用 `MARIADB_TEST_CONTAINER`、`GO_CMD` 和 `GOTMPDIR` 覆盖本机默认值；自定义临时目录路径应保持较短，以免 Unix socket 测试超过系统路径上限。
