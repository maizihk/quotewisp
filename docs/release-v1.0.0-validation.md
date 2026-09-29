# v1.0.0 本地正式候选验收

验收日期：2026-09-29。结论：本地候选制品通过；尚未创建正式 Git 标签、推送 registry 或部署生产环境。

## 制品身份

- 版本：`v1.0.0`。
- 源码提交：`e189895784c2b931cb04c13047d9aa0b94f8ce4b`。
- UTC 构建时间：`2026-09-29T08:36:34Z`。
- 本地镜像别名：`quotewisp:v1.0.0-candidate`。
- 不可变本地 image ID：`sha256:06862886bc408afa417390ffed1e9b402fc3c66fd4ad123e7f29073c18d1d4b5`。
- 平台：Linux amd64；Go 1.27.1，`CGO_ENABLED=0`，scratch，UID/GID 65532。

构建上下文来自该提交的 `git archive`，不包含工作区未提交改动。此 image ID 是本地 Docker 身份，不宣称已经存在 registry 发布 digest。证据文档在构建后补录；若正式标签选择后续提交，必须重新构建并核对其提交元数据，不能把本镜像归属到后续提交。

## 本次结果

1. `/version` 的 version、git_commit、build_time 与构建参数完全一致。
2. 最终镜像使用只读根文件系统、非 root 用户、独立命名卷和 256 MiB 私有 `/tmp`；重建两次均通过 API/Web smoke，包括 healthz、readyz、首页、文档、投稿、数据集、管理入口跳转及错误路径状态码。
3. 首次 schema 5、示例 1 条、管理员 0；重建后示例不重复、密钥不变化，SQLite 完整性及外键检查通过。
4. 同一最终镜像从 schema 4 升级到 5，保留管理员、站点设置、原有语句和密钥；导入 12000 条原生 JSON 与 1 条 Hitokoto，最终 12002 条语句，CSRF 和重复确认检查通过。
5. 整卷离线备份恢复到新卷、重建持久化和导入回执恢复通过。旧镜像拒绝 schema 5，升级前备份配匹配旧镜像可回滚。
6. 负向验证：故意传入错误版本号，验收脚本非零退出并报告元数据不匹配；隔离容器与测试卷已清理。
7. 六个 CI fuzz 目标均在 Go 1.27.1 下各运行 30 秒、2 个 worker，通过；[执行计数](validation/release-v1.0.0-20260929/fuzz.json)已记录。Go 1.26.8 的远端矩阵不在本次本地复验内。
8. 发布工作流 YAML 解析通过，正式标签过滤的正反例检查通过。远端流水线、registry 拉取后的验收及真实部署尚未执行。

可复核数据：[构建参数](validation/release-v1.0.0-20260929/build.json)、[最终镜像 smoke](validation/release-v1.0.0-20260929/smoke.json)、[升级恢复演练](validation/release-v1.0.0-20260929/restore.json)、[负向检查](validation/release-v1.0.0-20260929/negative.txt)。

## 与既有证据的关系

应用源码与 `5f4dd9c` 一致，本次只修改发布文档、CI 和验收脚本；沿用已有全量 race、MariaDB 集成、vet 和 [并发导入性能报告](sqlite-performance-20260929.md)。上述性能数据并非对本次 image ID 重新压测；本次单独完成了最终制品功能和恢复验收。

31.2 万条数据测试的峰值内存贴近 1 GiB，正式部署需留余量。MySQL 8.4、多架构、真实生产部署不在验收范围内。外部发布配置缺项见 [发布准备](release-readiness.md)。
