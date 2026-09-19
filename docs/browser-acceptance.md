# 浏览器验收记录

历史结论：公开页面和基本交互通过；长句单屏适配未通过（1000 字模拟句复现），归类 P2 布局问题。该记录保留用于对照，部署后复验见文末。

日期：2026-09-20。目标站点：`https://quote.andan.me`。

使用官方 `mcr.microsoft.com/playwright:v1.55.0-noble` 容器（宿主网络，容器 `--rm`），未改宿主系统依赖、1Panel 或生产数据。证据目录：`.cache/browser-acceptance-20260920/`，原始结果为 `report.json`，截图按桌面和移动视口保存。

公开页面通过真实 Chromium 浏览器访问：首页、`/submit`、`/dataset`、`/docs`、`/admin/login` 在桌面 1440×1000 和移动 390×844 均返回 HTTP 200。页面标题和主要正文均加载；浏览器 console error 和 request failure 均为 0。

首页交互通过：点击“换一句”后 DOM 内容改变，捕获的三次 `/api/v1?max_length=1000` 响应均为 200；授予页面 clipboard 权限后点击“复制”，读取剪贴板内容与当前句子一致。桌面首页 `scrollWidth=innerWidth=1440`，`scrollHeight=viewport=1000`，无横向溢出。

长句适配通过浏览器路由拦截模拟（未修改线上数据）：拦截 `/api/v1` 返回 240 字符自编内容，DOM `data-length=xlong`，内容区域无横向溢出。该项证明前端布局处理，不证明线上接口返回该特定内容。

补充布局证据位于 `.cache/browser-acceptance-20260920/layout/layout-report.json`，覆盖桌面 1440×900 和移动 390×844 的 40、100、240、1000 字符模拟句，并保存 240/1000 字截图。所有样本 `scrollWidth` 等于视口宽度，未出现横向撑出；40/100/240 字样本通过，`pageerror` 均为 0。1000 字样本在桌面/移动的页面高度分别为 1750/1807 像素，按钮均不在首屏视口内，因此“长句缩小字体且不撑出屏幕”的单屏要求未通过。归类 P2 待修布局；建议保持可读字号并约束句子展示区，同时保证内容完整，不截断内容或限制句库数量。

后台登录页仅验证公开登录表单可访问；本轮未复测登录后页面、投稿审核、后台数据维护等受保护流程（历史已有授权账号，但本轮未重用）。测试未提交投稿、未改变线上数据。

## 本地长句布局候选修复

针对上述 P2，本地候选修改仅涉及首页模板、随机脚本和 CSS：极长句区域设置可滚动上限，保留至少 16px 字号；句子节点增加 `tabindex="0"` 以便键盘聚焦；换句时将内部滚动位置重置为顶部。该段记录候选阶段，后续已部署并复验。

Playwright 容器候选证据位于 `.cache/browser-long-fix-v7/report.json`。通过路由注入本地 `/static/site.css` 和 `/assets/random.js`，覆盖 1440×900、390×844、390×667 及 40/100/240/1000 字符模拟句。严格断言全部通过：12 个样本均无页面溢出，按钮在视口内，复制内容完整，1000 字句键盘 End 可滚动到末尾且换句会重置 `scrollTop`，pageerror 为 0；1000 字句内容区域内部滚动。CSS 候选标记和字节数也记录在 JSON 中。该结果是本地候选资源的浏览器拦截验收，部署后复验见文末。

模板为句子节点增加 `tabindex="0"`，与浏览器设置同等属性；相关模板/静态资源集成由 `go test ./internal/web/render ./internal/web/public` 覆盖通过。这证明候选源码行为，不代表线上 HTML 已部署。

## 2026-09-20 已部署候选复验

Web dirty 候选部署后，公开浏览器报告 `.cache/deployed-browser-20260920/public/report.json` 通过：12 个桌面/手机样本页面高度等于 viewport，复制与真实复制均通过；1000 字样本的 End、PageUp 和换句滚动复位通过；实际 HTML `tabindex` 为 `0`；CSS/JS 资源 hash 与本地候选一致；5 个公开页面均返回 200。后台报告 `admin-report` 的 14 个页面通过，cookie 的 Secure、HttpOnly、SameSite=Strict、生效注销及旧 session replay 拒绝均通过，且无 pageerror。

本轮只读验收未执行投稿、审核、用户/句子/分类/站点设置修改等业务操作流。当前部署仍是 dirty 候选，不是正式 clean release；历史 1000 字布局失败记录和候选 v7 记录保留作为追溯历史。
