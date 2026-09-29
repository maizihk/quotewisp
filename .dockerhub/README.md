# 拾句 Quotewisp

拾句是一个可以自己部署的语句网站，适合搭建个人句子收藏站、文案接口、博客随机语句、应用欢迎语或团队内部语录库。

它提供随机一句、分类浏览、访客投稿和管理后台。管理员可以维护语句与分类、审核投稿、导入现有句库，并设置站点名称和介绍。

在线演示：[https://sent.andan.me/](https://sent.andan.me/)

## 为什么有拾句

拾句源于我们对[一言](https://hitokoto.cn/)的使用。一言汇集了许多值得收藏的句子，也给了这个项目最初的灵感。

实际接入时，我们遇到过网络响应较慢和接口调用频率受限的情况。一言官方也[建议请求量较大时自行部署接口或增加缓存](https://developer.hitokoto.cn/sentence/)。于是我们写了拾句，让服务和数据都由自己管理，并在随机语句接口之外加入投稿、审核和管理后台。

线上演示使用[一言开源社区句子库](https://github.com/hitokoto-osc/sentences-bundle)，感谢一言开源社区长期整理并开放这些内容。

## 快速开始

```bash
docker volume create quotewisp-data

docker run -d \
  --name quotewisp \
  --restart unless-stopped \
  -p 8080:8080 \
  -e COOKIE_SECURE=false \
  -v quotewisp-data:/var/lib/quotewisp \
  --tmpfs /tmp:rw,noexec,nosuid,size=256m,uid=65532,gid=65532,mode=0700 \
  maizihk/quotewisp:v1.0.1
```

浏览器打开 `http://localhost:8080` 即可使用。

`COOKIE_SECURE=false` 只适合本机或局域网 HTTP 访问。绑定域名并启用 HTTPS 后，请去掉这一行，重新创建容器。

## 创建管理员

应用没有默认账号和密码。首次启动后运行：

```bash
read -rs -p '管理员密码: ' admin_password
printf '%s\n' "$admin_password" | docker exec -i quotewisp \
  /sentence-api web admin create --username admin --password-stdin
unset admin_password
```

打开 `http://localhost:8080/admin/`，使用用户名 `admin` 和刚设置的密码登录。

## 页面入口

- `/`：随机一句
- `/submit`：访客投稿
- `/docs`：API 使用示例
- `/dataset`：查看和下载公开句子库
- `/admin/`：管理后台

后台可以管理语句、分类、投稿、管理员和站点信息，也可以上传原生 JSON 或 Hitokoto JSON 导入句库。

## 调用示例

```bash
# 随机一句
curl http://localhost:8080/api/v1

# 可用分类
curl http://localhost:8080/api/v1/categories

# 指定分类
curl 'http://localhost:8080/api/v1?categories=original'
```

## 数据与升级

请始终挂载 `quotewisp-data`，管理员账号、语句、站点设置和应用密钥都会保存在其中。

升级前停止容器并备份完整数据卷。拉取新版本后重新创建容器，继续挂载原来的数据卷。建议使用 `v1.0.1` 这样的明确版本标签。

完整说明和更新记录请查看 [GitHub 项目主页](https://github.com/maizihk/quotewisp)。

## 许可与数据来源

拾句程序代码采用 [Apache License 2.0](https://github.com/maizihk/quotewisp/blob/main/LICENSE) 开源。

一言开源社区句子库采用 AGPL-3.0 授权，句子著作权并非全部归一言或本项目所有。使用、修改或重新分发相关句子数据时，请遵守[上游授权与使用说明](https://github.com/hitokoto-osc/sentences-bundle)。程序许可证不改变任何句子数据原有的权利和许可。
