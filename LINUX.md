# 在 Linux 上签名 + Apple ID 自动配置描述文件

这个 fork 让 SignTools 自己就能完成签名和分发，不再需要 Mac，也不需要 GitHub Actions 或外部 builder：

- **签名**：服务端在本机直接运行 `signer/sign.py`（`builder.local`）。签名引擎是 [rcodesign](https://github.com/indygreg/apple-platform-rs)，也就是 [Impactor](https://github.com/claration/Impactor) 用的那个库。
- **Apple ID**：在网页上登录 Apple ID（支持 2FA），选一个 team 作为签名配置。签名时会像 Impactor 一样自动完成：
  - 给每个 App 和 App 扩展注册**独立的（非通配符）App ID**，并开启 App 请求的能力，例如 *Increased Memory Limit*
  - 复用或申请一张 *Apple Development* 证书（私钥只存在服务器上）
  - 下载对应的描述文件。描述文件包含 team 里**已注册的设备**，设备需要你自己在开发者后台添加
- **大文件**：用 rsync / SMB 把 IPA 放进 `import_dir`，在网页上选择即可，不经过浏览器上传。

登录和开发者后台接口由 `portal/`（`signtools-portal`）提供，它是基于 Impactor 的 `plume_core` 写的一个小 CLI。

## 部署

```bash
cp signer-cfg.example.yml signer-cfg.yml    # 改 server_url 和 basic_auth 的密码
mkdir -p data/import
docker compose up -d --build
```

compose 里有两个容器：

- `signtools`：网页、签名和 portal 都在这里
- `anisette`：[anisette-v3-server](https://github.com/Dadoum/anisette-v3-server)，登录 Apple ID 需要的 anisette 数据由它提供，不依赖第三方服务器

iOS 一键安装需要有效的 HTTPS，在前面放 Caddy 或 nginx 反代到 `:8080`（nginx 要透传 `Host` 和 `X-Forwarded-Proto`）。

## 使用

1. 点首页右上角的 **Apple ID**，登录 Apple ID，输入 2FA 验证码（也可以改用短信）。
2. 页面上会列出这个 Apple ID 所在的所有 team，例如个人 team 和组织 team。在要用的 team 上点 **Use for signing**，它就会出现在上传窗口的「Signing profile」下拉框里。
   - *allow certificate reset*：team 的开发证书名额满了时，允许吊销一张旧证书再申请新的。这和 Impactor 的行为一致，Impactor 也会先问你。不勾选时，名额满了会报错，由你自己去后台处理。
3. 大 IPA 用 rsync / SMB 放进 `data/import/`。上传窗口里打开 **Server** 开关，选择文件后提交。小文件也可以照常在浏览器上传。
4. 签名进度和日志在首页的 **Logs**（`/local-builder`）里查看。

### Bundle ID

App ID 在整个苹果生态里是唯一的。选「保留原 ID」时，会先尝试注册原来的 Bundle ID。如果已经被别人注册了（比如别人也签过同一个 nxnative 游戏），会自动改用 `<原ID>.<TeamID>`，App 扩展也会跟着改。你也可以选自定义 ID，或者勾选「编码 ID」。

### Increased Memory Limit 等能力

签名时会读取 App 原签名里请求的 entitlements（nxnative 打出来的 IPA 会请求 `com.apple.developer.kernel.increased-memory-limit`），在 App ID 上开启对应的能力，然后才下载描述文件。最终签进去的 entitlements 以描述文件为准。如果某项没能开启，日志里会有醒目警告；设置环境变量 `SIGN_REQUIRE_MEMORY_ENTITLEMENTS=1` 后会直接拒绝签名。

## 证书 + 描述文件模式

原来的用法照样支持：在 `data/profiles/<名字>/` 里放 `cert.p12`、`cert_pass.txt`、`prov.mobileprovision`、`name.txt`。

## 大文件

| 环节 | 处理 |
| --- | --- |
| 导入 | `import_dir` 里的文件通过**硬链接**进入存储（零拷贝），原文件保留，可以换参数再签 |
| 签名任务 | 直接从存储里的文件解压，不走 HTTP；签好的包按路径交回服务端（rename，零拷贝） |
| 重新打包 | 增量回写：`zip -O` 只替换签名时改过的文件，几 GB 的游戏镜像按原压缩数据直接拷贝。6 GB 测试从 286 s 降到 103 s，额外占用磁盘从 12 GB 降到 6 GB |
| 内存 | rcodesign 流式计算哈希，几十 MB |

`import_dir`、`save_dir` 和签名工作目录（默认 `<save_dir>/work`）需要在**同一个文件系统 / 挂载点**上，硬链接和 rename 才能生效。compose 里它们都在 `./data` 下面。工作目录需要大约 2 倍于最大 IPA 的空间。

## 配置项（`signer-cfg.yml`）

| 配置 | 说明 |
| --- | --- |
| `builder.local.enable` | 在服务端本机签名 |
| `builder.local.sign_script` | 签名脚本，默认 `signer/sign.py` |
| `builder.local.work_dir` | 工作目录，默认 `<save_dir>/work` |
| `builder.local.python` | 解释器，默认 `python3` |
| `apple_id.enable` | 启用 Apple ID 页面和对应的签名配置 |
| `apple_id.portal_bin` | portal 程序，默认 `signtools-portal` |
| `apple_id.data_dir` | 会话和证书私钥的存放目录，默认 `<save_dir>/portal`，**请妥善保管** |
| `apple_id.anisette_url` | anisette v3 服务器地址，为空则使用 Impactor 默认的公共服务器 |
| `import_dir` | 服务器上用来放待签名 IPA 的目录 |
| `local_transfer_dir` | 按路径交换文件的目录。使用 `builder.local` 时会自动设置，一般不用管 |

## 自己编译

```bash
(cd portal && cargo build --release)     # signtools-portal
go build -o SignTools .
pip install cryptography lief            # 加上 rcodesign、unzip、zip、openssl、curl
```

## 测试

```bash
SERVER_BIN=./SignTools signer/tests/e2e.sh                   # Apple ID 模式（模拟的开发者后台）
MODE=cert SERVER_BIN=./SignTools signer/tests/e2e.sh         # 证书 + 描述文件模式
BIG_MB=300 SERVER_BIN=./SignTools signer/tests/e2e.sh        # 加一个 300 MB 的文件
go test ./...
```

测试用 clang + lld 在 Linux 上编出一个真实的 arm64 iOS 应用：主程序请求 increased-memory-limit，带一个 Share 扩展和一个 framework，并预先签着另一个 team 的旧签名。`signer/tests/fake-portal.py` 模拟 `signtools-portal`，命令和 JSON 协议都一样：密码 + 短信 2FA 登录、有两个 team、原 Bundle ID 被占用、只授予请求过的能力。测试通过网页走完整个流程，然后检查签好的 IPA：证书、各组件的 Team ID / Bundle ID / entitlements、描述文件、封印。

## 局限

- 真实的 Apple 登录和开发者后台调用**没有在真实账号上测试过**，测试用的都是模拟的 portal。登录、证书、App ID 和描述文件的接口直接用的是 Impactor 的实现。
- App Groups 没有处理（Impactor 会自动建 group，这里没做）。请求 App Groups 的 App 签出来不会带这项权限。
- Apple ID 会话过期后需要在网页上重新登录。
- 设备需要自己在开发者后台注册。描述文件只包含签名那一刻已注册的设备，新加设备后要重签一次。
