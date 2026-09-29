# v1.0.0 发布准备

更新时间：2026-09-29（Asia/Shanghai）。目标版本为 `v1.0.0`，尚未发布或部署。当前应用 schema 为 5；本页替代旧 rc1/schema 4 发布清单，历史部署证据仍保留在 deployment 系列文档。

## 已完成的应用验收

- 默认 SQLite、启动自动迁移、首次 seed、空库、非 root 持久化与 MariaDB 11.8 兼容测试通过。
- 后台原生 JSON / Hitokoto 预览确认、异步导入、提交回执、刷新重试与首次管理员并发初始化保护完成。
- 全量 MariaDB 集成与 race 测试、Go vet 已通过，见 [恢复验收](sqlite-release-validation.md) 和 [性能报告](sqlite-performance-20260929.md)。
- 隔离容器完成 schema 4→5 升级、双格式导入、整卷离线备份恢复、重建持久化及旧镜像配旧备份回滚。
- 312001 条数据下，1000 RPS 持续 10 分钟：600001 请求、零错误、零丢发；整体 P95 0.221 ms，导入窗口 P95 7.888 ms。峰值内存贴近 1 GiB，部署必须按数据量和导入负载留余量；不能声称 1 GiB 有充足余量。

## 制品冻结和验收

从提交导出干净构建上下文，以 `VERSION=v1.0.0`、完整 `GIT_COMMIT`、真实 UTC `BUILD_TIME` 构建本地候选镜像。工作区已有的 `docs/environment.md` 用户改动不纳入本次发布提交。

使用不可变 image ID 运行 `scripts/smoke-release-image.py`，核对 `/version` 三项元数据、API/Web 页面、两次容器启动、schema 5、seed 和密钥持久化。再用 `scripts/verify-sqlite-release.py` 对该镜像复验升级、12000 条原生导入、Hitokoto 导入、恢复与回滚。本次候选的具体提交、image ID 和结果记录在 [正式候选验收](release-v1.0.0-validation.md)。

当前构建仅覆盖 Linux amd64，不宣称多架构发布。此前性能结果覆盖相同应用源码；仅改变版本元数据和发布脚本无需重跑完整压测，但最终镜像必须单独 smoke。

## 外部发布阻塞

2026-09-29 使用 GitHub API 只读核对 `maizihk/quotewisp`：

- 只有 `integration` 环境，其 secret 列表为空；标签门禁要求的 `MYSQL_TEST_DSN` 尚未就绪。
- `release` 环境不存在；查询变量与 secret 返回 404。需要配置 registry、镜像名称和发布凭证。
- rulesets 查询返回 403，明确提示需 GitHub Pro 或公开仓库；当前不能确认所需受保护标签可用。

不删除 `github.ref_protected`、外部数据库门禁或 reviewer 要求来绕过这些限制。需由仓库所有者决定受支持的 GitHub 保护方案并提供目标 registry 配置；不自动公开仓库、不购买套餐、不猜测凭证。

## 发布与部署顺序

1. 完成本地候选验收并保留不可变镜像身份；配置 `.github/RELEASE.md` 中的外部门禁。
2. 推送经审阅的提交与 `v1.0.0` 标签，等待 quality、fuzz、integration、image 门禁全部通过。
3. publish 写入版本元数据并推送，随后按 registry digest 拉取，执行最终制品 smoke。失败时该 digest 不得部署；推送已发生，不自动删除远端制品。
4. 记录成功流水线链接和 digest，再备份目标部署数据并部署已验收 digest。检查 readiness、API、后台登录、导入刷新与日志。
5. SQLite 回滚时停止新版实例，用升级前完整 `DATA_DIR` 备份和匹配旧镜像恢复；不能只切旧镜像而继续使用 schema 5 数据库。恢复会回到备份时点，先保留升级后的数据以便核对。

MySQL 8.4、超大 Hitokoto 专项性能和生产部署不在当前验收声明内；它们不应被描述成已经通过。历史故障/浏览器记录也不能直接充当本次最终 digest 的验收结果。
