# sentence-api

纯 Go 的只读语句 API。公开读取从原子内存快照完成，MariaDB/MySQL 只用于迁移、导入和后台刷新。当前部署验收基线为 MariaDB 11.8；MySQL 8.4 是待单独验证的兼容目标。

构建开发二进制：

```bash
go build -o sentence-api ./cmd/api
```

```bash
MYSQL_DSN='migration_user:change_me@tcp(db.example:3306)/sentence_api?tls=true' ./sentence-api migrate up
MYSQL_DSN='import_user:change_me@tcp(db.example:3306)/sentence_api?tls=true' ./sentence-api import --file testdata/sentences.json
MYSQL_DSN='api_read_user:change_me@tcp(db.example:3306)/sentence_api?tls=true' ./sentence-api
```

服务默认监听 `:8080`。主要端点为 `/api/v1/sentences/random`、`/api/v1/sentences/{uuid}`、`/api/v1/categories`、`/healthz`、`/readyz`、`/metrics` 和 `/version`。服务启动不会自动迁移或导入。

随机与 UUID 查询使用统一的语句结构：`uuid`、`content`、`category`、`source`、`author`、`length`；响应不公开数据库内部 `id`。精确数据集版本位于字符串形式的 `meta.dataset_version`。

完整的参数、方法、响应、错误、CORS、reload 和健康检查规则见 [开发规格](docs/development-spec.md)；导入 JSON 字段、严格校验、去重、冲突与事务语义见 [导入格式](docs/import-format.md)。

开发检查：

```bash
go test ./...
go test -race ./...
go vet ./...
```

设置 `MYSQL_TEST_DSN` 后，集成测试会创建随机命名的独立数据库、执行正式迁移，并在独立清理上下文中删除该数据库。未设置时测试明确跳过。

部署、回滚和性能验证分别见 [operations.md](docs/operations.md)、[performance.md](docs/performance.md) 与 [acceptance.md](docs/acceptance.md)。
