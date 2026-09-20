# sentence-api

纯 Go 的只读语句 API。公开读取从原子内存快照完成，MariaDB/MySQL 只用于迁移、导入和后台刷新。当前部署验收基线为 MariaDB 11.8；MySQL 8.4 是待单独验证的兼容目标。

构建开发二进制：

```bash
go build -o sentence-api ./cmd/api
```

```bash
MYSQL_DSN='sa_test_write:change_me@tcp(db.example:3306)/sentence_api?tls=true' ./sentence-api migrate up
MYSQL_DSN='sa_test_write:change_me@tcp(db.example:3306)/sentence_api?tls=true' ./sentence-api import --file testdata/sentences.json
MYSQL_DSN='sa_test_write:change_me@tcp(db.example:3306)/sentence_api?tls=true' \
WEB_SECRET_KEY='change-me-012345678901234567890123456789' SITE_CONTACT='ops@example.com' \
./sentence-api
```

服务默认监听 `:8080`。主要端点为 `/api/v1`、`/api/v1/sentences/{uuid}`、`/api/v1/categories`、`/healthz`、`/readyz`、`/metrics` 和 `/version`。服务启动不会自动迁移或导入。

随机与 UUID 查询使用统一的语句结构：`uuid`、`content`、`category`、`source`、`author`、`length`；响应不公开数据库内部 `id`。精确数据集版本位于字符串形式的 `meta.dataset_version`。

完整的参数、方法、响应、错误、CORS、reload 和健康检查规则见 [开发规格](docs/development-spec.md)；导入 JSON 字段、严格校验、去重、冲突与事务语义见 [导入格式](docs/import-format.md)。

开发检查：

```bash
go test ./...
go test -race ./...
go vet ./...
```

设置 `MYSQL_TEST_DSN` 后，集成测试会创建随机命名的独立数据库、执行正式迁移，并在独立清理上下文中删除该数据库。未设置时测试明确跳过。

## Web 前台/后台

同一二进制还提供 `web` 子命令，启动前台站点、投稿与管理员后台；合并服务默认监听 `:8080`，生产使用一个 `app.env` 和一个 `MYSQL_DSN`：

```bash
WEB_SECRET_KEY='change-me-012345678901234567890123456789' \
SITE_CONTACT='ops@example.com' \
COOKIE_SECURE=false \
MYSQL_DSN='sa_test_write:change_me@tcp(db.example:3306)/sentence_api?tls=true' \
HTTP_ADDR=:8080 \
./sentence-api
```

中文名称、英文名称、站点标语、公开地址、联系方式、备案等到后台「站点设置」修改；上述环境变量只在空库首次启动时写入种子。

创建首个管理员（密码仅通过 stdin，需 `--password-stdin`）：

```bash
MYSQL_DSN='sa_test_write:change_me@tcp(db.example:3306)/sentence_api?tls=true' \
  ./sentence-api web admin create --username admin --password-stdin <<< 'your-secure-password'
```

常用管理命令：`web admin list`、`web admin reset-password --username U --password-stdin`、`web admin enable|disable --username U`。详见 [web-spec.md](docs/web-spec.md)。

部署拓扑：一个 `quotewisp` 容器监听 `:8080`，同一进程提供 `/api/*` 和 Web 路由；反向代理的 `/api/*` 与 `/` 均转发到该端口。生产服务环境统一放在 `/home/andan/.config/quotewisp/app.env`，其中只配置一个 `MYSQL_DSN`。

本地/CI 集成测试需设置 `MYSQL_TEST_DSN`（可创建 `sentence_api_test_%` 前缀的库）。Makefile 提供 `make smoke-web`（需合并服务已启动，默认 `BASE=http://127.0.0.1:8080`）。

Docker 示例：

```bash
docker run --rm --env-file /home/andan/.config/quotewisp/app.env -p 8080:8080 sentence-api:tag
```

部署、回滚和性能验证分别见 [operations.md](docs/operations.md)、[performance.md](docs/performance.md) 与 [acceptance.md](docs/acceptance.md)。
