# 运行与回滚

首次部署按“迁移、导入、启动服务”执行。迁移、导入和 API 建议分别使用最小权限账号，并通过 `MYSQL_DSN` 注入；不要把 DSN、令牌或导入数据写入镜像。服务账号需要读取 schema、版本、分类和语句，导入账号需要相应读写权限，迁移账号需要 DDL 权限。

镜像使用不可变版本或 digest。发布前注入程序版本、完整提交哈希和 UTC 构建时间；缺少真实 Git 元数据的本地构建只能作为开发验证，不能发布。容器监听 8080，可使用只读根文件系统。Nginx 应覆盖或正确追加 `X-Forwarded-For`，`TRUSTED_PROXY_CIDRS` 只配置实际受控代理网段。

上线检查顺序：确认迁移状态为版本 1、2、3 或 4 且非 dirty。当前读 API 与 import 可在 schema 1–4 上启动；`web` 需要版本 4。升级必须先把所有读 API 实例替换成支持 schema 4 的当前镜像，再执行 `migrate up`，最后启动当前 `sentence-web`（同一镜像，命令 `/sentence-api web`，默认 `:8081`）。旧读 API 只接受 schema 1–3，不能跨过这一步直接迁移。迁移 000004 只新增可空的 `english_name` 与 `slogan`，不会改写已有 `site_name`。执行 import dry-run；执行正式导入；启动新实例；检查 `/healthz`、`/readyz`、`/version`、分类和随机查询；观察刷新、数据库和请求指标；最后由外部代理逐步切流。指标和 `/internal/reload` 应限制在受信任网络。OpenResty 将 `/api/` 转到读 API，其余转到 web。中文名称、英文名称、标语、域名、联系方式和备案在后台「站点设置」修改，不必重建镜像。后台概况的接口调用次数需要 web 容器能访问读 API 内网 `/metrics`；Docker 部署应使用 API 服务容器名，例如 `API_METRICS_URL=http://sentence-api-test:8080/metrics`，不能使用指向 web 容器自身的 `127.0.0.1`。该计数在读 API 重启后从 0 累加。

应用回滚通过代理切回前一个已验证镜像 digest。回滚到只支持 schema 3 的旧 web 前，先完成备份并停止依赖品牌新字段的 web，再显式执行 `migrate down --steps 1`；000004 回滚会删除英文名称和标语两列，但保留 `site_name` 及其他站点设置。数据库迁移回滚与应用切流是独立操作。数据回滚应通过遵循版本锁协议的补偿写入或数据库恢复完成；恢复后需递增版本、手动 reload 或重启，避免相同版本号掩盖内容变化。

收到 SIGTERM/SIGINT 后 readiness 会失败，后台轮询和刷新被取消，HTTP 请求在同一个 `SHUTDOWN_TIMEOUT` 内排空。超时会强制关闭连接并以失败退出。

本机 MariaDB 11.8.9 验收可运行 `scripts/test-mariadb.sh`。脚本默认连接本机 `127.0.0.1:3306` 的 `MariaDB` 容器，只在子进程内读取已有 root 密码，不输出或保存凭证；它创建加密随机命名的专用数据库并清理。无参数时运行详细集成测试；传入参数时原样交给 `go test`，例如 `scripts/test-mariadb.sh -race ./...`。可用 `MARIADB_TEST_CONTAINER`、`GO_CMD` 和 `GOTMPDIR` 覆盖本机默认值；自定义临时目录路径应保持较短，以免 Unix socket 测试超过系统路径上限。
