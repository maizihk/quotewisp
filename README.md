# 拾句 Quotewisp

拾句是一个可以自己部署的语句网站。它提供随机一句、分类浏览、访客投稿和管理后台，也可以作为语句 API 使用。

适合搭建个人句子收藏站、文案接口、博客随机语句、应用欢迎语或团队内部语录库。

## 你可以用它做什么

- 在首页随机展示一句话，并按分类和长度筛选。
- 让访客提交语句，由管理员审核后发布。
- 在后台维护语句、分类、投稿、管理员和站点信息。
- 导入原生 JSON 或 Hitokoto JSON，先预览再确认。
- 下载当前公开句子库，或通过 API 接入网站和应用。
- 所有数据保存在一个持久化目录中，方便备份和迁移。

## 用 Docker 启动

先创建数据卷并启动：

```bash
docker volume create quotewisp-data

docker run -d \
  --name quotewisp \
  --restart unless-stopped \
  -p 8080:8080 \
  -e COOKIE_SECURE=false \
  -v quotewisp-data:/var/lib/quotewisp \
  --tmpfs /tmp:rw,noexec,nosuid,size=256m,uid=65532,gid=65532,mode=0700 \
  maizihk/quotewisp:v1.0.0
```

浏览器打开 `http://localhost:8080` 即可看到首页。

`COOKIE_SECURE=false` 只适合本机或局域网 HTTP 访问。绑定域名并启用 HTTPS 后，请去掉这一行，重新创建容器。

## 创建第一个管理员

应用不会设置默认账号或密码。容器启动后运行：

```bash
read -rs -p '管理员密码: ' admin_password
printf '%s\n' "$admin_password" | docker exec -i quotewisp \
  /sentence-api web admin create --username admin --password-stdin
unset admin_password
```

然后打开 `http://localhost:8080/admin/`，使用用户名 `admin` 和刚设置的密码登录。

第一个管理员只能通过上面的命令创建。登录后可以在后台继续添加管理员。

## 日常使用

首页提供随机一句，`/submit` 是访客投稿页面，`/docs` 提供可以直接复制的 API 示例，`/dataset` 可以查看并下载当前公开句子库。

管理后台位于 `/admin/`，主要操作包括：

1. 在“语句”中新增、编辑、停用或恢复内容。
2. 在“分类”中创建分类并调整显示顺序。
3. 在“投稿”中审核访客提交的内容。
4. 在“导入”中上传 JSON 文件，确认预览后导入。
5. 在“站点设置”中填写站名、介绍、公开地址和联系信息。

## 在网站或应用中使用

获取随机一句：

```bash
curl http://localhost:8080/api/v1
```

查看可用分类：

```bash
curl http://localhost:8080/api/v1/categories
```

按分类获取随机一句：

```bash
curl 'http://localhost:8080/api/v1?categories=original'
```

在浏览器中打开 `/docs` 可以看到更多现成示例和参数说明。

## 更新版本

更新前先备份数据卷。然后拉取新镜像并重新创建容器，继续挂载原来的 `quotewisp-data`：

```bash
docker pull maizihk/quotewisp:v1.0.0
docker stop quotewisp
docker rename quotewisp quotewisp-old
```

使用“用 Docker 启动”中的命令重新创建容器。确认新容器中的首页、后台和数据正常后，再删除旧容器：

```bash
docker rm quotewisp-old
```

请使用明确的版本标签，不要依赖浮动标签。

## 备份数据

数据库、管理员账号、站点设置和应用密钥都在 `quotewisp-data` 中。备份前停止容器，再复制整个数据目录；恢复时也要恢复完整目录。

更完整的升级、备份与恢复步骤见 [使用与维护说明](docs/operations.md)。

## 相关链接

- [Docker Hub 镜像](https://hub.docker.com/r/maizihk/quotewisp)
- [v1.0.0 发布说明](https://github.com/maizihk/quotewisp/releases/tag/v1.0.0)
- [导入文件格式](docs/import-format.md)
