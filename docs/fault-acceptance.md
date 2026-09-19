# API 数据库中断与恢复验收

执行时间：2026-09-20 03:42 CST（2026-09-19 19:42 UTC）  
执行范围：隔离的临时读 API、临时数据库和临时 TCP 代理；未操作现行 API、Web 或 MariaDB 服务。

## 隔离方式

- 使用已部署制品 `sentence-api:release-fixes-20260919t190026z`，镜像 ID `sha256:0cb1fd9412addf6a587c19ad42d31d79b9103a115f50c037527e9226dac308ac`，大小 16,726,132 字节，运行用户 `65532:65532`。未重新构建或推送镜像。
- 本轮资源前缀为 `sa_fault_20260919194252_16ab8b`。创建独立临时数据库和仅获该库权限的临时用户。
- 临时 API 运行在 Docker 默认 bridge；其 DSN 只指向绑定在 Docker bridge 网关地址上的临时 TCP 代理。代理转发至 MariaDB 的宿主回环端口，因此停止代理只中断本轮 API。
- 凭证仅保存在演练进程内存及临时容器环境中，报告与保留证据均不包含密码或 DSN。

本轮为缩短隔离故障窗口，临时实例显式设置了 `SNAPSHOT_POLL_INTERVAL=1s`、`SNAPSHOT_LOAD_TIMEOUT=8s` 和 `SHUTDOWN_TIMEOUT=5s`。生产代码默认值分别为 60 秒、30 秒和 10 秒；下述 626 ms 恢复时间是在 1 秒轮询配置下测得，不能解释为默认 60 秒轮询下的恢复时延。

## 验收结果

| 场景 | 结果 |
| --- | --- |
| 初始化 | 镜像内 `migrate up` 和固定 `testdata/sentences.json` 导入成功；导入后数据版本为 `2` |
| 基线 | `/readyz` 为 `200`，`/api/v1` 为 `200`，快照版本为 `2` |
| 数据库链路中断 | 停止仅供临时实例使用的 TCP 代理后，`sentence_api_mysql_last_operation_success` 变为 `0`；日志出现 `snapshot_version_check_failed`，分类为 `database` |
| 故障期可用性 | `/readyz` 仍为 `200`，`/api/v1` 仍为 `200`，版本保持 `2`，证明旧快照继续服务 |
| 恢复与重建 | 直接向临时库提交一条唯一句子并在同一事务将版本递增到 `3`，随后恢复代理；新 UUID 接口在 626 ms 内返回 `200`，内容精确匹配，`/readyz` 报告版本 `3` |
| 数据库指标恢复 | `sentence_api_mysql_last_operation_success` 恢复为 `1`；日志记录版本 `3` 的成功快照刷新 |
| 刷新中退出 | 暂停代理使已接受的手动刷新阻塞；`POST /internal/reload` 返回 `202` 后发送 `SIGTERM`。容器在 239 ms 后以状态码 `0` 退出，刷新记录为 `canceled` |

## 清理与证据

- 临时 API 容器已删除。
- 临时数据库不存在；清理校验计数为 `0`。
- 临时数据库用户不存在；未发现本轮用户名前缀残留。
- MariaDB、现行 API、现行 Web、`1panel-network` 和正式数据均未停止或修改。
- 脱敏原始证据保存在 `.cache/fault-evidence-20260919194252_16ab8b/`，包括 HTTP 响应、导入摘要、API 日志和结果清单。

复核时，结果清单中的数据库指标 `0 → 1`、恢复耗时 626 ms、退出耗时 239 ms 与同目录响应和日志一致：故障日志记录 `snapshot_version_check_failed`，恢复日志记录版本 `3` 的成功刷新，退出日志记录刷新 `canceled`；恢复后的 ready 与 UUID 响应也都携带版本 `3`。耗时由保留的演练脚本使用单调相邻的纳秒时间戳计算，证据中不包含凭证。

主机只有 4 个 CPU，本演练在性能压测前串行执行，避免与压测争用。故障窗口仅约 1 秒，未对共享 MariaDB 施加持续负载。
