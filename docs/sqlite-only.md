# SQLite 实现说明

当前仅支持 SQLite；本页替代早期开发/Web 规格中的外部数据库章节。API、导入格式、管理界面和鉴权契约保持不变。

## 存储与迁移

- 使用纯 Go `modernc.org/sqlite`，文件固定为 `DATA_DIR/quotewisp.db`；默认目录 `/var/lib/quotewisp`，密钥也保存在该目录。
- WAL、外键检查、5 秒 busy timeout、4 个连接；写事务通过 `BEGIN IMMEDIATE` 串行化。首次连接启用 WAL 遇锁竞争时在调用者期限及 5 秒上限内重试；其他错误不重试。
- schema 保持 5，没有为移除外部数据库兼容而修改已有 SQLite 表。启动逐版本事务迁移；显式 `migrate down --steps N` 仅供已备份的计划操作，不自动降级。
- 新库示例由持久化初始化标记控制；清空句库不会重新生成示例，空库快照仍可就绪。
- 已删除 MySQL 驱动、golang-migrate 依赖、外部 SQL 迁移目录及所有 SQL 方言切换。原生迁移位于 `internal/database/database.go`。

## 配置和升级

不再接受 `MYSQL_DSN`、`DB_HOST`、`DB_PORT`、`DB_NAME`、`DB_USER`、`DB_PASSWORD`、`DB_TLS` 及三项旧 `MYSQL_*` 连接池参数。即使值为空也会明确报错，错误只包含参数名，不回显值。所有 CLI/服务模式遵循相同规则。

已有 SQLite 部署继续使用原数据目录，升级前备份整个目录。外部数据库部署不能直接替换镜像；需先在旧版本导出语句 JSON，再在新 SQLite 部署导入并核对。账号、站点设置、投稿等不是语句 JSON 的一部分，不自动搬迁。详见 [运行与回滚](operations.md)。

数据库指标改为 `sentence_api_database_last_operation_success`、`sentence_api_database_last_check_timestamp_seconds` 和 `sentence_api_database_last_success_timestamp_seconds`。旧 `sentence_api_mysql_*` 查询需要更新；指标仍表示最近操作结果，不是实时探活。

## 测试与交付

所有数据库业务测试使用独立临时 SQLite 文件，不要求服务或密钥，不因缺少 DSN 而跳过。迁移、事务回滚、导入失败、重复 UUID、账号并发、会话、设置与刷新恢复均保留真实数据库测试。`make e2e-web` 在临时数据目录验证投稿、登录、审核和 API 刷新。

CI 保留 quality、fuzz、integration、image 四项门禁；integration 执行 SQLite 合约和 HTTP 闭环，image 执行 SQLite 持久化与 API/Web 验收。正式发布仍采用个人免费仓库的手动 GHCR 流程，见 [发布说明](../.github/RELEASE.md)。

2026-09-29 本地 Go 1.27.1 全量 race、vet、`CGO_ENABLED=0` 构建及 HTTP E2E 通过；首次并发初始化连续 30 次通过。历史双数据库候选的性能与验收记录保留作为历史证据，不宣称为本次新镜像的测试结果。正式发布与生产部署尚未执行。
