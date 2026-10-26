# 降智检测修复版：保留自动账号流程

Fork 自 [jaychou778/degradation-check](https://github.com/jaychou778/degradation-check)，仓库地址：<https://github.com/YqngQB/degradation-check>。

## 这次怎么用

**保留原版菜单自动登录、自动加载账号、单项和批量检测。检测服务只需配置一次 Sub2API 后台地址，不需要给每个账号再配一份上游 Key。**

```text
Sub2API 后台菜单（原来的 token / src_host）
  → 检测服务验证管理员权限，换取临时检测会话
  → 自动加载官方账号列表
  → 选择账号、模型和检测题目
  → 服务端读取所选账号的原始上游配置
  → 按账号配置调用上游 Responses / Chat Completions
  → 保存脱敏后的检测结果
```

目前自动直连支持 **platform=openai、type=apikey**，包括使用 API Key 接入的第三方中转站。OAuth、影子账号及其他平台仍显示在列表中，但不允许自动直测，不会拿隐藏凭据发请求或偷偷退回旧测试接口。

## 为什么需要这个修复

官方 Sub2API v0.2.18 的管理账号测试接口在 Responses 路径发送固定 `hi`，忽略检测题目，可能导致“找不到可渲染的代码块”。强制账号使用 Chat Completions 又会影响需要 Responses 工具调用的 Codex。

本 fork **不改 Sub2API 二进制、账号协议或数据库**。检测直接发送题目到所选账号的上游，不再调用管理账号 `/test` 接口。官方 Sub2API 和 Codex 继续按原配置工作。

这只修复检测题目与协议转发；不会让一个本来不支持 Responses 的上游凭空支持 Codex 工具调用，也不会自动切换协议隐藏上游错误。

## 最少配置

默认模式为 `auto`，只需设置：

```ini
DETECTION_MODE=auto
SUB2API_BASE_URL=https://你的Sub2API后台域名
```

`SUB2API_BASE_URL` 是你自己的官方 Sub2API 后台地址，**不是账号里的上游中转站地址**。有几个上游账号都只填这一处，新增/修改账号后在面板刷新即可。

如果服务端通过内网访问 Sub2API，而浏览器访问公网域名：

```ini
SUB2API_BASE_URL=http://127.0.0.1:8080
SUB2API_PUBLIC_URL=https://你的Sub2API后台域名
```

`SUB2API_PUBLIC_URL` 用于校验原菜单的 `src_host` 和允许官方后台 iframe 嵌入。浏览器参数不能更改服务端实际访问目标。此地址应与后台菜单传来的来源一致；后台必须允许嵌入检测页，HTTPS 后台也需要 HTTPS 检测地址。

不必设置 `SUB2API_ADMIN_TOKEN`、`UPSTREAMS_FILE` 或逐个账号的 API Key。管理员登录令牌由原版菜单传入；独立打开检测页也可手填管理员登录令牌。不要用 Sub2API 发给普通用户调用模型的业务 Key 代替管理员登录令牌。

## Ubuntu 24.04 原生部署

不需要 Docker，也不替换官方 Sub2API。使用配套压缩包里的二进制，`uname -m` 为 `x86_64` 选 amd64，`aarch64` 选 arm64。先验证配套 `SHA256SUMS`，以下以 amd64 为例：

```bash
sha256sum -c SHA256SUMS
sudo useradd --system --home /var/lib/degradation-check --shell /usr/sbin/nologin degradation-check
sudo install -d -m 700 /etc/degradation-check
sudo install -d -o degradation-check -g degradation-check -m 700 /var/lib/degradation-check
sudo install -m 755 degradation-check-linux-amd64 /usr/local/bin/degradation-check
sudo install -m 600 .env.example /etc/degradation-check/env
sudoedit /etc/degradation-check/env
```

将 `SUB2API_BASE_URL` 改成自己的后台地址。创建 `/etc/systemd/system/degradation-check.service`：

```ini
[Unit]
Description=AI account degradation checks
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=degradation-check
Group=degradation-check
EnvironmentFile=/etc/degradation-check/env
Environment=HOST=127.0.0.1
Environment=PORT=8088
Environment=DATA_DIR=/var/lib/degradation-check
ExecStart=/usr/local/bin/degradation-check
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/degradation-check
UMask=0077

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now degradation-check
sudo systemctl status degradation-check
curl http://127.0.0.1:8088/api/health
```

systemd 读取 root 所有、600 权限的环境文件，服务用户不需要直接读取它。用现有 Nginx/Caddy 给检测服务配置独立 HTTPS 域名，反向代理到 `127.0.0.1:8088`，建议读取超时至少 180 秒；腾讯云安全组无需开放 8088。本服务的静态资源和 API 使用根路径，部署在独立域名根目录。

Nginx 的 HTTPS 虚拟主机中可使用以下转发段（证书沿用你自己的域名配置）：

```nginx
location / {
    proxy_pass http://127.0.0.1:8088;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_read_timeout 180s;
}
```

在官方后台 **系统设置 → 自定义菜单** 里，URL 仍填写检测服务地址，例如 `https://check.example.com/`；让 Sub2API 按原版方式自动附加管理员 `token` 和 `src_host`。无需在菜单里手写长期令牌。检测面板显示账号后即可单测或批量检测。

若另外开启 `DC_AUTH_USER` / `DC_AUTH_PASSWORD`（至少 16 字符），整个检测服务会再要求 Basic 登录。默认自动账号模式不要求这层登录，以保留原版 iframe 流程；浏览器对跨站 Basic 登录可能有限制。

## 二次验证与读取凭据

官方普通账号列表/详情会隐藏真实 API Key。本服务在你发起检测时，使用**同一管理员登录会话**请求官方受保护的 `GET /api/v1/admin/accounts/data?ids=所选ID&include_proxies=true`，每次只读取一个所选 ID，避免重名账号错配。不会调用导入、修改或删除账号接口。

- 官方未启用 step-up 时，直接读取并检测。
- 返回 `STEP_UP_REQUIRED` 时，面板弹出六位动态验证码输入框，调用官方 `/api/v1/user/totp/step-up`，验证成功后重试原检测。
- 管理员未启用 TOTP、权限不足或接口不可用时，显示明确错误；不会绕过官方保护，也不需要关闭二次验证。
- 管理员 API Key 被官方 step-up 拒绝时，请通过管理员后台登录会话进入。
- 批量任务会先读取全部所选账号；读取失败不会先跑一半再等验证码。

协议遵循账号 `extra.openai_responses_mode` 的强制设置，其次读取 `openai_responses_supported`；标记缺失默认 Responses，与官方行为一致。模型映射支持精确匹配和末尾 `*` 的最长优先匹配，保留安全请求头覆写。账号配置代理时使用该代理；代理过期/停用/缺失则报错，不自动改走直连或备用代理。不支持覆盖认证、Cookie、连接控制或会话隔离请求头。

## 密钥、结果及升级

- 上游 Key、代理密码只在检测服务内存中使用，不传给浏览器，不写 SQLite 或普通日志。上游错误和回答中出现当前已知凭据时会脱敏。
- 管理员 JWT 通过一次性连接请求交给服务端，保存在有数量上限的内存会话中，最长八小时；浏览器只保存随机检测会话标识。每次访问受保护 API 都重新验证官方管理员权限；JWT 更早失效也会要求重新进入。
- 服务重启会清空会话，但保留检测结果。过期会话会清理；原始 Key 不被持久化缓存。
- 原版菜单最初的 `?token=...` 仍可能出现在反向代理访问日志中。前端会尽快移除 URL 和旧版浏览器令牌缓存，但不能消除已经记录的初始请求。使用 HTTPS，避免日志记录带令牌的查询串，不向第三方开放此服务；已外泄的令牌应撤销。
- 结果、预览和删除均验证管理员权限。预览使用带会话头的请求及隔离 iframe，不把令牌放入预览 URL；生成画面禁止访问网络及父页面。
- **同一 Sub2API 实例的管理员共享结果**；普通账号没有访问权，不提供不同管理员之间的结果隔离。
- 默认 `auto` 沿用原版 `DATA_DIR/degradation.db` 的账号 ID 和表结构。只替换检测服务程序并保留原 `DATA_DIR`，无需迁移官方 Sub2API 数据。首次绑定前确认数据来自同一个 Sub2API；后续同一数据目录拒绝更换后台地址，独立站点要用独立目录。
- 升级前停止检测服务，再备份整个数据目录、程序和配置，防止遗漏 SQLite WAL。已有原版检测服务应沿用它原来的数据路径，不能照抄示例路径后误以为历史被删除。回滚时保留备份。
- 若之前用了本 fork 的手动直连模式，旧结果仍在 `direct-degradation.db`，不会按不同 ID 自动合并。
- 一个账号每类检测仅保留最新一条，累计统计沿用原版。不同模型重复测试会覆盖上一条记录。
- 官方 Sub2API 可继续独立升级；本修复依赖它的账号列表、导出和二次验证 API，官方改动这些 API 时可能需要适配。

## Docker（可选）

```bash
git clone https://github.com/YqngQB/degradation-check.git
cd degradation-check
cp .env.example .env
# 编辑 .env 中的 SUB2API_BASE_URL
chmod 600 .env
docker compose up -d --build
```

不需要 `upstreams.json`。容器访问宿主机 Sub2API 时，容器里的 `127.0.0.1` 不是宿主机，应使用容器可达的官方服务地址，并按需设置 `SUB2API_PUBLIC_URL`。默认只映射宿主机回环端口，配合 HTTPS 反代。

## 仍保留手动直连模式

只有不愿提供管理员会话或无法使用官方导出接口时才需要此模式：

```ini
DETECTION_MODE=direct
DC_AUTH_USER=operator
DC_AUTH_PASSWORD=替换为至少16字符的随机密码
UPSTREAMS_FILE=/etc/degradation-check/upstreams.json
RELAY_API_KEY=你的上游Key
```

从 `upstreams.example.json` 复制后填写渠道地址、`protocol`、`api_key_env`、模型建议等。`id` 为本地渠道 ID，使用独立 `direct-degradation.db`；Key 通过环境变量读取。若用 Docker，需自行增加 JSON 只读挂载和 `UPSTREAMS_FILE`。

另保留 `DETECTION_MODE=sub2api` + `SUB2API_BASE_URL` + `SUB2API_ADMIN_TOKEN` 的旧管理接口兼容模式，要求 Basic 认证。**该兼容模式没有修复 Responses 固定 `hi` 的问题**，不推荐用于此次需求。

## 源码构建与验证

Go 1.23+、Node.js、pnpm：

```bash
cd web
pnpm install --frozen-lockfile
pnpm build
cd ../server
go test ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o ../degradation-check-linux-amd64 .
```

已用本地 HTTP 模拟服务覆盖隐藏凭据列表、分页、所选账号导出、重名账号、二次验证、两种协议的真实题目、模型映射、代理、会话撤销、结果权限及脱敏。浏览器验证菜单跨域 iframe 自动连接、验证码失败/成功重试、批量检测和画面预览。没有使用你的真实后台凭据或调用付费模型；上线后应先用一个账号验证。

检测只反映所选 Key 当次命中的上游路径，不证明上游全部内部账号的状态。指纹只能说明相似度；糖果题原有“独立 21”判分和累计统计有局限；鹈鹕需要人工观察。不能仅凭速度或一次分数断言降智。

## 许可

保留原项目 LGPL-3.0（`LICENSE`），数字指纹参考数据保留 lm-detector 的 MIT 许可。README 正文沿用原版，只更新顶部 fork 提示；本文描述 fork 的实际行为。
