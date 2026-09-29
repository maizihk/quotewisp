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

使用不可变 image ID 运行 `scripts/smoke-release-image.py`，核对 `/version` 三项元数据、API/Web 页面、两次容器启动、schema 5、seed 和密钥持久化。再用 `scripts/verify-sqlite-release.py` 对该镜像复验升级、12000 条原生导入、Hitokoto 导入、恢复与回滚。本次候选已通过上述验收，具体提交、image ID 和结果记录在 [正式候选验收](release-v1.0.0-validation.md)。

当前构建仅覆盖 Linux amd64，不宣称多架构发布。此前性能结果覆盖相同应用源码；仅改变版本元数据和发布脚本无需重跑完整压测，但最终镜像必须单独 smoke。

## GitHub Free 发布方案

按仓库所有者要求，现已改为个人免费私有仓库流程。此前查询发现环境和付费标签保护不可用，现不再将它们作为发布前提：

- 不依赖 `github.ref_protected`、release/integration 环境或人工 reviewer。
- MariaDB 11.8 集成测试使用 CI 临时服务，每次实际执行完整 race 测试，无需外部 DSN 密钥。
- 默认推送至 `ghcr.io/maizihk/quotewisp`，使用 `GITHUB_TOKEN`，无需额外 registry 密钥。新包默认私有，不自动改变可见性。
- 普通 push/PR/tag 只检查；仅手动在正式标签上指定 `publish=true`，且四项测试门禁通过后才发布。
- 未保护的标签和镜像版本标签可以被有权限者改动，手动触发也不是独立审批。保留提交哈希和镜像 digest，部署使用 digest。

仍需把工作流合入默认分支并推送预期标签；确认 Actions 可运行、有可用额度。若 GHCR 同名包已存在，需要授予本仓库 Actions 写入权限。当前尚未推送本次工作流、执行远端发布或修改任何仓库可见性。详细操作见 [发布配置](../.github/RELEASE.md)。

## 免费方案配置验证

2026-09-29 本地检查通过：workflow YAML 解析、所有 run 脚本语法、默认不发布、手动发布条件、四项依赖门禁、无付费 environment 依赖、正式标签接受及分支/预发布标签拒绝。

使用与 CI 一致的 MariaDB 11.8 临时容器配置，实际执行 `go test -count=1 -race ./...` 全部通过（Go 1.27.1）；临时容器已删除。测试数据库镜像 digest 为 `sha256:79d59758afc91b89b120b0a8904d637f5a3b3e1c4900f29b740d6d46c72fef68`。这是本地验证，GitHub 托管 runner 和 GHCR 的真实发布尚未执行。

## 发布与部署顺序

1. 完成本地候选验收并保留不可变镜像身份，将新版工作流合入默认分支。
2. 推送经审阅的提交与 `v1.0.0` 标签，运行 `gh workflow run ci.yml --repo maizihk/quotewisp --ref v1.0.0 -f publish=true`；本次手动运行内的 quality、fuzz、integration、image 必须全部通过。
3. publish 写入版本元数据并推送，随后按 registry digest 拉取，执行最终制品 smoke。失败时该 digest 不得部署；推送已发生，不自动删除远端制品。
4. 记录成功流水线链接和 digest，再备份目标部署数据并部署已验收 digest。检查 readiness、API、后台登录、导入刷新与日志。
5. SQLite 回滚时停止新版实例，用升级前完整 `DATA_DIR` 备份和匹配旧镜像恢复；不能只切旧镜像而继续使用 schema 5 数据库。恢复会回到备份时点，先保留升级后的数据以便核对。

MySQL 8.4、超大 Hitokoto 专项性能和生产部署不在当前验收声明内；它们不应被描述成已经通过。历史故障/浏览器记录也不能直接充当本次最终 digest 的验收结果。
