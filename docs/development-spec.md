# Go 语句 API 开发规格

本轮增量：默认 SQLite（`DATA_DIR/quotewisp.db`）、SQLite/MySQL 自动迁移、仅新库一次性自编示例及有效空快照支持。下文保留首版规格；涉及仅 MySQL、手动迁移、空库拒绝启动和分进程部署的旧描述，以 [README](../README.md) 和 [运行与回滚](operations.md) 为准。后台文件导入仍按 [后续计划](next-development.md) 待开发；本地变更不代表旧发布镜像或历史验收结果已更新。

版本：1.3\\
状态：读 API 已实现并验证，见 [验收记录](acceptance.md)；1.3 前台、投稿与后台以实现代码和 [前台规格](web-spec.md) 为准\\
修订日期：2026-09-19\\
目标：独立实现一个可生产部署的纯 Go 语句 API，以及与之配套的前台站点、公开投稿和管理员后台。

本文件为当前开发基线，替代 1.2 版。配套的 [数据导入格式](import-format.md) 与 [前台规格](web-spec.md) 与本文件具有相同约束力。本文中的“必须”属于验收要求，“建议”属于推荐做法。本文与实现、测试对齐；冲突时改代码或回改本文，禁止两套说法并存。

## 1. 项目边界

本项目只负责 Go API 服务及其镜像。

包含：

- HTTP JSON API。
- 外部 MySQL 的连接、数据读取和数据库迁移文件。
- 内存快照与随机语句索引。
- 后台数据刷新。
- 健康检查、结构化日志、指标和版本信息。
- 独立迁移命令与数据导入命令。
- 单元测试、fuzz 测试、连接外部数据库的集成测试及性能测试工具。
- Dockerfile、CI、运行及回滚文档。

不包含：

- MySQL 容器的创建、备份、复制和运维。
- Nginx 容器、HTTPS 证书和反向代理配置。
- Docker Compose。
- JSONP、可执行 JavaScript、GBK、网易云音乐等历史兼容功能。
- 自动下载第三方语句库。

1.3 起，前台站点、投稿、审核和管理后台由同一二进制的 `sentence-api web` 子命令承担，作为独立容器 `sentence-web` 部署，规格见 [前台规格](web-spec.md)。该进程与本文件描述的读 API 共享镜像、数据库、数据模型和版本协议，但读 API 自身仍然只读、只用只读账号，本文件对读 API 的全部约束不变。

部署方提供 MySQL DSN，并将 Nginx 流量按路径前缀分别转发至读 API 的 `8080` 端口和 `sentence-web` 的 `8081` 端口。影子流量、灰度切流、外部访问控制、基础设施及数据库备份由部署方负责；项目提供验证工具、检查说明和回滚步骤。

## 2. 设计目标

1. 公开读取接口不直接查询 MySQL，正常读取全部从内存完成。
2. 多分类查询必须在所有符合条件的语句中均匀随机，不能先等概率随机分类。
3. 刷新期间不中断请求；新快照构建成功后原子切换。
4. 业务响应只使用 JSON；错误使用 `application/problem+json`。指标使用 Prometheus 文本格式。
5. 对输入执行明确、严格、可测试的校验。
6. 内存用于优化和容量规划：约 1.2 万条数据时，稳定内存 128 MiB、刷新峰值 220 MiB 和容器配置 256 MiB 作为参考值，不作为硬性验收门槛或数据准入条件。
7. MySQL 短暂不可用时继续使用最后一次成功加载的快照。
8. 项目代码独立实现，不复制其他一言项目的源代码。
9. 不因内存参考值设置语句总数、分类总数、文本总量或导入文件大小上限；按实际数据规模测量并配置部署资源。

## 3. 技术栈

- Go：使用当前稳定版本，CI 同时覆盖当前版本和上一稳定版本。1.2 沿用 Go 1.27 与 Go 1.26 的工具链基线；构建使用 1.27 的维护补丁版本，`go.mod` 最低版本为 1.26。维护时同步更新 CI、Dockerfile、最低版本声明及验证记录。
- HTTP：首版使用标准库 `net/http`，当前路由数量不需要额外路由框架。
- 日志：标准库 `log/slog`，输出 JSON。
- 数据库：`database/sql` + `go-sql-driver/mysql`。
- 当前部署验收基线：MariaDB 11.8、InnoDB、`utf8mb4`，已在 MariaDB 11.8.9 实测；MySQL 8.4 为兼容目标，仍需单独完成集成验证。首版保持两者共通的 SQL 和事务语义，不建立多数据库兼容框架。
- 迁移：`golang-migrate/migrate`，迁移文件随程序发布。
- UUID：`google/uuid`。
- 指标：Prometheus 文本格式，可使用官方客户端库。
- 配置：环境变量，不引入重量级配置框架。
- 测试：标准库 `testing`、`httptest` 和 fuzz test。

依赖越少越好。不采用完整 Web 框架、ORM、Redis 或运行时插件系统。依赖锁定在 `go.mod`、`go.sum` 中。

首版只实现本文已经定义的功能。使用普通结构体、函数和少量明确的并发控制，不预建通用仓储、任务调度、事件总线、配置热更新或插件扩展框架。接口只用于已有的替换或测试需要。

## 4. 总体架构

```text
                    外部 Nginx
                         │
                         ▼
                  Go API :8080
                    │        │
          只读内存快照        │ 后台版本检查与刷新
                    ▲        ▼
                    └── 外部 MySQL
```

请求路径：

```text
HTTP 请求 → 参数校验 → 读取一次当前快照 → 查询/随机选择 → JSON 响应
```

刷新路径：

```text
轮询发现版本变化 / 手动触发
→ 取得唯一刷新执行权
→ 在同一只读 REPEATABLE READ 事务内读取版本、分类、已发布语句
→ 结束读取事务 → 完成索引及完整校验
→ atomic.Pointer 原子切换 → 旧快照不再被引用后由 GC 回收
```

轮询时读到的版本只用于触发检查，快照版本必须取自加载事务。每个 HTTP 请求只读取一次当前指针，响应中的数据和版本必须来自同一个快照。

MySQL 是数据源，不是公开读取接口的实时依赖。

## 5. 项目目录

```text
sentence-api/
├── cmd/
│   └── api/
│       └── main.go
├── internal/
│   ├── config/
│   ├── database/
│   ├── httpapi/
│   ├── importer/
│   ├── snapshot/
│   └── observability/
├── migrations/
├── testdata/
├── docs/
│   ├── development-spec.md
│   ├── import-format.md
│   ├── operations.md
│   └── performance.md
├── scripts/
├── .github/workflows/
├── Dockerfile
├── .dockerignore
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

包按实际职责划分：`config` 解析配置；`database` 管理连接、迁移和数据库读取；`snapshot` 保存模型、构建索引并管理刷新；`httpapi` 实现路由、handler 和中间件；`importer` 实现文件校验和导入事务；`observability` 提供日志和指标。handler 与中间件先用同包文件组织，不预先拆分子包。

禁止创建只有转发作用的接口和空包。无需独立的 domain、repository、service 三层或依赖注入容器。上图是建议交付结构，可按实际代码继续合并，不要求凑齐目录，也不表示所有文件已存在。

## 6. MySQL 数据模型与数据约束

数据库与表的字符集统一使用 `utf8mb4`。数据库会话和应用时间统一使用 UTC，连接池必须建立经过规范化的会话配置。

### 6.1 categories

```sql
CREATE TABLE categories (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    code        VARCHAR(32) COLLATE utf8mb4_bin NOT NULL,
    name        VARCHAR(64) NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order  INT NOT NULL DEFAULT 0,
    created_at  DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at  DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
                ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uk_categories_code (code),
    KEY idx_categories_enabled_sort (enabled, sort_order)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

分类代码必须匹配 `^[a-z0-9][a-z0-9_-]{0,31}$`。不限制为单字符，不自动转换大小写或去除空格。分类名为 1–64 个 Unicode 码点，不能全为空白，UTF-8 编码最多 256 字节。分类排序为 `sort_order ASC, code ASC`，后者按代码字节序比较。

### 6.2 sentences

```sql
CREATE TABLE sentences (
    id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    uuid          CHAR(36) COLLATE utf8mb4_bin NOT NULL,
    category_id   BIGINT UNSIGNED NOT NULL,
    content       TEXT NOT NULL,
    source        VARCHAR(255) NULL,
    author        VARCHAR(128) NULL,
    length        SMALLINT UNSIGNED NOT NULL,
    status        TINYINT UNSIGNED NOT NULL DEFAULT 0,
    created_at    DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at    DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
                  ON UPDATE CURRENT_TIMESTAMP(6),
    published_at  DATETIME(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_sentences_uuid (uuid),
    KEY idx_sentences_publish_query
        (status, category_id, length, id),
    CONSTRAINT fk_sentences_category
        FOREIGN KEY (category_id) REFERENCES categories(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

状态定义：`0` 待处理，`1` 已发布，`2` 已拒绝，`3` 已停用。

发布数据必须满足：

- `content` 是有效 UTF-8，非空且不能全为空白，不自动 trim 或执行 Unicode 归一化。仅遵守现有 `TEXT` 字段的 65535 字节容量和 `length` 字段的表示范围，不额外设置基于内存预算的单条内容上限。字段容量依据 [MySQL 字符串类型说明](https://dev.mysql.com/doc/refman/8.4/en/string-type-syntax.html)。
- `length` 由 Go 使用 `utf8.RuneCountInString` 计算，包含空格、标点和换行；不是字节数，也不是视觉字素数量。不得接受调用方提交的长度。
- `source` 最多 255 个码点、1020 字节；`author` 最多 128 个码点、512 字节。
- UUID 必须采用标准 36 字符带连字符表示。接口和导入可接受大小写十六进制，解析后统一为小写；不接受无连字符、URN 或花括号形式。
- 数据库 UUID 必须以小写标准形式存储，规范化后不得重复。除格式外不额外限定 UUID 版本。
- 状态为 `1` 的记录必须有 `published_at`，且长度与实际内容一致。
- `source`、`author` 的数据库 `NULL` 和空字符串对读取 API 等价，均输出空字符串。

快照仅包含 `status = 1` 且所属分类 `enabled = TRUE` 的语句。禁用分类下的语句不会通过 UUID 接口暴露。

随机接口的 0–1000 长度参数范围只约束查询，不是入库或快照加载限制。超过 1000 个码点且满足数据库字段约束的语句可以导入、加载、计入分类数量并通过 UUID 查询。

### 6.3 dataset_versions

```sql
CREATE TABLE dataset_versions (
    id            TINYINT UNSIGNED NOT NULL,
    version       BIGINT UNSIGNED NOT NULL DEFAULT 1,
    published_at  DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO dataset_versions (id, version) VALUES (1, 1);
```

服务使用 `id = 1` 这一条记录；记录缺失或 `version = 0` 时，快照加载失败。

所有影响读取结果的修改，包括发布、撤回、修改内容或来源、删除语句、创建或启停分类、修改分类名及顺序，使用同一个简单写入协议。先开始事务，在修改业务表之前取得版本行锁：

```sql
SELECT version FROM dataset_versions WHERE id = 1 FOR UPDATE;
```

这使低频批量写入按事务串行执行，便于处理 UUID 冲突及版本更新；公开请求和快照的一致性读取不取得此锁。完成有实际变化的业务写入后，在同一事务末尾执行：

```sql
UPDATE dataset_versions
SET version = version + 1,
    published_at = CURRENT_TIMESTAMP(6)
WHERE id = 1;
```

必须检查受影响行数为 1。版本溢出或版本更新失败时回滚整个事务。一次有实际变更的事务只递增一次版本；完全没有变化的导入不递增版本。

禁止依赖 `updated_at` 推断全库版本。所有外部写入程序必须遵守该事务协议；直接修改表而不更新版本不受支持。

### 6.4 一致性读取和完整校验

加载事务必须显式使用只读 `REPEATABLE READ`，不依赖服务器默认隔离级别。事务内依次读取版本、启用分类、已发布且所属分类启用的语句，使用一致性读取，不混用事务外查询或锁定读取。

对读取结果逐条验证第 6 节数据格式、数据库字段容量、分类引用和唯一性约束。校验失败时整次加载失败，不允许静默跳过损坏记录。数据库 `length` 与计算结果不符也必须失败。记录数和文本总量仅用于观测，不作为拒绝加载的条件。

只验证进入快照的数据；未发布语句和禁用分类下语句不进入快照。

### 6.5 迁移与首次部署

同一二进制提供：

```text
sentence-api                    # 启动服务
sentence-api migrate up         # 执行尚未应用的迁移
sentence-api migrate down --steps 1
sentence-api import --file sentences.json
```

- 服务启动不自动执行 DDL。部署流程先独立运行迁移命令。
- 迁移通过 `golang-migrate/migrate` 执行。SQL 文件嵌入程序，并作为可检查文件随发布包及镜像发布；两者必须来自同一份构建输入。
- `down` 必须显式指定正数步数；不提供默认回退全部迁移的行为。执行前由部署方完成适当备份并检查具体 SQL。
- 空数据库首次部署顺序为：迁移 → 导入包含分类和语句的数据文件 → 启动服务。分类也可由外部管理程序按版本协议提前创建。
- API 账号使用读取权限；导入账号具备必要的读取、写入权限；迁移账号具备必要的 DDL 权限。每次命令通过 `MYSQL_DSN` 注入相应账号，不要求服务账号拥有 DDL 权限。
- 首个 schema 迁移编号为 1。当前随包迁移最高为 4。读 API 与 import 接受版本 1、2、3 或 4 且非 dirty；`web` 只接受版本 4。启动时检查该范围，不建立通用多版本兼容框架。未知更高版本或 dirty 直接失败。
- `migrate up` 必须允许从空库或已存在的较早迁移版本按随包 SQL 升级，不能套用服务启动时的版本检查而阻止首次建库。未知迁移版本或 dirty 状态直接失败。
- MySQL DDL 不承诺整个迁移批次事务回滚。中断或失败可能留下部分结构和 dirty 状态；部署方按运行文档检查实际结构、修复后使用官方迁移工具纠正版本，不自动 force 或重试破坏性 DDL。

## 7. 内存快照

概念模型：

```go
type Sentence struct {
    ID       uint64
    UUID     string
    Content  string
    Category string
    Source   string
    Author   string
    Length   uint16
}

type Category struct {
    Code      string
    Name      string
    SortOrder int32
    Count     uint64
}

type Snapshot struct {
    Version    uint64
    LoadedAt   time.Time
    ByUUID     map[string]*Sentence
    ByCategory map[string][]*Sentence
    Categories []Category
}
```

这些类型是概念模型。HTTP 字段可以通过 JSON tag 或小型响应结构表达，不要求为每一层复制一份模型；对外 ID、版本的字符串序列化及内部字段隐藏必须满足第 9 节。

### 7.1 索引与随机算法

- 所有启用分类均保留，包括当前语句数为 0 的分类。
- `ByCategory` 切片按 `Length ASC, ID ASC` 排序。
- 使用 `sort.Search` 分别寻找首个长度 `>= min_length` 和首个长度 `> max_length` 的位置，区间两端均包含。
- 多分类查询先计算各分类匹配数量，在总数量 `[0, total)` 上生成一个均匀随机偏移，再定位到相应分类和语句。
- 对重复分类只计算一次权重，不能先等概率选择分类。
- 随机数使用并发安全的实现，例如标准库 `math/rand/v2` 的包级函数；共享独立随机源时必须自行保证并发安全。无需密码学随机数。
- 没有匹配时返回无结果，不回退到其他分类或长度。
- 不得为请求复制完整候选列表；临时空间只能与参与查询的分类数有关。未指定分类时遍历已有分类索引，不创建整库候选数组。

### 7.2 发布与内存使用

- 使用 `atomic.Pointer[Snapshot]` 保存当前快照。
- 快照发布后完全只读，不修改底层 map、slice、分类计数或 Sentence 对象，不复用仍可能被旧快照引用的可变缓冲区。
- 构建必须流式扫描数据库记录并逐条校验，不先保留另一个全量中间数据集，避免不必要的数据复制。
- 不设置语句总数、启用分类总数、文本总量或估算内存的应用级准入上限，也不因超过内存参考值截断数据、拒绝启动或拒绝刷新。
- 供日志和指标使用的文本总量按 UTF-8 字节累加：每个分类的 `code`、`name`，以及每条语句的 `uuid`、`content`、`category`、`source`、`author`。相同文本在不同记录中分别计数，NULL 按空字符串计数。这些统计不控制快照发布。
- 全量只读快照的内存会随句库规模增长；按实际数据和刷新期间的峰值配置资源。第 16 节记录 Go 堆、RSS 和容器内存，超出参考值时评估实现及部署配置，不减少可加载的数据范围。
- 加载发生查询、超时或数据校验错误时仍保留旧快照；首个快照无法构建时退出。这些错误处理与内存参考值无关。
- 连续刷新和慢请求的内存情况纳入压力测试；使用 GC 回收，不实现快照引用计数、手动释放或快照对象池。

## 8. 快照刷新

### 8.1 启动流程

1. 解析并验证配置。
2. 建立外部 MySQL 连接池，执行有超时的 `PingContext`。
3. 检查 schema 状态是否受当前程序支持。
4. 在同一个一致性事务中加载数据，构建首个快照。
5. 验证至少存在一个启用分类和一条可返回语句，并满足全部数据正确性约束。
6. 发布快照后开始监听 HTTP，开放 readiness。
7. 启动后台版本检查任务。

首个快照无法构建时进程非零退出，不允许以空数据提供服务，也不隐式导入测试数据。

### 8.2 定时刷新

- 默认每 60 秒读取一次 `dataset_versions.version`。
- 与当前快照版本相等时不读取全部数据；不同即触发刷新，包括小于当前版本的情形。较低版本必须记录结构化警告，以支持识别外部数据库恢复。
- 每个轮询任务串行运行，不因慢查询积压 goroutine。刷新在后台执行，不占用请求 goroutine。
- 同一时间只允许一个快照构建任务，定时与手动刷新共用执行权。忙时定时任务跳过本轮，不排队。
- 版本查询、初始加载和每次刷新均有明确超时。单次加载的总期限由 `SNAPSHOT_LOAD_TIMEOUT` 控制，覆盖读取、索引和校验；构建期间定期检查取消或超时状态。
- 轮询版本仅用于触发；最终发布加载事务内的版本。取得刷新执行权后可以再次检查版本并取消已无必要的自动重建。
- 服务进入退出流程后不得启动新任务，也不得发布取消后的构建结果。
- 失败或取消保留旧快照。正常退出引起的取消不记为刷新失败，计入取消结果。
- 成功切换后记录版本、语句数、分类数、耗时、文本字节数及基于当前结构的粗略内存估算。估算只用于诊断，不承诺精确 RSS，也不控制是否发布。失败记录错误类别，不记录 DSN、令牌或语句全文。

### 8.3 手动刷新

```http
POST /internal/reload
Authorization: Bearer <RELOAD_TOKEN>
```

- 未配置 `RELOAD_TOKEN` 时不注册路由，请求返回统一的 `404`。
- 该路由只接受 `POST`，不允许查询参数和请求体，不开放 CORS。
- 配置令牌必须为 32–256 字节可打印 ASCII，不能包含空白；建议使用密码学随机生成的至少 32 字节随机值的安全文本编码。
- 校验 Bearer 凭证时，对候选令牌和配置令牌取固定长度摘要，使用常量时间比较，不直接使用普通字符串相等判断。缺失、格式错误或不匹配统一返回 `401`，并附带 `WWW-Authenticate: Bearer`。
- 先鉴权，再检查执行权；已有构建任务时返回 `409`。
- 成功取得执行权并提交后台任务后立即返回 `202 Accepted`；不等待数据库加载完成。
- 手动刷新强制重建，即使版本没有变化。
- 任务使用服务后台生命周期上下文，不使用 HTTP 请求上下文；客户端断开不取消已经接受的任务。
- `202` 表示已接受，不代表成功切换。通过日志中的触发请求 ID 和刷新指标判断结果；首版不新增任务查询接口。

示例：

```json
{
  "data": {"status": "accepted"},
  "meta": {
    "dataset_version": "1289",
    "request_id": "4eb245ba-2340-420a-8e8b-6f6034c1dcde"
  }
}
```

这里的版本是接受请求时的当前版本。部署方必须通过内网隔离或 Nginx 访问控制进一步保护该路由。

## 9. HTTP API

业务接口统一前缀为 `/api/v1`。成功 JSON 使用 `application/json; charset=utf-8`，所有文本使用 UTF-8。

### 9.1 通用约定

- 对外 `dataset_version` 使用十进制字符串，避免 JavaScript 整数精度损失；内部数据库 `id` 不通过语句接口公开。`length`、分类 `count` 和错误 `status` 使用 JSON 数字。
- 所有服务可控响应包含 `X-Request-ID`。
- 公开读取路由允许 `GET`、`HEAD`、`OPTIONS`。`HEAD` 执行与 `GET` 相同的校验与状态判断，返回相应响应头，不返回响应体。
- `OPTIONS` 不执行随机查询，合法预检及普通能力查询返回 `204`；方法头和 CORS 行为见第 11 节。业务查询参数校验适用于 `GET`、`HEAD`。
- 业务接口不接受请求体；已携带实体的公开读取请求返回 `400`。
- 已知路径上不支持的方法返回 `405`，包含准确的 `Allow`；未知路径返回 `404`。
- 拒绝畸形查询编码、重复参数名、未知参数名和超限查询。随机接口原始查询字符串最多 4096 字节，超限返回 `400`。
- UUID、分类、健康、版本、指标接口不接受查询参数。
- 根路径 `/` 不提供兼容接口。
- 随机、分类、UUID 及控制接口返回 `Cache-Control: no-store`，避免中间缓存导致随机性或版本可见性异常。

### 9.2 随机语句

```http
GET /api/v1
```

| 参数 | 类型 | 默认值 | 限制 |
| --- | --- | --- | --- |
| `categories` | 逗号分隔字符串 | 全部启用分类 | 去重后最多 20 个 |
| `min_length` | 十进制整数 | `0` | 0–1000 |
| `max_length` | 十进制整数 | `30` | 0–1000，必须大于等于最小值 |

分类规则：

- 参数省略表示全部启用分类；显式空字符串、空元素、连续逗号、空白及非法代码返回 `400`。
- 按第 6.1 节语法校验，不 trim，不忽略大小写。
- 先去重，再检查 20 个分类上限；解析工作量由统一的 4096 字节查询长度限制约束，不再增加另一套原始项数限制。
- 20 个分类仅是单次显式筛选条件的接口限制，不限制句库分类总数；省略 `categories` 时查询全部启用分类。
- 未知分类或停用分类返回 `400`。存在但当前无语句的启用分类属于合法分类。
- 查询中任一分类非法即拒绝整次请求，不忽略非法分类。

长度规则：

- 只接受 `[0-9]+` 形式，允许前导零；不接受空字符串、符号、空白、小数、科学计数法和溢出整数。
- `0` 和 `1000` 均为合法边界；`min_length = max_length` 表示精确长度。
- 默认值独立应用。例如仅传 `min_length=50` 时，`max_length` 仍为 30，因此返回 `400`。
- 有效数据不含零长度语句，因此 `[0,0]` 是合法查询，但结果为 `404`。

成功示例：

```json
{
  "data": {
    "uuid": "75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2",
    "content": "今天也要认真写代码。",
    "category": "f",
    "source": "项目自编示例",
    "author": "",
    "length": 10
  },
  "meta": {"dataset_version": "1289"}
}
```

没有符合条件的数据时返回 `404`，不得回退到其他长度或分类。

### 9.3 根据 UUID 查询

```http
GET /api/v1/sentences/{uuid}
```

- 按第 6.2 节 UUID 规则校验并规范化。
- 格式错误返回 `400`。
- 不存在、未发布或所属分类未启用返回 `404`。
- 只查询当前快照的 `ByUUID`，不访问数据库。
- 成功响应与随机接口使用相同的数据结构及版本元数据。

### 9.4 分类列表

```http
GET /api/v1/categories
```

返回所有启用分类，包括零语句分类，按 `sort_order ASC, code ASC` 排列。

```json
{
  "data": [
    {"code": "f", "name": "原创", "count": 120},
    {"code": "poetry", "name": "诗歌", "count": 0}
  ],
  "meta": {"dataset_version": "1289"}
}
```

`count` 为当前快照该分类的全部语句数，不受随机接口默认长度 30 的影响。

### 9.5 健康检查

```http
GET /healthz
```

只确认进程和 HTTP 循环正常，不检查 MySQL。成功返回 `200`：

```json
{"status":"ok"}
```

```http
GET /readyz
```

存在有效快照且未进入退出流程时返回 `200`：

```json
{"status":"ready","dataset_version":"1289"}
```

MySQL 短暂中断但旧快照有效时仍保持 ready，通过指标报告数据库异常。没有快照或已经开始退出时返回 `503` 问题响应。退出是“有效快照即可 ready”的明确例外。

### 9.6 指标

```http
GET /metrics
```

返回 Prometheus 文本格式。至少包含第 17 节定义的请求、快照、刷新、数据库和 Go 运行时指标。

指标不是业务 JSON。部署方应限制抓取来源，不在公网无限制暴露。

### 9.7 版本信息

```http
GET /version
```

```json
{
  "version": "1.0.0",
  "git_commit": "0123456789abcdef",
  "build_time": "2026-09-18T00:00:00Z"
}
```

仅包含程序版本、Git 提交和构建时间，不包含主机名及环境变量。本地未注入构建信息时可使用 `dev`、`unknown`；发布镜像不得缺失这些信息。程序版本与开发规格版本分别管理。

## 10. 错误格式

服务可控错误统一使用 `application/problem+json`，包括参数错误、未找到、错误方法、鉴权失败、刷新冲突、未就绪和可恢复 panic。

```json
{
  "type": "about:blank",
  "title": "参数无效",
  "status": 400,
  "detail": "max_length 必须大于等于 min_length",
  "request_id": "4eb245ba-2340-420a-8e8b-6f6034c1dcde",
  "code": "invalid-parameter"
}
```

首版使用 `about:blank`，增加稳定的 `code` 扩展字段供客户端识别；不使用不受项目控制的示例域名作为正式错误类型。

| HTTP 状态 | code | 场景 |
| --- | --- | --- |
| 400 | `invalid-parameter` | 参数、UUID、查询编码或不支持的请求实体 |
| 401 | `unauthorized` | 手动刷新凭证无效 |
| 403 | `cors-denied` | 明确被拒绝的 CORS 预检 |
| 404 | `not-found` | 未知路径、缺失语句或没有匹配数据 |
| 405 | `method-not-allowed` | 已知路径上方法不受支持 |
| 409 | `reload-in-progress` | 已有快照构建任务 |
| 500 | `internal-error` | 可恢复内部错误 |
| 503 | `not-ready` | 无有效快照或服务正在退出 |

要求：

- 客户端错误描述不得包含数据库原始错误、堆栈、DSN、内部路径、令牌或未经处理的输入全文。
- `HEAD` 错误只返回相应头和状态，不返回问题响应体。
- 在提交响应头前完成业务 JSON 序列化，尽量保证 panic 或编码错误能统一处理。
- 响应已经提交后发生错误，不得再拼接第二个 JSON 或声称可以改写状态码；记录内部错误并终止响应。
- `net/http` 在中间件执行前拒绝的畸形报文、超大请求头以及连接级超时，不承诺统一 JSON 格式和请求 ID。不得为统一格式自行重写 HTTP 协议栈。

## 11. 中间件与安全

中间件顺序：

1. Request ID。
2. 真实客户端 IP 解析。
3. 访问日志与请求指标。
4. Panic recovery。
5. 安全响应头。
6. CORS。
7. 路由。

### 11.1 HTTP 服务限制

- `ReadHeaderTimeout`：5 秒。
- `ReadTimeout`：10 秒。
- `WriteTimeout`：10 秒。
- `IdleTimeout`：60 秒。
- `MaxHeaderBytes`：1 MiB。
- 服务生成 UUID 请求 ID，忽略客户端传入的 `X-Request-ID`，在响应中返回新 ID。
- 至少设置 `X-Content-Type-Options: nosniff` 和 `X-Frame-Options: DENY`。HTTPS 专属策略由外部 TLS 终止层配置。
- 禁止 JSONP、动态 callback 和可执行 JavaScript。
- 公开读取路由仅允许 `GET`、`HEAD`、`OPTIONS`；内部 reload 仅允许 `POST`。
- 限流优先由外部 Nginx 完成。首版不要求 Go 内置限流；后续可增加单实例令牌桶，不得因此引入 Redis。

### 11.2 可信代理解析

- 默认使用 TCP 直接连接方地址。
- 仅当直接连接方属于 `TRUSTED_PROXY_CIDRS` 时解析 `X-Forwarded-For`。
- 首版仅支持 `X-Forwarded-For`，忽略 `Forwarded` 和 `X-Real-IP`，避免来源优先级歧义。
- 从直接连接方开始，自右向左检查完整代理链，剔除可信代理，取首个不可信地址为客户端 IP；全部可信时取链中最左地址。
- 头内只接受裸 IPv4 或 IPv6 地址，不接受端口、主机名、`unknown` 或带引号值；多行头按顺序拼接处理。
- 最多解析 32 个转发地址。格式错误、空元素或超限时回退为直接连接方，不采用部分解析结果。
- IPv4 映射 IPv6 地址先规范化再匹配 CIDR。
- 部署文档必须提示：边缘代理应覆盖或正确追加该头，可信网段只能包含实际受控代理。

### 11.3 CORS

- 默认允许所有网站访问公开 API（等价于 `*`），无需配置白名单；CORS 不作为服务端鉴权措施，且不携带 credentials。
- `CORS_ALLOWED_ORIGINS` 可选，用于把公开 API 限制为明确的 HTTP/HTTPS origin 列表；不能包含路径、查询、片段、用户信息、`null` 或显式 `*`。未设置或为空时默认返回 `*`；显式白名单时仅匹配来源返回该 origin。
- 只对 `/api/v1`、`/api/v1/categories` 和 `/api/v1/sentences/{uuid}` 路由启用 CORS，内部 reload、健康、版本和指标接口不开放 CORS。
- 预检仅允许 `GET`、`HEAD` 和请求头 `Accept`、`Content-Type`，头名比较不区分大小写。不启用 credentials。
- 来源、请求方法或请求头不被允许的预检返回 `403`；预检只接受 `GET`、`HEAD` 以及 `Accept`、`Content-Type` 请求头。
- 普通、不带 CORS 预检头的 `OPTIONS` 返回 `204` 和 `Allow`。
- 正确设置 `Vary: Origin`；预检额外包含 `Access-Control-Request-Method` 和 `Access-Control-Request-Headers`，不能覆盖已有的 `Vary` 值。
- `Access-Control-Expose-Headers` 包含 `X-Request-ID`，使允许的前端可以读取请求 ID。

### 11.4 日志与凭证

- 管理令牌只能通过环境变量注入，不提供命令行令牌参数或持久化配置文件。
- 不记录完整 DSN、令牌、密码、Authorization、Cookie、完整查询字符串、语句内容或请求体。
- 配置、驱动和数据库错误在日志中使用经过审查的错误类别及必要错误码，不直接输出可能包含凭证的原始错误字符串。
- 不向客户端返回 panic 内容或堆栈；内部诊断日志也必须遵守凭证脱敏规则。

## 12. 配置

所有可部署配置通过环境变量注入；导入文件路径、dry-run、迁移方向和步数属于命令操作参数。

| 环境变量 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `HTTP_ADDR` | 否 | `:8080` | HTTP 监听地址 |
| `MYSQL_DSN` | 是 | 无 | 外部 MySQL DSN，各命令可注入不同账号 |
| `MYSQL_MAX_OPEN_CONNS` | 否 | `10` | 最大连接数，必须为正数 |
| `MYSQL_MAX_IDLE_CONNS` | 否 | `5` | 空闲连接数，0 至最大连接数 |
| `MYSQL_CONN_MAX_LIFETIME` | 否 | `30m` | 正数连接生命周期 |
| `SNAPSHOT_POLL_INTERVAL` | 否 | `60s` | 正数版本检查间隔 |
| `SNAPSHOT_LOAD_TIMEOUT` | 否 | `30s` | 单次检查或加载的期限 |
| `IMPORT_TIMEOUT` | 否 | `120s` | 导入校验与数据库操作总期限 |
| `RELOAD_TOKEN` | 否 | 空 | 配置后注册手动刷新路由 |
| `CORS_ALLOWED_ORIGINS` | 否 | 空（允许所有公开 API origin） | 逗号分隔的明确 origin 列表；设置后限制公开 API 来源 |
| `TRUSTED_PROXY_CIDRS` | 否 | 空 | 逗号分隔的可信代理网段 |
| `LOG_LEVEL` | 否 | `info` | `debug`、`info`、`warn`、`error` |
| `SHUTDOWN_TIMEOUT` | 否 | `10s` | 正数退出总时限 |

配置校验规则：

- 必填项缺失、格式错误、整数溢出、非法范围、非正期限必须在执行相关命令前报错，不能静默回退默认值。
- 默认值只用于未设置的变量；可选列表和令牌允许显式空值，其他数值或期限不接受空字符串。
- 列表项允许去除外围 ASCII 空白，去重；非空列表中的空元素视为错误。该规则不适用于 HTTP 分类参数。
- 按命令验证相关配置；执行 import 或 migrate 不要求配置 reload 令牌，也不应被无关 HTTP 配置阻止。
- DSN 必须包含数据库名。通过驱动配置结构规范化 `parseTime=true`、时间位置 UTC、会话时区 `+00:00` 和 `utf8mb4` 字符集，不能仅依赖部署方记住这些参数。
- DSN 未指定时，连接建立超时为 5 秒，读写超时各为 30 秒；DSN 中的覆盖值必须为正数。服务查询和导入另用上下文控制总期限，不动态重建连接池以匹配每个请求的剩余时间。
- 迁移使用驱动网络超时和迁移库的有限锁等待超时，不包装额外的命令总计时器；任何中断都按第 6.5 节检查实际迁移状态。服务及导入连接禁用多语句执行，迁移按迁移驱动需要单独配置。
- 生产跨主机连接建议使用经过验证的 TLS，不关闭证书校验。
- Go 运行时内存设置及容器资源由部署方按实际规模配置，并记录在性能报告中；程序不根据这些设置拒绝数据或缩小快照。
- 不提供基于内存预算的数据量或导入文件大小限制配置。加载及导入超时仍用于操作生命周期控制，可按数据规模调整。

DSN 示例只能使用占位内容：

```text
api_user:change_me@tcp(mysql.example.internal:3306)/sentence_api?charset=utf8mb4&parseTime=true&loc=UTC&tls=true
```

不得将真实账号密码或真实 DSN 提交到仓库。

## 13. Docker 镜像与发布

只交付 API 镜像，不编写 MySQL、Nginx 或 Compose 配置。

Dockerfile 要求：

- 多阶段构建，构建阶段固定 Go 1.27 系列，并在发布记录中保留确切补丁版本及基础镜像摘要。
- `CGO_ENABLED=0`。
- 通过构建参数注入程序版本、完整提交哈希和 UTC 构建时间。
- 运行阶段使用最小化基础镜像，例如 `scratch`。
- 最终应用层只复制二进制、CA 证书和迁移文件，不包含源码、Git 目录、工具链和测试数据。
- 使用明确的非 root 数字 UID/GID，不依赖运行阶段 shell。
- 使用 exec 形式 `ENTRYPOINT` 直接运行二进制；允许通过参数运行 import 或 migrate。
- 暴露 8080 端口。服务运行不依赖可写根文件系统。
- `.dockerignore` 排除本地凭证文件、Git 元数据、测试产物和非构建必需数据。

镜像标签必须不可变，例如：

```text
sentence-api:1.0.0
sentence-api:git-<short-sha>
```

生产不得只引用 `latest`。发布记录保存镜像 digest；CI 发布必须来自受保护的版本标签，并在必需检查及外部 MySQL 集成测试通过后执行。未配置镜像仓库凭证时，只能完成构建验证，不能记为已发布。

## 14. 启停行为与回滚

### 14.1 优雅退出

收到 `SIGTERM` 或 `SIGINT` 后：

1. 原子标记进入退出状态，readiness 变为失败，禁止启动新刷新任务。
2. 停止接收新连接和新请求，同时取消后台轮询及刷新任务。
3. 等待进行中的请求完成，并等待后台任务真正退出。
4. 关闭数据库连接池及其他资源。
5. 在一个共同的 `SHUTDOWN_TIMEOUT` 期限内退出。

不得为每个退出步骤重新启动完整超时时间。不要在正常排空开始时无差别取消全部在途 HTTP 请求上下文。服务正常排空且资源关闭成功时返回 0；启动失败、异常监听退出或退出超时返回非零。期限到达后强制关闭剩余连接并终止进程，不再无限等待。

### 14.2 回滚

- 应用回滚：部署方将流量切回已验证的不可变镜像 digest；保留前一版本镜像及对应配置说明。
- 数据库结构回滚：独立操作，必须检查迁移是否可逆及是否会丢失数据，不随应用回滚自动执行 `down`。
- 数据回滚：由外部管理程序或数据库恢复流程完成。在线逻辑回滚应写入补偿变更并递增版本；数据库整体恢复可能带来较低版本，服务按“版本不同即刷新”处理。
- 恢复后即使版本号碰巧与内存相同但数据不同，也不会被版本轮询自动识别；部署方必须重新递增版本、手动强制刷新或重启服务。
- 回滚镜像必须支持恢复后的 schema。没有 schema 变更时直接回滚镜像；涉及 schema 变更时，发布说明逐次写清兼容性和必要操作，不承诺任意两个历史版本都能互相回滚。
- 回滚验收包括 readiness、版本、数据范围及流量恢复，不只验证进程存活。

## 15. 测试要求

### 15.1 单元测试

必须覆盖：

- 分类省略、空值、去重、20 项限制、非法代码、未知和停用分类。
- 长度 0、精确区间、1000、负数、正号、空值、非数字、溢出及默认值交互。
- 重复参数、未知参数、畸形查询编码、查询超限和禁止的兼容参数。
- 多分类随机选择没有分类偏置；用可注入随机偏移逐一验证各候选边界，不能只依赖概率性统计断言。
- UUID 格式、规范化、命中、缺失及未发布/禁用分类数据不可见。
- 空快照、合法零语句分类和无匹配结果。
- 数据长度不符、非法 UTF-8、重复 UUID 和数据库字段容量校验。
- 多批次导入及大数据集加载不截断记录，计数与数据源一致；超过随机接口长度范围的合法记录仍能通过 UUID 查询。
- 原子快照切换、请求数据与响应版本一致。
- 刷新失败、超时及取消后继续使用旧快照。
- 轮询版本未变不全量加载，较低版本仍触发重建。
- 定时与手动刷新竞争时最多一个构建任务，无刷新任务队列积压。
- reload 未配置不注册、令牌验证、`202`、`409` 和异步生命周期。
- Panic recovery 不泄露内部错误，已提交响应不重复写问题 JSON。
- HEAD、OPTIONS、405、统一错误格式、安全头、CORS 与可信代理链。
- 请求指标标签不包含任意路径、UUID、客户端 IP 或查询值。
- 导入去重、冲突、dry-run、重复 JSON 成员、非法 Unicode 转义、全量事务回滚、提交结果不确定及无变化导入行为。
- 退出期间取消后台任务、排空请求和共同期限行为。

### 15.2 Fuzz 测试

至少 fuzz：

- 查询参数解析。
- UUID 路径解析。
- 分类字符串解析。
- 错误响应序列化。

各 fuzz 目标按职责验证“不 panic”、合法结果的范围及错误响应的 JSON 有效性和无内部信息泄露。HEAD 等 HTTP 行为通过 handler 测试验证。常规 `go test` 执行种子语料；CI 的每个 fuzz 目标独立运行至少 30 秒，较长任务可安排为定期检查。

### 15.3 外部 MySQL 集成测试

通过 `MYSQL_TEST_DSN` 连接部署方提供的专用测试数据库；当前部署验收使用 MariaDB 11.8，MySQL 8.4 兼容目标需另行验证。集成测试套件不启动数据库容器。CI `integration` job 使用外部 DSN secret，不定义数据库 service；`image` job 另启 MariaDB 11.8 只做生产镜像 smoke，不能替代外部集成门禁。

测试账号必须有创建和删除测试数据库及执行迁移的权限。每个测试套件实例必须：

1. 生成固定前缀加随机标识的独立数据库名，禁止复用 DSN 中现有业务数据库。
2. 创建独立测试数据库，明确使用 `utf8mb4`。
3. 执行实际发布的迁移。
4. 导入项目自编的固定夹具。
5. 运行一致性加载、快照、HTTP、导入及迁移行为测试。
6. 使用独立清理上下文删除本次创建的数据库，不能复用已取消的测试上下文。

测试必须覆盖并发发布时版本与数据一致、导入中途失败无部分写入、禁用分类、版本记录缺失、刷新失败和恢复。清理函数只能删除本次登记创建的数据库；异常中断留下的数据由测试环境维护流程按同一受控前缀清理。

未设置 `MYSQL_TEST_DSN` 时明确输出 skip 原因，单元测试继续运行；变量已设置但连接失败、权限不足或测试失败时必须失败，不得降级为 skip。

发布验收必须实际运行并通过外部 MySQL 集成测试。“因未配置而跳过”不算通过。受信任的发布任务从 CI 密钥获取 DSN；不向不可信 PR 暴露密钥。

### 15.4 竞态、静态检查与 CI

Go 当前及上一稳定版本的 CI 均执行：

```bash
go test -race ./...
go test ./...
go vet ./...
```

另外检查格式、fuzz、构建和镜像。竞态检查在具备 C 工具链的测试环境执行；生产二进制保持 `CGO_ENABLED=0`。发布任务必须把外部 MySQL 集成测试作为必需检查，并校验最终镜像以非 root 身份运行。

## 16. 性能验收与内存指导

### 16.1 验收环境

- API 使用发布构建，基准 CPU 配额为 1 vCPU，不启用 race 检测；压测客户端不占用 API 的 CPU 配额。容器内存可从 256 MiB 起步，按实际数据规模配置并记录，不固定为硬性测试前提。
- 从外部 MySQL 加载约 12000 条自编测试语句，固定生成种子，保留夹具或生成脚本。
- 数据覆盖大小不等的分类、默认长度范围及超过 30 的内容、中文和四字节 UTF-8 字符。具体分类数和长度比例在性能报告中固定，不把某一组比例写成产品契约。
- 使用现有负载工具及少量脚本，保持 HTTP 连接复用。不开发专用压测框架。

### 16.2 必需场景与方法

- 正常流量：预热后持续至少 10 分钟，以随机接口为主，覆盖默认、多分类和长度过滤查询。
- 刷新流量：在持续负载中多次提交实际数据变化并触发刷新。
- 数据库故障：在持续负载中断开数据库至少两个轮询周期，再恢复连接，验证旧快照服务和后续刷新。
- 容量观察：补测连续手动刷新、慢请求和更大规模数据集，记录数据是否完整加载及峰值内存，用于部署资源规划。
- 建议预热 60 秒、初始并发 32；具体并发和请求比例可以调整，但必须记录并保持同一对比场景可复现。
- 负载工具应采用固定到达率或等效方法，记录实际完成 RPS、排队和未能按计划发起的请求；不得通过降低发送率或丢弃失败样本掩盖过载。

### 16.3 通过标准

- 持续实际完成至少 1000 RPS 时，随机接口 P95 低于 10 毫秒，不含 Nginx 和公网延迟。
- 有匹配结果的有效请求无 5xx 或请求超时。刷新及数据库故障阶段单独统计，P95 仍低于 10 毫秒；不能用正常阶段平均值掩盖刷新停顿。控制接口的预期 `409` 单独统计。
- 固定分类数、改变语句总数执行 `-benchmem`，确认请求路径临时分配不随句库规模线性增长。

### 16.4 内存指导与记录

- 约 1.2 万条数据时，稳定内存 128 MiB、刷新峰值 220 MiB、容器配置 256 MiB 仅作为容量规划和优化参考，超出这些数值本身不判定验收失败。
- 使用现有监控或 cgroup 统计记录容器实际内存、高水位和进程 RSS，并说明采样间隔、峰值来源及实际数据规模；Go 堆可作为辅助观测。
- 数据量增长时优先减少不必要的复制、优化实现并调整部署资源，不通过限制语句数、分类数、文本总量或导入文件大小满足参考值。
- 保留完整快照和已有查询语义；不为满足参考值引入截断加载、自动丢弃数据或额外存储系统。

`docs/performance.md` 记录 Go 版本、程序提交或镜像 digest、操作系统、CPU 型号及配额、内存限制、Go 运行时环境变量、MySQL 版本、数据规模和字节分布、请求组成、并发数、持续时间、RPS、P95、错误率、内存峰值及刷新耗时。未实际测量的结果标为“待验收”，microbenchmark 只作为补充。

## 17. 可观测性

### 17.1 访问日志

每条访问日志至少包含：

- `time`：UTC 时间。
- `level`、`msg`：固定日志等级和事件名称。
- `request_id`。
- `method`：HTTP 方法。
- `route`：路由模板，例如 `/api/v1/sentences/{uuid}`。
- `status`。
- `duration_ms`。
- `response_bytes`：实际发送的响应体字节数，HEAD 为 0。
- `client_ip`：可信代理规则处理后的地址。

未匹配路由使用固定 `unmatched`，不得用原始路径代替模板。日志必须可解析，禁止拼接不可解析文本及完整查询内容。业务访问日志在错误恢复后也必须记录正确状态与字节数。

### 17.2 指标

至少提供：

| 指标 | 类型 | 含义 |
| --- | --- | --- |
| `sentence_api_http_requests_total` | Counter | 按 method、route、status 统计请求 |
| `sentence_api_http_request_duration_seconds` | Histogram | 按 method、route 统计时延，包含 0.01 秒边界 |
| `sentence_api_snapshot_version` | Gauge | 当前版本的数值观测值 |
| `sentence_api_snapshot_sentences` | Gauge | 当前语句数 |
| `sentence_api_snapshot_categories` | Gauge | 当前启用分类数 |
| `sentence_api_snapshot_loaded_timestamp_seconds` | Gauge | 当前快照加载完成时间 |
| `sentence_api_snapshot_text_bytes` | Gauge | 第 7.2 节定义的当前文本总量 |
| `sentence_api_snapshot_refresh_total` | Counter | result 为 success、failure、canceled |
| `sentence_api_snapshot_refresh_duration_seconds` | Histogram | 刷新耗时 |
| `sentence_api_snapshot_refresh_in_progress` | Gauge | 是否正在构建 |
| `sentence_api_mysql_last_operation_success` | Gauge | 最近一次数据库操作是否成功 |
| `sentence_api_mysql_last_check_timestamp_seconds` | Gauge | 最近一次数据库探测/加载尝试完成时间 |
| `sentence_api_mysql_last_success_timestamp_seconds` | Gauge | 最近一次成功数据库操作时间 |

还必须包含 Go 运行时指标；可以增加 `database/sql` 连接池指标。

约束：

- MySQL 指标表示最近观测结果，不宣称实时在线状态。版本轮询同样更新数据库观测时间。
- 数据库读取成功但快照校验失败时，数据库状态仍为成功，刷新结果为失败。正常退出导致的上下文取消不误报数据库故障。
- 未发生数据库操作前，状态及时间戳为 0。
- 单纯版本未变的检查、忙时跳过和未授权请求不计为刷新成功或失败；实际首个快照构建计入刷新指标。
- method 标签只使用固定允许集合及 `OTHER`，route 使用有限模板及 `unmatched`，status 使用有限 HTTP 状态码。
- 不使用 UUID、请求 ID、客户端 IP、原始路径、查询值、令牌、错误文本或版本字符串作为无界标签。
- Prometheus 数值不能精确表示任意 uint64；版本 gauge 只用于观测，精确版本以 JSON 字符串和结构化日志字符串为准，不增加随版本变化的 info 标签。
- 读取 `/metrics` 不查询数据库。

## 18. 数据导入

提供独立命令：

```text
sentence-api import --file sentences.json
sentence-api import --file sentences.json --dry-run
```

完整格式、冲突规则及输出字段以 [docs/import-format.md](import-format.md) 为准，避免两处维护相同协议。实现必须满足：

- 写入前完整校验文件及其引用的分类、UUID；新增记录直接发布，长度由 Go 计算。
- 内容一致的重复 UUID 跳过，冲突整批拒绝，不提供覆盖模式或部分成功模式。
- 按第 6.3 节锁定版本行，在一个事务内完成全部新增及版本递增。可以分批 SQL，不分批提交。
- 只对本次输入及关联记录执行逐条业务校验，不执行用于内存准入的全库计数或文本总量聚合。全库逐条校验由快照加载负责，导入不重建完整快照或索引。
- dry-run 使用一致性只读事务检查同样的输入和冲突，不写入；正式执行时重新检查，不把 dry-run 当作预留。
- 无实际变更时成功返回 `changed=false`，版本不变；实际变更只递增一次。
- 使用流式解析和可配置的总期限，不设置文件大小、语句条数或分类条数上限；在写入前仍须完成整个文件的校验。输出简短 JSON 摘要，提交结果不确定时明确报错，不宣称已经回滚。

导入成功表示本次事务已提交，不代替 API 的完整快照校验。外部写入必须遵守共同版本协议；加载仍独立检查完整数据的正确性，失败保留旧快照。

第三方语句数据必须由部署者确认许可证。仓库只包含自编示例和测试夹具，不内置 AGPL 语句库，不自动下载未明确授权的数据。

## 19. 兼容策略

新 API 不承诺兼容旧一言 API，只保留字段含义相近的 JSON 结构。

明确不支持：

- `encode=js`。
- `callback`。
- `select`。
- GBK。
- 网易云扩展。
- 任意 HTTP 方法访问根路径。

这些查询参数作为未知参数返回 `400`，不静默忽略。未来确需兼容时，在独立 `/compat/v1` 适配层实现并设置废弃日期，不得污染核心服务。

相对开发规格 1.0，1.1 将对外 `id`、`dataset_version` 确定为字符串。1.2 随后停止公开内部数据库 `id`，以附录 C 的修订为当前契约。v1 API 尚未发布，不保留两套响应形式。

## 20. 开发阶段与责任

### 阶段一：MVP，参考 3–5 个工作日

- 项目骨架和配置。
- 迁移命令、数据模型与首次部署流程。
- 一致性启动加载和内存快照。
- 随机、UUID、分类、健康及版本接口。
- 基础测试、Dockerfile 和接口文档。

### 阶段二：生产化，参考 5–8 个工作日

- 后台版本检查、原子刷新和异步手动刷新。
- 指标、结构化日志、安全中间件和优雅退出。
- 完整边界、fuzz、竞态及集成测试。
- 导入工具、内存观测、性能工具与报告。
- CI、发布镜像、运行及回滚文档。

### 阶段三：上线验证，参考 3–5 个工作日

- 部署方提供外部 MySQL、访问控制、镜像仓库、测试及流量环境。
- 双方完成影子流量测试、符合新过滤规则的数据范围对比。
- 执行负载测试、数据库中断与恢复、连续刷新、退出排空及回滚演练。
- 部署方完成灰度发布和流量回切，项目方提供验证及问题修复。

参考总周期约 2–4 周，取决于环境和数据准备。时间估计不替代验收条件；外部环境未具备时应记录阻塞项，不将未运行的检查标记为通过。

## 21. 完成定义

### 21.1 代码交付完成

必须同时满足：

- 所有公开接口、内部刷新接口、错误码和严格输入规则有文档。
- 单元测试、fuzz、竞态检查和静态检查通过，CI 覆盖两代 Go 稳定版本。
- 使用外部 MariaDB 11.8 实际完成集成测试，具有执行记录；MySQL 8.4 兼容验证单独记录，不以 MariaDB 结果代替。
- Docker 镜像构建成功，并验证以非 root 用户运行。
- 所有部署配置通过环境变量注入，发布信息可查询。
- 无 JSONP、动态 JavaScript 或 GBK 兼容代码。
- 快照加载具有一致性和完整校验，不按内存参考值限制数据量，刷新失败不影响旧快照请求。
- 导入完整校验、去重、事务原子性和 dry-run 均通过验证。
- 提供迁移、导入格式、API、运行、权限、首次部署和回滚说明。
- 仓库不包含密钥、真实 DSN 或来源不明的数据。
- 提供可运行的性能及故障验证工具；未运行的环境验证项明确标为待验收。

### 21.2 上线验收完成

必须在代码交付完成的基础上，同时满足：

- 在第 16 节基准环境达到 P95 和吞吐目标，记录内存观察结果并据此配置部署资源；内存参考数值不作为通过门槛。
- 数据库中断、恢复、刷新失败及连续刷新演练通过。
- 退出排空、版本观测、监控抓取和访问控制已验证。
- 已生成并发布可追溯的不可变镜像，保留 digest 和回滚版本。
- 实际部署数据的许可证及使用权限已由部署方确认。
- 影子流量、灰度发布和回滚演练由部署方确认通过。

只有两组条件全部满足，项目才达到“生产可替换状态”。代码交付完成不能表述为已经通过上线验收。

## 附录 A：1.0 → 1.1 修订记录

- 明确同一只读一致性事务内加载版本、分类和语句，补齐所有数据修改的版本更新协议。
- 将手动刷新确定为异步 `202`，明确互斥、强制加载、任务上下文和失败行为。
- 明确内存数字仅用于容量规划和优化，不设置语句数量、分类数量、文本总量或导入文件大小配额；保留数据库字段和接口参数的必要校验。
- 固定 UUID、分类、长度、未知/重复查询参数、HEAD/OPTIONS 和错误格式行为。
- 将对外 ID 与版本改为字符串，避免客户端整数精度损失。
- 明确独立迁移、首次导入、分类创建与最小数据库权限流程。
- 定义导入格式、冲突拒绝、无变化跳过、dry-run 和单事务提交规则。
- 补齐代理链解析、CORS、日志脱敏、指标标签及数据库最近观测语义。
- 明确优雅退出的共同期限、后台任务等待以及三种不同的回滚操作。
- 细化外部 MySQL 集成测试、性能数据与方法、发布检查，区分代码交付和上线验收。

## 附录 B：1.1 复审后的精简决定

- 目录按六项实际职责组织，取消预设的 domain/repository/service 层次和强制多份 DTO。
- 内存改为指导指标，删除数据量上限、导入文件大小上限及相关配置；记录数和文本字节数仅用于观测。
- 导入只校验本次数据和已有冲突，不做容量准入聚合，不逐条扫描验证整个句库，不重建快照。
- 低频写入统一先锁版本行，减少多套细粒度锁顺序；读取仍使用无锁的一致性事务。
- 删除单独的导入条数配置、迁移总期限配置和查询分类原始项数限制，保留必要的操作超时及接口参数规则。
- 首版仅检查实际使用的 schema 版本；迁移允许空库初始化，不预建多版本兼容框架。
- 保留性能目标与故障场景，具体数据比例、并发参数和采样方案放在执行报告，不开发压测平台或快照存活追踪系统。
- 保留严格输入、事务原子性、单任务刷新、可信代理、低基数指标及竞态检查，这些直接保护数据和服务正确性。

迁移中断不能等同于业务事务回滚，依据 [MySQL 隐式提交说明](https://dev.mysql.com/doc/refman/8.4/en/implicit-commit.html)；dirty 状态按 [golang-migrate 官方处理流程](https://github.com/golang-migrate/migrate/blob/master/GETTING_STARTED.md#forcing-your-database-version) 由部署方核查修复，不在服务中实现自动修复系统。

## 附录 C：1.1 → 1.2 修订记录

- 随机语句与 UUID 查询继续使用同一个公开响应结构。
- 公开语句响应删除内部数据库 `id`；数据库模型、快照索引及内部排序仍可使用该字段。
- 将旧响应字段 `hitokoto`、`type`、`from`、`from_who` 分别替换为规范字段 `content`、`category`、`source`、`author`。
- 保留 `uuid`、`length` 和字符串形式的 `meta.dataset_version`。
- v1 API 尚未发布，因此不提供旧字段别名、双写响应或兼容开关。

## 附录 D：1.2 → 1.3 修订记录

- 项目边界扩展：新增前台站点、公开投稿、管理员后台（含投稿审核、句子管理、分类管理、多管理员），由 `sentence-api web` 子命令在独立容器中运行，规格见 [前台规格](web-spec.md)。同一镜像、同一标签发布两个容器。读 API 的行为、账号权限和本文件其余约束不变。
- 第 13 节"只交付 API 镜像"含义不变：仍是一个镜像，但该镜像同时提供 `web` 子命令。
- 数据模型新增 `submissions`、`admin_users`、`admin_sessions` 三张表（迁移 `000002`）以及 `site_settings`（迁移 `000003`），`sentences`、`categories`、`dataset_versions` 不改动。读 API 快照查询不依赖新表，因此 **1.3 读 API 与 import 接受 schema 1、2 和 3**；`sentence-api web` 与 `web admin` 要求 schema 3。
- 滚动升级：先用 1.3 镜像替换仍运行在较早 schema 上的读 API 容器 → `migrate up` 到当前版本（读 API 继续服务）→ 启动 `sentence-web`。禁止在仅有 1.2 读 API 时执行 `000002`（1.2 只接受 schema 1）。回滚 web 后可将 schema 降回 1，1.3 读 API 仍可运行；1.2 镜像不能在 schema 2+ 上启动。站点名称、公开地址、联系方式、仓库与备案以后台设置为准，环境变量只作空库种子。
- `sentences.status = 3`（停用）首次有写入路径：后台停用/恢复按第 6.3 节协议递增版本。
- 数据来源决定：初始句子库来自 `hitokoto-osc/sentences-bundle`（AGPL v3），`/dataset` 署名并提供完整句子库导出下载；导出格式与导入格式一致。用户投稿在条款中同意以相同条件公开。程序许可与数据许可分别声明。
- 第 11.1 节"首版不要求 Go 内置限流"仅针对读 API；`sentence-web` 的投稿与登录必须有进程内限流，仍不引入 Redis。
- 第 18 节"需要审核的数据应通过外部管理流程处理"中的外部管理流程即 `sentence-web`。导入命令行为不变。
