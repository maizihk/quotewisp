# 验收记录

日期：2026-09-18。

- MariaDB 11.8.9：已通过加密随机命名的隔离数据库迁移、正式导入、dry-run、幂等无变化导入、超过 1000 码点内容 UUID 查询、禁用分类、版本记录缺失、失败后刷新恢复、并发发布一致快照，以及第二批触发失败时第一批/新分类/版本全部回滚；测试数据库已清理。
- 严格 JSON：已覆盖重复成员、未知字段、尾随值、BOM、非法 UTF-8、孤立/错误组合 Unicode 代理项、合法代理对、合法 U+FFFD 和逐字节 reader；COMMIT 无法确认已验证固定归类为 `commit-outcome-unknown` 且不泄露底层错误。
- HTTP fuzz：查询 31.014 秒/436213 次、UUID 30.073 秒/453306 次、分类 30.089 秒/430494 次、问题响应 31.015 秒/247281 次，均通过。
- MariaDB 到 HTTP 的实际衔接已验证随机、多分类元数据、超过 1000 码点内容的 UUID 查询和分类列表；刷新校验失败期间旧快照仍通过 HTTP 返回 200。
- 开发规格 1.2 的公开语句契约已通过单元与 MariaDB 集成测试：随机和 UUID 查询统一返回 `uuid`、`content`、`category`、`source`、`author`、`length`，`meta.dataset_version` 保持字符串；响应不再序列化内部 `id` 或 `hitokoto`、`type`、`from`、`from_who`。
- 本地工具链：Go 1.27.1 与 Go 1.26.8 的 `go test ./...`、`go test -race ./...`、`go vet ./...` 均通过；两版最终 race 全仓运行均实际注入 MariaDB 连接，集成测试没有跳过。
- MySQL 8.4：兼容目标，尚未在外部 MySQL 8.4 上单独验证；MariaDB 结果不替代该项。
- 持续 10 分钟压测：当前未安装 k6，尚未执行；只交付了可运行脚本，未声称达到阈值。
- 发布与灰度：尚未执行。当前工作区没有可用 Git 历史，本地开发构建只能注入 `unknown`，不得作为发布镜像。

本地验收镜像 `sentence-api:acceptance` 已完成 smoke，image ID 为 `sha256:5e71ddfccaabb931dac2d6f6af0536d41696cfcfd12aec7955e747199ff19a08`，大小 11,822,694 字节；这是本地 image ID，不是 registry digest。构建信息为 `dev`、`unknown`、`2026-09-17T22:26:33Z`，因此只算本地验收镜像，不算正式发布。

该镜像以 UID/GID `65532:65532` 和只读根文件系统验证：migrate、import dry-run、正式 import 均退出 0；health、ready、random、UUID、categories、metrics、version 均返回 200；reload 返回 202，24 个并发 reload 中 12 个返回 202、12 个返回 409；SIGTERM 正常退出 0。测试创建的 `sa_accept_*` 数据库及 API 容器均已清理，无残留。
