# 月映 Yueying

Vue 3 多端播放器、Go 房间服务、PostgreSQL 和可切换的私有对象存储。一次上传供整个房间观看。成员通过房间码以昵称加入，控制事件按房间串行处理，断线重连时恢复最新状态。弹幕即时广播，暂停期间也可以发送。

## 运行

需要 Go 1.25、Node 22.12+、PostgreSQL 和私有 S3 兼容存储。后端实行单实例房间广播。创建一个属于月映的空数据库，赋予其账号建表权限。服务启动时执行幂等建表。以下 PowerShell 配置中的示例值需按自己的环境填写。

```powershell
cd server
$env:DATABASE_URL='postgresql://user:password@db-host/yueying?sslmode=require'
$env:DEFAULT_STORE='r2-v1'
$env:LEGACY_STORE='r2-v1'
$env:STORES_JSON='{"r2-v1":{"provider":"r2","endpoint":"ACCOUNT_ID.r2.cloudflarestorage.com","region":"auto","bucket":"yueying-private","accessKey":"ACCESS_KEY","secretKey":"SECRET_KEY"}}'
$env:ALLOWED_ORIGINS='http://localhost:5173,https://your-site.vercel.app,https://localhost,capacitor://localhost,http://tauri.localhost,tauri://localhost'
go run .
```

默认端口 8080，可配置 PORT 和 LISTEN_HOST。Vercel 的项目根选 web，构建使用 npm run build，输出 dist。设置 VITE_API_URL 为实际 Go 服务的 HTTPS 根地址。开发时在 web 执行 npm ci 和 npm run dev。未指定构建期地址时，可在客户端进房页的「连接设置」填写服务地址。WebSocket 自动从同一地址派生。浏览器邀请地址为网站根地址加 ?room=房间码。

存储端 CORS 允许实际客户端来源、PUT/GET/HEAD、Content-Type、Authorization 和 Range，暴露 ETag、Content-Length、Content-Range、Accept-Ranges。App 端应按真机 WebView 实际 Origin 配置。不要把存储密钥打进客户端。仅本机模拟器可在存储配置中使用 localHTTP。内测使用房间邀请和访客凭据，建房接口默认开放；对公网发布应先限制建房和上传入口。

## 切换与恢复

每个配置 ID 永久标识一个服务和桶。轮换密钥可以复用 ID；换桶请新增 ID。房间引用逻辑片源 ID，数据库保留源文件、可播放版本、上传会话和文件位置。配置过渡期同时保留新旧服务的凭据。

1. 在 STORES_JSON 加入第二个配置。七牛配置使用 provider=qiniu、区域 S3 endpoint（仅主机名）、region、bucket、accessKey、secretKey、uploadHost（区域 HTTPS 上传根地址）、downloadHost（已开通私有下载的 HTTPS 域名）。管理端使用该桶的 S3 兼容接口。上线前实测 Kodo 私有授权、CDN 防盗链策略、分片上传与 Range。
2. 分发支持 kodo-multipart-v2 的客户端。将 DEFAULT_STORE 指向七牛，LEGACY_STORE 保留 R2，重启 Go 服务。仅支持 s3-multipart-v1 的旧端继续写 R2。断线后客户端会重连，未完成的上传使用原配置。
3. 迁移指定物理文件，在 server 目录按相同环境变量运行 `go run . migrate FILE_ID TARGET_STORE`。程序流式复制、逐字节 SHA-256 校验、检查真实播放地址的 HTTP 206 及 Content-Range，再事务切换首选位置。一个原片与其转码版本各有文件 ID，可分别迁移。子文件 ID 由 assets.playable_id 指向。
4. 回退默认写入时改回 DEFAULT_STORE。回退已迁移的文件可对原位置再次运行 migrate；校验留存副本后直接切回，即使当前源端离线也可恢复。仅存于新端的文件需先复制回来。迁移保留源副本，人工清理前应覆盖最长播放签名有效期（一小时）及观察期。
5. 上传会话分片为 16 MiB，单文件上限 8 GiB。取消接口为 POST /api/v1/uploads/{id}/cancel。为桶配置未完成分片的回收策略，并监控总存储和下载用量。应用内的单文件限制不是云账户总账单上限。

## 个人画质

在播放器的画质菜单选择「省流 · 360p」。房间成员都可以使用。首次选择时，服务端按需生成最高 640×360 的 H.264/AAC MP4，准备期间可以继续看默认版本。完成后播放器切换到同一房间时间点。每部视频只保留一个省流副本，若生成文件比默认版本大或相等则沿用默认版本。

Railway 的服务镜像包含 FFmpeg。其他部署需要把带 libx264 和 AAC 编码器的 ffmpeg 加到 Go 服务的 PATH，并提供可写临时目录。生成任务串行执行，会占用托管实例的 CPU、临时磁盘和对象存储额度。任务状态保存在 PostgreSQL，服务重启后继续处理队列。已有房间同样支持。

## 可选云转码

播放兼容文件可直接使用原片。点击「申请转码」会创建去重任务。Go API 和独立 worker 配置 MPS_ENABLED=1 后才启用；使用 `go run . transcode-worker` 启动 worker。它需要 ffprobe 在 PATH 内，以及以下服务端变量。

- TENCENT_SECRET_ID、TENCENT_SECRET_KEY、MPS_REGION、MPS_BUCKET 以及 MPS_TEMPLATE（单档 H.264/AAC MP4 模板编号）。
- MPS_OUTPUT_STORE 指向 STORES_JSON 中用于读取该 COS 暂存桶的配置（使用 COS 区域 endpoint、完整桶名并设 virtualHost=true），MPS_MONTHLY_MINUTES 指定月度转码分钟数。为 MPS 授权访问暂存桶，为产物配置充足的保留时间及生命周期规则。
- 任务提交前探测时长并预占额度。worker 使用数据库锁保证只有一个活动实例，保存服务商任务 ID，轮询完成后将产物复制到当前首选存储并校验。原片与播放版本分别持久化，重复转码请求复用同一任务。故障时可按任务 ID 在服务商后台核对费用与状态。

## 客户端构建

所有原生包内置 Vite 产物。每次修改后先重新构建并同步，再打对应平台包。内测人员使用同一个后端地址和房间码。

| 目标 | 构建 |
| --- | --- |
| Android | 在 web 运行 npm run android，再在 android 工程运行 gradlew assembleDebug。正式内测渠道使用 Android Studio 配置自己的发布签名。 |
| iOS | 在 Mac 运行 npm run ios，安装 CocoaPods 依赖，使用 Xcode 打开 App.xcworkspace，选择团队，Archive 后上传 TestFlight。 |
| HarmonyOS | 在 web 运行 npm run harmony，将 dist 内联资源同步到 ArkWeb rawfile，使用 DevEco Studio 打开 harmony 并 assembleHap。真机安装需要配置本项目的调试签名和设备授权。 |
| Windows | 安装 Rust 与 Tauri 系统依赖，在 web 执行 npm run desktop -- --bundles nsis，生成 setup.exe。 |
| macOS | 在 Mac 安装两个架构的 Rust target，执行 npm run desktop -- --target universal-apple-darwin --bundles dmg，外部分发使用 Developer ID 签名和公证。 |

## 验证

在 server 执行 go test ./...。集成用例 TestStorageSwitch 需要显式设置 TEST_DATABASE_URL 和 TEST_S3_ENDPOINT（本机一次性 PostgreSQL 和 S3 模拟器，测试 S3 凭据 test/test，HTTP）。用例会创建独立的随机测试桶及房间。在 web 执行 npm run build 同时进行 Vue 类型检查与生产构建。

本机集成验证覆盖两个存储配置；实际大陆网络、R2、Kodo、MPS 账单及原生设备均需使用目标账号和设备验收。
