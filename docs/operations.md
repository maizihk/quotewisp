# 运行与回滚

首次部署按“迁移、导入、启动服务”执行。迁移、导入和 API 建议分别使用最小权限账号，并通过 `MYSQL_DSN` 注入；不要把 DSN、令牌或导入数据写入镜像。服务账号需要读取 schema、版本、分类和语句，导入账号需要相应读写权限，迁移账号需要 DDL 权限。

镜像使用不可变版本或 digest。发布前注入程序版本、完整提交哈希和 UTC 构建时间；缺少真实 Git 元数据的本地构建只能作为开发验证，不能发布。容器监听 8080，可使用只读根文件系统。Nginx 应覆盖或正确追加 `X-Forwarded-For`，`TRUSTED_PROXY_CIDRS` 只配置实际受控代理网段。

上线检查顺序：确认迁移状态为版本 1 且非 dirty；执行 import dry-run；执行正式导入；启动新实例；检查 `/healthz`、`/readyz`、`/version`、分类和随机查询；观察刷新、数据库和请求指标；最后由外部代理逐步切流。指标和 `/internal/reload` 应限制在受信任网络。

应用回滚通过代理切回前一个已验证镜像 digest。数据库迁移回滚是独立操作，先完成备份并审查 down SQL，再显式运行 `migrate down --steps N`。数据回滚应通过遵循版本锁协议的补偿写入或数据库恢复完成；恢复后需递增版本、手动 reload 或重启，避免相同版本号掩盖内容变化。

收到 SIGTERM/SIGINT 后 readiness 会失败，后台轮询和刷新被取消，HTTP 请求在同一个 `SHUTDOWN_TIMEOUT` 内排空。超时会强制关闭连接并以失败退出。

本机 MariaDB 11.8.9 验收可运行 `scripts/test-mariadb.sh`。脚本默认连接本机 `127.0.0.1:3306` 的 `MariaDB` 容器，只在子进程内读取已有 root 密码，不输出或保存凭证；它创建加密随机命名的专用数据库并清理。无参数时运行详细集成测试；传入参数时原样交给 `go test`，例如 `scripts/test-mariadb.sh -race ./...`。可用 `MARIADB_TEST_CONTAINER`、`GO_CMD` 和 `GOTMPDIR` 覆盖本机默认值；自定义临时目录路径应保持较短，以免 Unix socket 测试超过系统路径上限。
