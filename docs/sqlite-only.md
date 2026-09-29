# SQLite 存储

数据库和密钥位于同一持久化数据目录；部署、备份与恢复见 [运行文档](operations.md)。

## 存储与迁移

- 使用纯 Go `modernc.org/sqlite`，文件固定为 `DATA_DIR/quotewisp.db`；默认目录 `/var/lib/quotewisp`，密钥也保存在该目录。
- WAL、外键检查、5 秒 busy timeout、4 个连接；写事务通过 `BEGIN IMMEDIATE` 串行化。首次连接启用 WAL 遇锁竞争时在调用者期限及 5 秒上限内重试；其他错误不重试。
- 当前 schema 为 5。启动逐版本事务迁移；显式 `migrate down --steps N` 仅供已备份的计划操作，不自动降级。
- 新库示例由持久化初始化标记控制；清空句库不会重新生成示例，空库快照仍可就绪。
- 迁移实现位于 `internal/database/database.go`。

## 数据与事务

语句、分类、投稿、管理员、会话、站点设置和导入任务持久化在 SQLite 中。语句与分类写入通过数据集版本协调快照刷新；导入数据和提交回执在同一事务中保存，刷新失败可以独立重试。

SQLite 使用本地磁盘，不应将数据库放在网络文件系统。数据目录权限为 `700`，容器运行用户为 `65532:65532`。批量写入和排序可能使用临时文件，应为 `/tmp` 留出可写空间。

## 观测

数据库指标为 `sentence_api_database_last_operation_success`、`sentence_api_database_last_check_timestamp_seconds` 和 `sentence_api_database_last_success_timestamp_seconds`，表示最近一次数据库操作结果及时间。
