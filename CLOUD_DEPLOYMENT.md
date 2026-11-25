# 月映公网内测部署

| 模块 | 服务与入口 | 用途 |
| --- | --- | --- |
| Web | [Vercel 项目导入](https://vercel.com/new) | 发布 Vue 页面和房间邀请链接 |
| 房间服务 | [Render Blueprint 部署](https://render.com/deploy?repo=https://github.com/Rain-ziyu/yueying) | 单个 Go 实例同时处理 HTTPS 和 WebSocket |
| 数据库 | [Neon 控制台](https://console.neon.tech/) | 托管 PostgreSQL，保存房间、成员会话、片源和播放状态 |
| 视频存储 | [Cloudflare 控制台](https://dash.cloudflare.com/) | R2 私有桶，客户端凭限时地址上传与播放 |

## 发布顺序

1. 在 Neon 建立 Free 项目，优先选与 Go 服务相同或邻近的区域，取带 TLS 的数据库连接串。Go 服务启动时需要在月映数据库里建表。详情见 [Neon 计划](https://neon.com/pricing)。
2. 开通 R2，在账号内创建 Standard 私有桶。创建仅能读写该桶对象的 S3 凭据。保存账号 ID、桶名、Access Key ID 与 Secret Access Key。密钥填入 Render 的环境变量。标准存储含每月 10 GB·月免费存储，直接出站免费；超额存储和请求可能计费。[R2 价格](https://developers.cloudflare.com/r2/pricing/)
3. 在 Vercel 通过 GitHub 导入 Rain-ziyu/yueying，选择 master 和 web 根目录，使用 Node 22.x、npm ci、npm run build、dist。记录实际分配的生产域名。Hobby 适用于个人非商业用途。[Vercel Hobby](https://vercel.com/docs/plans/hobby)
4. 在 Render 通过上表 Blueprint 导入仓库。填写 DATABASE_URL、ALLOWED_ORIGINS 及 STORES_JSON。ALLOWED_ORIGINS 填实际前端来源，如 https://实际项目名.vercel.app；安装包来源按 README 配置。STORES_JSON 的 R2 字段见 README。免费 Go 实例会在闲置后休眠，重新进房可能需要等唤醒。[Render 免费服务](https://render.com/docs/free)
5. 将 Render 实际提供的 HTTPS 根地址写入 Vercel 的生产环境变量 VITE_API_URL，重新部署 Web。R2 桶配置 CORS，允许上述实际前端来源和 PUT、GET、HEAD；允许 Content-Type、Range 等上传读取所需请求头，暴露 ETag、Content-Length、Content-Range、Accept-Ranges。访问 R2 不需要购买域名。
6. 从公网检查 /health 返回 204，再用两台设备从同一邀请链接进房，上传 MP4，检查暂停、跳转、倍速、弹幕及断线恢复。当前建房入口对访问者开放。亲友试用时控制链接传播，观察实际存储、请求与用量；应用的 8 GiB 单文件限制无法限制账户总开销。

## 可选服务

- 常见不兼容片源的转码使用 [腾讯云媒体处理](https://cloud.tencent.com/product/mps)、[COS](https://cloud.tencent.com/product/cos) 和独立的转码工作进程。先用兼容 MP4 验收基本链路，启用转码时按 README 配置模板和月度分钟额度。
- 后续国内存储可以接入 [七牛 Kodo](https://www.qiniu.com/products/kodo)。切换步骤、旧客户端回退与历史片源迁移见仓库 README。
