# 性能测试

测试使用隔离数据和容器。记录提交、镜像 digest、硬件、Go 版本、数据规模、CPU/内存限制、请求组合、持续时间与导入窗口；内存需求取决于句库大小及快照刷新峰值。

## 读取负载

生成固定种子的自编夹具：

```sh
go run scripts/generate-fixture.go -count 12000 -seed 20260918 > /tmp/sentences-performance.json
```

将夹具导入测试实例后执行 k6：

```sh
BASE_URL=http://127.0.0.1:8080 RPS=1000 PREALLOCATED_VUS=128 DURATION=10m MIN_REQUESTS=600000 k6 run scripts/performance.js
```

脚本先预热 60 秒，再测量默认、多分类和长度条件查询。标准验收要求测量阶段至少完成 600000 请求、无检查失败、无 HTTP 错误、无丢发，P95 小于 10 ms。

## 并发导入

`scripts/verify-sqlite-performance.py` 创建隔离应用和数据卷，在持续读取期间执行批量导入，并输出请求、延迟、内存和导入结果：

```sh
python3 scripts/verify-sqlite-performance.py \
  --image quotewisp:local \
  --k6-image grafana/k6:0.54.0 \
  --output .cache/sqlite-performance
```

镜像需已在本地构建或拉取。可用 `--preflight-only` 先检查导入；用 `--tmpfs-mib` 与 `--vus` 调整临时空间和预分配并发。脚本结束后清理测试容器和卷，保留输出目录。评估结果时同时检查完成请求数、丢发、错误率、导入窗口延迟和内存峰值。
