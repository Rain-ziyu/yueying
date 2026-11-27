# 月映公网内测部署

## 当前无卡部署方案

- Web 使用 [月映 GitHub Pages](https://rain-ziyu.github.io/yueying/)，从 gh-pages 分支根目录发布。在 web 下运行 npm ci 与 npm run build，把 dist 内容发布到该分支。上线后设置 VITE_API_URL 为实际 Go 服务域名并重新构建。
- 在 [Railway](https://railway.com/dashboard) 的同一项目内创建 Go 服务和私有 Storage Bucket。Go 服务连接本仓库 master 分支，Root Directory 选 /server；构建与健康检查见 server/railway.toml。
- 数据库首选 [Neon Free](https://neon.com/pricing)，单独使用数据库免费额度。DATABASE_URL 使用月映数据库的 TLS 连接串。部署时实测 Railway 到 Neon 的 PostgreSQL 连接；Limited Trial 的出站限制可能影响访问。DEFAULT_STORE 与 LEGACY_STORE 设为 railway-v1。ALLOWED_ORIGINS 包含 https://rain-ziyu.github.io。为 Go 服务生成 Railway 提供的 HTTPS 域名。
- STORES_JSON 使用以下结构，把值替换为桶 Credentials 页给出的对应字段。endpoint 仅填主机名。桶 CORS 允许上述前端来源对对象执行 PUT、GET、HEAD，并允许上传、Range 读取所需请求头。密钥仅存入 Go 服务的环境变量。

~~~json
{"railway-v1":{"provider":"s3","virtualHost":true,"endpoint":"BUCKET_ENDPOINT_HOST","region":"BUCKET_REGION","bucket":"BUCKET_NAME","accessKey":"ACCESS_KEY","secretKey":"SECRET_KEY"}}
~~~

Railway Free 每月包含 1 美元资源用量，额度耗尽会暂停到下一周期。空闲服务可能休眠，运行时间等会消耗额度。请用两条大陆网络实测邀请页、WebSocket 和大文件拖动。私有桶的直接签名下载免桶出站流量费。参阅 [Railway Free](https://docs.railway.com/pricing/free-plan) 和 [视频直传直读](https://docs.railway.com/storage-buckets/uploading-serving)。

视频桶也可独立部署到 [Backblaze B2](https://www.backblaze.com/sign-up/s3)，注册无需绑卡。另建存储配置 ID，填写 B2 的 S3 endpoint、region 和桶级应用密钥；先实测分片、私有下载和大陆网络。B2 免费出站量随月平均存储量变化，约为其三倍。多人重复观看时需比较实际流量。

## 原 Vercel / Render 方案


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

## 账号与房间保留

月映账号可直接注册并用密码登录。登录后的「我的房间」从 PostgreSQL 查询房主和成员身份。同一浏览器里已有的访客身份可在「我的房间」逐个绑定，也可以进房后点击「绑定当前房间身份」。最早版本没有记录创建者的房间只能绑定已有成员身份。

第三方网站登录回调使用部署的 HTTPS 前端首页。将该完整地址登记到平台应用，并在 Railway 后端设置 `LOGIN_CALLBACK_URL`（当前 Web 入口为 `https://rain-ziyu.github.io/yueying/`）。使用平台登记并获准的域名，如启用自有域名，同时更新后端 `ALLOWED_ORIGINS`。各平台可独立启用。

| 登录平台 | 后端环境变量 | 应用管理入口 |
| --- | --- | --- |
| 微信网站应用 | `WECHAT_APP_ID`、`WECHAT_APP_SECRET` | [微信开放平台](https://open.weixin.qq.com/) |
| QQ 网站应用 | `QQ_APP_ID`、`QQ_APP_SECRET`（App Key） | [QQ 互联](https://connect.qq.com/) |
| 支付宝网页授权 | `ALIPAY_APP_ID`、`ALIPAY_PRIVATE_KEY`（应用私钥）、`ALIPAY_PUBLIC_KEY`（支付宝公钥），采用 RSA2 公钥模式 | [支付宝开放平台](https://open.alipay.com/) |
| 华为账号网站应用 | `HUAWEI_APP_ID`（OAuth Client ID）、`HUAWEI_APP_SECRET` | [华为开发者联盟](https://developer.huawei.com/consumer/cn/) |

登录月映账号后，通过账号菜单绑定各平台，可让多个登录方式进入同一账号。各平台独立首次登录会创建独立账号。密钥只在 Go 后端配置。发起网页授权和接收回调需在同一浏览器会话；原生安装包的厂商 SDK 登录另行接入。

Go 服务启动时及运行期间每小时检查匿名房间。进入房间、在线心跳及房间操作更新云端活动时间，浏览大厅或查看房间清单不会续期。新增规则时，存量房间从迁移时刻开始计时。连续 30 天无人使用的匿名房间在后续巡检中清理，服务休眠时在下次启动补查。房主已绑定账号的房间长期保留。数据库将片源、迁移副本和未完成上传的回收任务与房间删除一起提交，对象存储暂不可用时自动重试。
