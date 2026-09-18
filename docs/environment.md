# Go 开发环境

配置日期：2026-09-18。当前只准备开发环境，业务服务尚未开发和部署。

## 环境清单

| 项目 | 当前配置 |
| --- | --- |
| 系统 | Debian 13，Linux amd64 |
| 面板 | 1Panel v2.3.0 |
| Go | 1.27.1，宿主机与容器一致 |
| 项目目录 | `/home/andan/projects/one` |
| 面板运行环境 | 运行环境 → Go → `sentence-go` |
| 容器镜像 | `golang:1.27.1-bookworm` |
| 容器工作目录 | `/app`，挂载项目目录 |
| 对外端口 | 暂无映射 |

本次使用的镜像摘要：

```text
sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b
```

## 宿主机开发

Go 安装在当前用户目录，直接使用已有 PATH 中的命令：

```bash
cd /home/andan/projects/one
go version
go env GOROOT GOPATH GOCACHE GOPROXY
```

| 用途 | 路径或配置 |
| --- | --- |
| 工具链 | `/home/andan/.local/share/go/1.27.1` |
| 命令入口 | `/home/andan/.local/bin/go`、`/home/andan/.local/bin/gofmt` |
| GOPATH | `/home/andan/go` |
| 模块缓存 | `/home/andan/go/pkg/mod` |
| 构建缓存 | `/home/andan/.cache/go-build` |
| GOPROXY | `https://proxy.golang.org,direct` |
| GOSUMDB | `sum.golang.org` |
| GOTOOLCHAIN | `auto` |

宿主机已有 GCC 和 Make，开发环境保留 `CGO_ENABLED=1`，支持 `go test -race`。生产镜像构建时按开发规格单独设置 `CGO_ENABLED=0`。

项目 `go.mod` 当前声明最低 Go 版本为 1.26.0，可使用已安装的 Go 1.27.1 开发。后续 CI 应分别使用 1.26 和 1.27 工具链验证兼容性。

业务代码加入后，在宿主机执行项目检查：

```bash
go test ./...
go test -race ./...
go vet ./...
```

当前尚无业务包，上述项目检查不属于本次已完成的验收。

## 1Panel 管理

运行环境通过 1Panel 原生 API 创建，可在面板的「运行环境 → Go」中查看、停止、启动和重启 `sentence-go`。

当前启动命令：

```bash
go version; exec sleep infinity
```

该命令让开发容器保持运行，便于检查工具链。运行状态表示开发环境可用，业务 API 尚未监听 8080 端口。

| 用途 | 宿主机路径 | 容器路径 |
| --- | --- | --- |
| 源码 | `/home/andan/projects/one` | `/app` |
| 运行环境配置 | `/opt/1panel/runtime/go/sentence-go` | 由面板管理 |
| 模块缓存 | `/opt/1panel/runtime/go/sentence-go/mod` | `/go/pkg/mod` |
| 构建缓存 | `/opt/1panel/runtime/go/sentence-go/build-cache` | `/go/build-cache` |

模块和构建缓存均持久化到宿主机。容器使用 `1panel-network`，未配置对外端口映射。

容器工具链检查：

```bash
docker exec sentence-go go version
docker exec sentence-go go env GOPROXY GOSUMDB GOCACHE
```

日常编辑与编译使用宿主机当前用户，避免通过开发容器向项目目录写入 root 所有的文件。当前开发容器沿用 1Panel Go 模板的默认用户；正式 API 镜像后续按开发规格使用非 root 用户构建和验收。

本机应用目录提供的 Go 模板版本为 1.26，本环境通过原生 API 将实际镜像标签固定为 `1.27.1-bookworm`，并已核验容器内版本。后续编辑运行环境时保留该标签；在面板中另选模板版本会改变 Go 版本。

## 已完成验证

- 官方 Go 安装包 SHA-256 校验通过。
- 宿主机与容器均输出 `go version go1.27.1 linux/amd64`。
- 宿主机与容器均通过 `go test -race unicode/utf8`，验证编译器和竞态检测工具链。
- 宿主机与容器均成功下载并校验 `github.com/google/uuid@v1.6.0`，仅写入模块缓存。
- 通过 1Panel 原生 API 重启运行环境成功，容器恢复运行，模块缓存保留。

本次未配置业务数据库连接或业务服务。已有 MariaDB、PHP 和 OpenResty 容器未作修改；项目内未保存面板密钥、数据库密码或令牌。

1Panel Go 运行环境的使用方式参见[官方文档](https://1panel.cn/docs/v2/user_manual/websites/golang/)。
