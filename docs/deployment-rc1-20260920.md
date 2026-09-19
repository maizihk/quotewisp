# 1.0.0-rc.1 双容器部署记录

部署时间：2026-09-20 06:34:17–06:34:23 +08。部署使用本地冻结提交 `2683eff6cd9ea019be92d3b9331e904420a02847` 构建的 `sentence-api:1.0.0-rc.1`，image ID `sha256:6a9c025647fd1b64a5d4ea4db62740d59cde51d6ff1d53af9cb45005052f8ee2`。API 保持 `172.16.99.100:18080 -> 8080`，Web 保持 `172.16.99.100:18081 -> 8081`，均使用 `1panel-network`、入口 `/sentence-api`、用户 `65532:65532`、restart `unless-stopped`，无挂载和资源限制。

部署后实际探针均通过：API/Web `/healthz` 返回 `status=ok`；`/readyz` 返回 `status=ready`、dataset version 4；`/version` 均为 version `1.0.0-rc.1`、commit `2683eff6cd9ea019be92d3b9331e904420a02847`、build time `2026-09-19T22:26:00Z`。完整脱敏证据在 `.cache/release-readiness/rc1-deployment.json`，包括实际容器 ID、端口、网络、命令、User、restart、挂载/资源比较、Env 等价布尔值和候选残留检查。

保留的精确回滚容器为：

- API：`sentence-api-test-rollback-rc1-2f8bae5d`，旧镜像 `sentence-api:overview-20260919t153429z`。
- Web：`sentence-web-test-rollback-rc1-7c7d9a4b`，旧镜像 `sentence-api:web-candidate-20260920t2158z`。

当前 rollback 容器为 stopped、restart `no`。回滚时先停止并删除当前同名 rc1 容器，确认其 ID 是本次部署记录的 newID；再将对应 rollback 容器改回 `sentence-api-test` 或 `sentence-web-test`，执行 `docker update --restart unless-stopped <name>`、`docker start <name>`，最后分别访问原绑定地址的 `/healthz`、`/readyz`、`/version` 并核对旧镜像版本。本文仅记录命令和顺序，未执行回滚。

部署后 HTTPS 与浏览器复验通过：`.cache/rc1-final/public-http.json` 的预期状态全部通过；`.cache/rc1-final/browser/report.json` 的 12 个样本、5 个公开页面、真实换句/复制、1000 字键盘行为和资源 hash 全部通过。1000 字样本由浏览器拦截构造，仅用于验收，不写入生产数据。

部署脚本没有迁移数据库，也没有修改 OpenResty、TLS、网络或其他旧 rollback 容器。临时候选容器检查无残留。稳定版本发布和 registry 推送尚未执行。
