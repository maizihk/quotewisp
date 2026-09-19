# 性能验证

性能结果必须记录硬件、Go 版本、镜像 digest、数据库版本、数据规模、容器内存限制、GOMEMLIMIT、请求组合和测试时长。128 MiB 稳态、220 MiB 刷新峰值和 256 MiB 容器仅是约 1.2 万条数据的容量规划参考，不是数据准入限制。

先用固定种子生成默认 12000 条自编合成数据。分类分布不等，内容包含中文、emoji、长短句和超过 1000 码点的语句：

```bash
go run scripts/generate-fixture.go -count 12000 -seed 20260918 > /tmp/sentences-performance.json
```

`scripts/performance.js` 先独立预热 60 秒，再以 constant-arrival-rate 默认持续 10 分钟发送 1000 RPS；初始并发 32，可按机器调整。读取组合覆盖默认、多分类和长度条件：

```bash
BASE_URL=http://127.0.0.1:8080 RPS=1000 PREALLOCATED_VUS=32 DURATION=10m MIN_REQUESTS=600000 k6 run scripts/performance.js
```

验收阈值为测量阶段有效请求无检查失败、无 HTTP 失败或超时、p95 小于 10 ms 且无 dropped iterations。默认脚本不假定测试时长；执行标准 10 分钟验收时通过 `MIN_REQUESTS=600000` 明确要求完成请求数。报告必须同时记录实际完成 RPS、dropped iterations 和因容量不足未发起的请求，不能只报告延迟。用 `PHASE=refresh` 或 `PHASE=fault` 标记刷新及故障注入阶段，分别保存结果。

压测时同步采集容器 RSS、Go heap、GC、请求延迟和错误率，并至少触发多次快照刷新，观察旧快照回收与刷新峰值。

## 2026-09-20 隔离验收

本次使用 Go 1.27.1 构建的 `release-fixes-20260919t190026z`（提交 `d213967adfa7-dirty`，构建时间 `2026-09-19T19:00:26Z`），镜像 ID `sha256:0cb1fd9412addf6a587c19ad42d31d79b9103a115f50c037527e9226dac308ac`。宿主为 Linux 6.12.107、i5-12400、4 个可用逻辑 CPU，数据库为 MariaDB 11.8.9。API 固定在 CPU 0，限制 1 CPU、256 MiB，`GOMEMLIMIT=192MiB`，未显式设置 `GOMAXPROCS`，`SNAPSHOT_POLL_INTERVAL=5s`，保持 `LOG_LEVEL=info` JSON 访问日志并配置 50 MiB × 3 轮转；k6 0.54.0（`grafana/k6@sha256:1f40432b1cbe7234e977f96c362c9bc550a2d2b583d014dd8669fe40d3e9e755`）固定在 CPU 2–3，预分配 32 VU、最多 256 VU。负载只访问绑定在 `127.0.0.1` 的临时 API，使用独立数据库和 12000 条固定种子合成数据。

测试从 `2026-09-19T19:48:35Z` 开始，先以 100 RPS 预热 60 秒，再以目标 1000 RPS 测量 600 秒。测量阶段完成 599989 个请求，按测量时长计算为 999.982 RPS；检查通过 599989/599989，HTTP 失败 0，p95 为 0.18623 ms。k6 报告 12 个 `dropped_iterations`，请求数也比 600000 门槛少 11，因此本次严格验收结果为**未通过**。这些数据证明服务响应无错误且延迟远低于 10 ms，但不能据此声称满足“无丢发”容量门槛；现有证据不足以把丢发归因到 API 或负载发生器中的某一方。

测试期间每分钟递增独立测试库的 dataset version，以相同 12000 条数据触发完整快照重建，没有修改句子内容。共确认 10 次版本刷新，最后一次位于负载结束边界；测量段内至少 9 次。版本发现延迟为 0.644–5.007 秒，对应 5 秒轮询间隔。指标记录 11 次成功加载（含初始加载），失败和取消均为 0，累计加载耗时 0.395860002 秒。

宿主 `/proc` 与 cgroup v2 的 521 次有效采样显示：进程 RSS 最大 31396 KiB，`VmHWM` 32624 KiB，cgroup `memory.peak` 32964608 bytes，Go heap objects 最大 14655088 bytes，OOM 事件为 0。最初一段容器内采样因 scratch 镜像没有 `awk`/`cat` 而无效，未用于上述数字；压测运行中已切换到宿主采样，cgroup 峰值仍覆盖容器自启动以来的快速刷新。

k6 无法在权限为 0700 的宿主结果目录中写入 `k6-summary.json`，因此原始机器可读 summary 不存在；完整控制台汇总保存在验收产物中，不能将人工整理结果冒充原始 summary。后续复测应预先创建容器用户可写的结果子目录。原始无密钥证据位于 `/home/andan/.cache/sentence-performance/20260920-acceptance/`。

在 i5-12400（4 个可用逻辑 CPU）的本地短跑基准中，100 条快照随机请求为 8058 ns/op、9952 B/op、76 allocs/op，10000 条快照为 12381 ns/op、10044 B/op、76 allocs/op（各 100 次）。这只是实现级短跑结果，不代替上述持续压测。

## 2026-09-20 预分配 VU 短测

同一隔离环境的 10 秒预热、60 秒测量短测中，32 VU 总计完成 60,995 个请求，其中 measured 阶段完成 59,995 个请求并有 6 个 dropped iterations；64 VU 总计完成 61,003 个请求，其中 measured 阶段完成 60,002 个请求且 dropped iterations 为 0。64 VU 短测满足短测阈值并被选作后续正式测量的预分配并发；这些短测结果不代表正式 600 秒验收通过。

32 VU raw metrics 显示 measured 首个时间点为 `2026-09-19T21:03:21.532344606Z`，6 个 dropped points 集中在 `2026-09-19T21:03:21.578311585Z` 至约 `21:03:21.578315307Z`，即 measured 开始约 46 ms 后。在该 dropped 时间点前后 2 秒窗口内，`http_req_blocked` 最大 21.876930 ms，`http_req_connecting` 最大 21.860928 ms，`http_req_duration` 最大 43.974351 ms。该相关性只能说明丢发与短暂排队、连接和请求延迟峰值同时出现，不能证明根因来自 API、k6 调度或宿主资源。

## 2026-09-20 正式隔离复测

正式复测沿用上节环境和固定数据，先预热 60 秒，再使用 64 个预分配 VU 测量 600 秒；测量阶段完成 600001 个请求，即 1000.002 RPS；dropped iterations 为 0，HTTP 失败为 0，checks 全部通过，measured 阶段 p95 为 0.183744 ms。总体（含 warmup）p95 为 0.185322 ms。本次 API 隔离性能复测通过既定 k6 阈值，但不代表全站上线或端到端验收全部通过。

正式阶段确认 10 次 dataset version 刷新，版本从 2 增至 12，全部位于测量阶段；确认延迟约 1.000865–2.008475 秒（1 秒采样精度，刷新版本轮询间隔 5 秒）。CSV 共 660 次有效采样，RSS 与 VmHWM 最大 36,216,832 bytes（34.539 MiB），cgroup memory.current 最大 30,900,224 bytes，memory.peak 最大 31,350,784 bytes（29.898 MiB），Go heap objects 最大 14,704,432 bytes（14.023 MiB），OOM 事件为 0。metrics 成功加载 11 次（含初始加载），failure/canceled 均为 0。

本次证据保存在 `/home/andan/.cache/sentence-performance/sentence_retest_14c5e1920605a9c0/`；`cleanup.json` 显示测试容器、环境文件和数据库用户均已清理。此前正式 FAIL 记录、32 VU measured 59,995 请求以及其根因不确定性均保留。
