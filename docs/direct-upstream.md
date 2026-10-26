# 降智检测：上游直连修复版

Fork 自 [jaychou778/degradation-check](https://github.com/jaychou778/degradation-check)。保留糖果题、数字指纹和鹈鹕画面检测，新增服务端配置的 OpenAI 兼容 API Key 上游直连，支持 **Responses 和 Chat Completions**。

## 修复的实际问题

官方 Sub2API v0.2.18 的管理账号测试接口，在 Responses 路径固定发送 `hi`，忽略传入的检测题目；Chat Completions 路径才使用 `prompt`。因此原版画鹈鹕可能提示“找不到可渲染的代码块”。强制账号使用 Chat Completions 又可能使 Codex 工具调用报 `requires Responses for tool calls`。

本 fork 默认直接发送检测题目到配置好的上游，不调用 `/admin/accounts/:id/test`，不修改 Sub2API 账号、协议、优先级或数据库。**Codex 继续使用 Responses；检测协议单独配置。**

```text
Codex → 官方 Sub2API → 上游 Responses
浏览器 → 本服务（登录认证）→ 配置好的上游 Responses / Chat Completions
```

本项目不会把 Responses 自动降级为 Chat Completions，也不是给 Codex 转换工具调用的代理。上游需要实际支持所选协议。一次只检测所选 Key 当次命中的渠道，不能证明上游所有内部账号的状态。

## 安全与行为变化

- 启动必须配置 `DC_AUTH_USER` 和至少 16 字符的 `DC_AUTH_PASSWORD`；**整个站点**（包括结果、预览和删除）统一使用 HTTP Basic 认证。
- API Key 只在服务端环境变量里；页面不读取、不传递 Sub2API 管理员 Token。
- 浏览器不能指定上游地址或凭据，渠道由服务端配置限定。禁止跟随上游 HTTP 重定向，防止密钥被转发。
- 默认原生服务只监听 `127.0.0.1`；Docker 只发布到宿主机回环地址。远程访问使用 SSH 隧道或 HTTPS 反代，Basic 认证不能代替 TLS。
- 默认只允许同源嵌入。推荐独立页面/新标签打开；跨域 iframe 需要另行配置 frame-ancestors 和浏览器认证策略，不能把管理员 Token 加进菜单 URL。
- 修改 API 需要 `X-DC-Request: 1`，阻止其他站点借用浏览器缓存的登录凭据执行删除或发起检测。
- 上游错误和答案中的已配置密钥会脱敏；本地 SQLite 仍包含账号信息和模型回答，请保护数据目录和备份。
- 所有通过登录认证的使用者仍共享本实例结果；**不是多租户隔离服务**。
- 失败、截断、流提前结束均记为检测失败，不根据部分内容判定能力；HTTP 状态码和上游错误可在页面查看。

## Docker 部署

```bash
git clone https://github.com/YqngQB/degradation-check.git
cd degradation-check
cp .env.example .env
cp upstreams.example.json upstreams.json
# 编辑 .env：替换 DC_AUTH_PASSWORD 和 RELAY_API_KEY。
# 编辑 upstreams.json：填写实际地址、协议和模型；删除不需要的示例渠道。
chmod 600 .env upstreams.json
docker compose up -d --build
```

在本机打开 `http://127.0.0.1:8088`，使用 `.env` 中的服务账号密码登录。默认未对公网开放 8088。

从你自己的电脑访问云服务器，可先建立 SSH 隧道：

```bash
ssh -L 8088:127.0.0.1:8088 你的SSH用户@服务器地址
```

然后在电脑浏览器打开 `http://127.0.0.1:8088`。长期使用建议 HTTPS 反代到回环端口，腾讯云防火墙无需开放 8088。

## Ubuntu 24.04 原生二进制部署

不要求 Docker，也不替换 Sub2API。构建后的单文件内嵌前端。

GitHub Actions 的 `degradation-check-linux` 构建附件包含 amd64 / arm64 二进制及 SHA256SUMS；也可以按“源码构建”自行生成。先用 `uname -m` 确认架构。下面以 amd64 为例：

```bash
sudo useradd --system --home /var/lib/degradation-check --shell /usr/sbin/nologin degradation-check
sudo install -d -m 700 /etc/degradation-check
sudo install -d -o degradation-check -g degradation-check -m 700 /var/lib/degradation-check
sudo install -m 755 degradation-check-linux-amd64 /usr/local/bin/degradation-check
sudo install -m 600 .env.example /etc/degradation-check/env
sudo install -m 600 upstreams.example.json /etc/degradation-check/upstreams.json
sudoedit /etc/degradation-check/env
sudoedit /etc/degradation-check/upstreams.json
```

创建 `/etc/systemd/system/degradation-check.service`：

```ini
[Unit]
Description=AI channel degradation checks
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
Environment=UPSTREAMS_FILE=/etc/degradation-check/upstreams.json
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

配置文件需要允许服务用户读取，但不允许其他用户读取：

```bash
sudo chown root:degradation-check /etc/degradation-check
sudo chmod 750 /etc/degradation-check
sudo chown root:degradation-check /etc/degradation-check/upstreams.json
sudo chmod 640 /etc/degradation-check/upstreams.json
sudo systemctl daemon-reload
sudo systemctl enable --now degradation-check
sudo systemctl status degradation-check
```

systemd 由系统管理器读取 `env`，保持 root 所有、600 权限即可；上游 JSON 由服务进程读取，按以上组权限设置。

## 上游配置

`UPSTREAMS_FILE` 指向一个 JSON 数组，例如：

```json
[
  {
    "id": 6,
    "name": "我的中转渠道",
    "base_url": "https://relay.example.com/v1",
    "protocol": "responses",
    "api_key_env": "RELAY_API_KEY",
    "models": ["gpt-6.1-sol"]
  }
]
```

- `id`：稳定且唯一的正整数，用于本地结果关联。不是自动同步的 Sub2API 账号 ID；不要把旧 ID 改给另一个渠道。
- `base_url`：填 API 根地址，支持域名根地址（自动补 `/v1`）、`/v1` 和显式自定义路径；不要填写 `/responses` 或 `/chat/completions`。只能由服务器管理员修改配置，不接受浏览器传入地址。
- `protocol`：`responses` 或 `chat_completions`，明确指定，不自动切换。
- `api_key_env`：存放上游 Key 的环境变量名。多个渠道可以引用不同环境变量；真实 Key 不写入 JSON。
- `models`：页面输入建议，可手工输入上游自定义别名；不自动下载模型列表。
- `max_output_tokens`：可选正整数；Responses 使用 `max_output_tokens`，Chat 使用 `max_completion_tokens`。省略时不强制限制。推理预算也可能占输出额度，过小会导致画面截断。
- 选用的推理强度按协议发送为 `reasoning.effort` / `reasoning_effort`。默认不发送；如果上游不支持，保留“默认”。指纹检测不额外发送推理强度。

如需比较同一渠道的两种协议，可用两个不同 ID、同一个 `api_key_env` 和不同 `protocol` 配置两行。不会影响 Codex 的协议。

## Sub2API 管理接口兼容模式

仍保留原管理接口路径，但管理员凭据改为服务端配置：

```text
DETECTION_MODE=sub2api
SUB2API_BASE_URL=https://你的Sub2API域名
SUB2API_ADMIN_TOKEN=你的管理员令牌
```

此模式不读取 `UPSTREAMS_FILE`。**它不能修复官方 v0.2.18 的 Responses 管理测试接口忽略题目的行为**；页面会提示使用直连模式。原版前端 URL Token 接入方式已移除。

## 结果与升级

- 直连：`DATA_DIR/direct-degradation.db`。
- 管理接口兼容模式：`DATA_DIR/degradation.db`，沿用原表结构。
- 两种模式分开存储，防止本地渠道 ID 与 Sub2API 账号 ID 相同导致覆盖旧结果。旧数据库不会自动转换到直连模式。
- 每个渠道、每类测试保留最新一条结果；不同模型/推理强度再次测试也会覆盖这条记录。糖果题原有多轮统计和判分算法尚未重做，累计计数不能当作严格的逐题准确率。
- 备份时停止检测服务再复制整个数据目录，避免漏掉 SQLite WAL。升级前备份二进制、配置和数据目录；不要删除旧数据卷。
- 升级官方 Sub2API 不需重新应用这个 fork 的补丁，因为直连检测不依赖它的管理接口。

## 检测结果的边界

本次修复解决的是协议与题目转发，不声称提高检测准确率。指纹只说明“像哪个已收录模型”，不能证明身份；糖果题原有的“出现独立 21”判分仍有误判可能；鹈鹕画面需要人工审阅。答得快也不能单独证明降智。不要用单次结果自动停用账号。

## 源码构建与验证

需要 Go 1.23+、Node.js 和 pnpm（CI 使用 pnpm 9）。

```bash
cd web
pnpm install --frozen-lockfile
pnpm build
cd ../server
mkdir -p ../dist
go test ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o ../dist/degradation-check-linux-amd64 .
```

测试使用本地模拟 HTTP 服务，不需要真实 Key，也不调用付费模型。覆盖两种请求格式、真实提示词、推理强度、SSE/JSON、失败/截断、认证、重定向阻断和密钥脱敏。

## 许可与致谢

保留上游 **LGPL-3.0** 许可，见 [LICENSE](LICENSE)。原始检测服务来自 [jaychou778/degradation-check](https://github.com/jaychou778/degradation-check)，题目和检测参考 [CPA Codex Candy Eval](https://github.com/haowang02/cpa-plugin-codex-candy-eval)（MIT），指纹库来自 [lm-detector](https://github.com/Ikaleio/lm-detector)（MIT，许可证随数据保留）。
