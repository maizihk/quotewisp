# 性能验证

性能结果必须记录硬件、Go 版本、镜像 digest、数据库版本、数据规模、容器内存限制、GOMEMLIMIT、请求组合和测试时长。128 MiB 稳态、220 MiB 刷新峰值和 256 MiB 容器仅是约 1.2 万条数据的容量规划参考，不是数据准入限制。

先用固定种子生成默认 12000 条自编合成数据。分类分布不等，内容包含中文、emoji、长短句和超过 1000 码点的语句：

```bash
go run scripts/generate-fixture.go -count 12000 -seed 20260918 > /tmp/sentences-performance.json
```

`scripts/performance.js` 先独立预热 60 秒，再以 constant-arrival-rate 默认持续 10 分钟发送 1000 RPS；初始并发 32，可按机器调整。读取组合覆盖默认、多分类和长度条件：

```bash
BASE_URL=http://127.0.0.1:8080 RPS=1000 PREALLOCATED_VUS=32 DURATION=10m k6 run scripts/performance.js
```

验收阈值为测量阶段有效请求无检查失败、无 HTTP 失败或超时、p95 小于 10 ms 且无 dropped iterations。报告必须同时记录实际完成 RPS、dropped iterations 和因容量不足未发起的请求，不能只报告延迟。用 `PHASE=refresh` 或 `PHASE=fault` 标记刷新及故障注入阶段，分别保存结果。

压测时同步采集容器 RSS、Go heap、GC、请求延迟和错误率，并至少触发多次快照刷新，观察旧快照回收与刷新峰值。当前环境没有安装 k6，因此持续 10 分钟压测尚未执行，不能声称达到阈值。

在 i5-12400（4 个可用逻辑 CPU）的本地短跑基准中，100 条快照随机请求为 8058 ns/op、9952 B/op、76 allocs/op，10000 条快照为 12381 ns/op、10044 B/op、76 allocs/op（各 100 次）。这只是实现级短跑结果，不代替上述持续压测。
