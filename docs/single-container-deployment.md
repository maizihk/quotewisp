# 单容器部署记录

部署完成后记录（2026-09-20）：生产运行一个 `quotewisp` 容器，绑定 `172.16.99.100:18080 -> 8080`，使用镜像 `sentence-api:1.0.0-rc.2`，镜像 ID 为 `sha256:810ac96c59935dfa6dc3b403ad25ccd803fc021749b7d10f8d84baeaeabc7f41`，提交 `acc1f0067ed7adc767cd6c0b377ed44cb2e2bc17`。

活动环境文件为 `/home/andan/deploy/quotewisp/.env`，权限 `600`。统一数据库账号为 `sa_test_write`；`sa_test_read` 与 `sa_test_migrate` 已删除。最终核验显示仅剩 `sa_test_write@%`，无库外权限、无 `GRANT OPTION`。

全量 MariaDB 测试、race、vet 和隔离业务验收通过。另有隔离负载记录：60 秒、11039 条句子、253056 请求、0 错误、P95 26.47 ms；该结果不是 1 vCPU 标准正式压测，也不代表全面性能合格。

当前回滚状态记录为 `6441c07b99624397b5caa94dd33b71ea`。持久回滚脚本位于 `/home/andan/.config/quotewisp/rollback-unified.py`，权限 700；状态文件位于 `/home/andan/.config/quotewisp/rollback-6441c07b99624397b5caa94dd33b71ea/state.json`。脚本已 dry-run，旧镜像使用统一 `app.env` 的 API/Web smoke 已通过，但未实际执行整套回滚。回滚需使用 `app.env` 以 RC1 镜像重建 API/Web；旧容器原有 read/migrate env 不作为回滚凭据，不能直接 restart 依赖旧账号的容器。

生产切换后，写入成功会主动刷新 API 与 Web 两个缓存；版本轮询用于处理外部更新。旧 `api.env` 与 `web.env` 仅保留为历史归档，不能直接作为当前回滚配置。

Compose 启动方式迁移已完成：`/home/andan/deploy/quotewisp` 放置 `compose.yaml` 和 `.env`（目录 700、`.env` 600），并已通过 `docker compose config -q` 与启动核验。更新 `.env` 后执行 `docker compose up -d --force-recreate`。当前只运行一个 app 服务，复用外部 `1panel-network`，不创建数据库或 Nginx。迁移状态位于 `/home/andan/deploy/quotewisp/migration-state.json`；一次性回滚容器已清理，不能直接启动历史容器。需要回滚时，使用保留的 RC1 镜像和统一 `app.env` 重建容器。旧 `app.env` 与 migration-state 仅保留为历史/旧回滚记录。
