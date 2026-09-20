# RC3 部署记录（2026-09-20）

- 镜像：`sentence-api:1.0.0-rc.3`
- Image ID：`sha256:95213865d19c0b12501e9d841db02c36c33d995e60e352f9e3448083ecbd65b2`
- Commit：`7d069674b8266b6fb70826d4b043dbc0fc9f2edf`
- Build time：`2026-09-20T02:26:08Z`
- VM LAN：`172.16.99.100:18080`（保持不变）

部署沿用 `sentence_api_test` 数据库和 `sa_test_write` 用户。生产 `.env` 使用四个 `DB_*` 字段；数据目录中的 `data/web-secret.key` 权限为 `0600`、归属 UID `65532`，并与旧 secret 一致。`TRUSTED_PROXY_CIDRS` 保留在 Compose environment 中。

迁移前数据库共有 11039 条记录。部署后 HTTPS、`/`、`/healthz`、`/readyz`、`/api/v1`、`/admin/login`、`/version` 均返回 200；版本为 rc3，snapshot 为 4。

回滚资料：旧 rc2 镜像保留。`/home/andan/deploy/quotewisp/.upgrade-backup-rc2` 中的原 `.env` 是未生效的 `quotewisp` 库配置，不能用于回滚；正确回滚应使用从旧容器实际 `Env` 提取的 `rollback.env`，并恢复备份的 Compose 文件。
