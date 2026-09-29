# SQLite 镜像、备份恢复与回滚验收

2026-09-29 在本地 Docker 隔离环境执行，结果通过。应用源码为 `9f73a47`，升级前 SQLite 基线为 `a1f7fce`。本记录不代表已经发布镜像或升级生产。

## 验收环境与边界

使用项目 Dockerfile 构建 Linux amd64、纯 Go、scratch 镜像。容器以 `65532:65532` 运行，根文件系统只读，移除全部 capabilities，并启用 `no-new-privileges`。应用数据保存在随机命名的独立卷，HTTP 仅绑定回环地址的随机端口。

| 镜像 | 本地镜像 ID |
| --- | --- |
| `quotewisp:import-validation` | `sha256:cb7382df2351d395b24881d9332e256fcd4b1e4909f8fdc9a0ed94f761a37cc2` |
| `quotewisp:rollback-baseline` | `sha256:1b5cce0d28198597ff9869fb54c516fb585a900732efc98f152728ca00e91b7d` |

两种镜像均仅保存在本地。测试不连接现有数据库，也不使用生产容器、卷、账号或密钥。演练结束后已检查临时容器和卷全部清理。

## 实测结果

| 场景 | 结果 |
| --- | --- |
| 最终镜像启动全新命名卷 | 自动创建 schema 5，生成 1 条自编示例，API 就绪 |
| 旧版初始化 | schema 4；创建 1 个管理员并通过真实 HTTP 登录，保存自定义站点名称和标语 |
| schema 4 → 5 升级 | 原数据不变，账号、设置和密钥保留 |
| 原生 JSON 与 Hitokoto JSON | 均通过后台上传、预览、确认、异步提交和快照刷新；API 可按 UUID 查询 |
| 导入安全边界 | 错误 CSRF 返回 403，重复确认返回 409 |
| 离线备份完整性 | 停止应用后归档整个数据目录；SQLite integrity_check 和 foreign_key_check 通过 |
| 恢复到全新卷 | 保留 3 条语句、1 个管理员、站点设置、密钥及两条已完成导入记录 |
| 文件权限与清理 | 数据库归运行 UID 所有，密钥没有组或其他用户访问权限，已完成任务无上传文件残留 |
| 重建容器 | 恢复卷再次挂载后，管理员仍能登录，导出数据一致 |
| 新 schema 配旧程序 | schema 4 程序读取 schema 5 时非零退出，数据库内容未被降级 |
| 计划回滚 | 恢复升级前整套备份并使用 schema 4 镜像；原始数据、管理员、设置及密钥一致 |

回滚恢复的是升级前的时间点，因此升级后新增的两条测试语句不在回滚结果中。演练没有对新版数据库执行破坏性降级。

## 复现

需要 Docker 和 Python 3 标准库。先在项目根目录构建两个本地镜像：

```bash
docker build --build-arg VERSION=sqlite-import-validation \
  --build-arg GIT_COMMIT=9f73a47 -t quotewisp:import-validation .

baseline_dir=$(mktemp -d)
git archive a1f7fce | tar -x -C "$baseline_dir"
docker build --build-arg VERSION=sqlite-rollback-baseline \
  --build-arg GIT_COMMIT=a1f7fce -t quotewisp:rollback-baseline "$baseline_dir"
rm -rf "$baseline_dir"

python3 scripts/verify-sqlite-release.py \
  --image quotewisp:import-validation \
  --baseline-image quotewisp:rollback-baseline
```

在其他源码版本上复现时，应替换镜像版本标识；该脚本的升级断言固定针对 schema 4 → 5。脚本用随机密码创建测试管理员，密码通过标准输入传入 CLI，不输出密码或密钥。备份仅在演练期间保存于内存和私有临时目录，不保留用户数据制品。

## 尚未覆盖

本次使用小规模数据验证正确性，不能替代大规模导入和并发读取压测。没有进行在线 SQLite 备份、bind mount 恢复、浏览器视觉验收、MySQL 8.4 验收或生产部署。首次管理员初始化保护已在后续修复中补齐，详见下节。

## 万条级复验与后续修复

后续扩大数据规模时，发现 scratch 镜像缺少可写临时目录会使批量写入失败；仅用几条语句的烟雾测试无法覆盖此问题。现已在镜像中预建 `/tmp`，Compose 挂载可配置的私有 tmpfs；31.2 万条规模下，64 MiB 容量可复现快照刷新失败，256 MiB 通过预检。临时空间是独立资源配置，不代表限制句库条目数。

修复后的本地镜像 `quotewisp:perf-validation`，ID 为 `sha256:8a63e2f61b77d883bc4033ecdb19d67fb078088f5bf88e1ae0b253a5510149df`。使用同一个 schema 4 基线重新执行整套演练，将原生格式导入扩大到 12000 条，再导入 1 条 Hitokoto 数据，恢复与回滚全部通过。升级后的恢复核对数为 12002 条语句、1 个管理员；回滚后仍为升级前的 1 条示例、1 个管理员。

此镜像还包括首次管理员初始化的事务保护，以及导入 UUID 查重的索引优化。复验脚本已经采用万条级用例和 256 MiB tmpfs；上文最初的 3 条数据结果保留为历史记录。大规模压力测试结果另见性能报告。
