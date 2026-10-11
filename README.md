# Mistral 路由 + HTTP 注册机

OpenAI 兼容的 **Mistral 多密钥路由器**，自带纯 HTTP 账号注册机。

注册机默认走纯 HTTP 路径（不需要浏览器 / Camoufox / xvfb）。

## 功能

- HTTP 注册机：Ory 注册 → IMAP 取验证码 → 铸造 API 密钥
- 多线程并发 + 住宅代理池
- OpenAI 兼容反向代理（`/v1/*`）
- 管理界面：密钥管理、注册机控制、运行状态

## 环境要求

| 组件 | 要求 |
|------|------|
| 操作系统 | Linux / macOS / WSL2 |
| Python | **3.10+** |
| 网络 | 能通过 HTTPS 访问 `auth.mistral.ai`、`admin.mistral.ai`、`api.mistral.ai` |
| 收码邮箱 | IMAP catch-all 收件箱，或自建 [自建邮箱服务](https://github.com/maillab/cloud-mail) 站点（二选一） |
| 代理 | 并发数 > 3 时建议配置 |
| Go（可选） | **1.25+**，用于编译路由器（受 `golang.org/x/crypto` 依赖要求限制） |

Python 依赖：

```text
requests
python-dotenv
```

## 快速开始

```bash
git clone <你的仓库地址>
cd mistral-router

make setup
# 编辑 .env 和 config/proxies.ranked.txt
make smoke          # 注册 2 个账号，并发 2
make farm N=20 C=8  # 更大批量
```

克隆后的一行命令：

```bash
make setup && ${EDITOR:-nano} .env config/proxies.ranked.txt && make smoke
```

手动安装：

```bash
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt

cp .env.example .env
# 填写 IMAP_HOST、IMAP_USER、IMAP_PASS、EMAIL_DOMAIN

cp config/proxies.example.txt config/proxies.ranked.txt
# 一行一个代理：
#   http://user:pass@host:port

python farm_http.py 2 2
```

## 配置说明

| 文件 | 字段 |
|------|------|
| `.env` | `OTP_PROVIDER`、`EMAIL_DOMAIN`，以及所选通道的凭据（必填） |
| `config/proxies.ranked.txt` | 代理列表（上量时建议配置） |

### 取码通道

注册机支持三种收验证码的方式，用 `OTP_PROVIDER` 切换（`mailapi` / `selfhost` / `imap`）。不填时自动推断：有 `MAILAPI_KEY` 走 mailapi，有 `SELFHOST_BASE` 走 selfhost，否则走 imap。

**mailapi**（推荐）—— 对接 邮件 API 服务 的 REST API：

```bash
OTP_PROVIDER=mailapi
EMAIL_DOMAIN=your-domain.com
MAILAPI_BASE=https://em.example.com/api/v1
MAILAPI_KEY=sk_live_xxxxxxxx
MAILAPI_DOMAIN_ID=1          # 发件域名 ID，可用 GET /domains 查到
MAILAPI_EXPIRES_HOURS=24     # 邮箱有效期（小时），创建时必填
MAILAPI_PERMANENT=0          # 设 1 则创建永久邮箱（此时不传有效期）
```

流程：`POST /mailboxes` 建号 → 注册 → 轮询 `GET /mailboxes/{id}/messages` → 读 `/messages/{mid}/raw[/parsed]` 提取 6 位码。

| 接口 | 用途 |
|------|------|
| `GET /domains` | 查域名列表，拿 `domainId` |
| `POST /mailboxes` | 建号，body `{localPart, domainId, expiresInHours}` → 返回 `{id, address}` |
| `GET /mailboxes/{id}/messages` | 邮件列表 |
| `GET /mailboxes/{id}/messages/{mid}/raw`（或 `/raw/parsed`） | 邮件原文 / 解析后内容 |
| `POST /mailboxes/{id}/release` · `/extend` | 释放 / 续期 |

> **注意**：邮件 API 服务 是按 **mailboxId** 查邮件，不是按收件地址，所以创建时返回的 `id` 会一路带到取码环节（`wait_otp(..., mailbox_id=...)`）。邮箱过期后记录保留，可用 `/extend` 复活同一地址。

**selfhost** —— 对接自建 [自建邮箱服务](https://github.com/maillab/cloud-mail) 的公开 HTTP 接口，不需要 IMAP：

```bash
OTP_PROVIDER=selfhost
EMAIL_DOMAIN=your-domain.com
SELFHOST_BASE=https://mail.example.com
SELFHOST_ADMIN=admin@your-domain.com
SELFHOST_PASSWORD=你的管理员密码
SELFHOST_CREATE_USER=1
```

用到的接口（全部是 POST）：

| 接口 | 用途 |
|------|------|
| `/api/public/genToken` | 用管理员邮箱 + 密码换 token，body `{email, password}` |
| `/api/public/addUser` | 批量建邮箱，body `{list:[{email}]}`；`SELFHOST_CREATE_USER=1` 时在注册前调用 |
| `/api/public/emailList` | 查邮件，body `{toEmail, size, num}`；轮询提取 6 位验证码 |

> **注意**：自建邮箱服务 把 token 存在服务端 KV 的**单个全局键** `public_key:` 上，每次 `genToken` 都会覆盖旧值——整个站点同时只有一个有效 token。注册机在进程内只取一次并复用，遇到 401 会自动重新换取。如果同时有网页端登录或第二个注册机实例，token 会互相顶掉（能自动恢复，只是多一次请求）。

**imap** —— 传统 catch-all 邮箱：

```bash
OTP_PROVIDER=imap
EMAIL_DOMAIN=your-domain.com
IMAP_HOST=imap.example.com
IMAP_PORT=993
IMAP_USER=you@your-domain.com
IMAP_PASS=your-imap-password
```

### 代理

注册走 `config/proxies.ranked.txt` 里的代理（一行一个，支持 `http://user:pass@host:port`、`host:port`、`host:port:user:pass`）。

路由器转发 `/v1/*` 到 `api.mistral.ai` 时走**上游代理池**：

```bash
MISTRAL_PROXY=http://127.0.0.1:7890                               # 单个
MISTRAL_PROXIES=http://127.0.0.1:7890,http://user:pass@host:port  # 多个，轮询
PROXY_BAN_AFTER=3      # 连续失败几次后暂停该代理
PROXY_BAN_SECONDS=60   # 暂停时长，到期自动放回
PROXY_ALLOW_DIRECT=1   # 全部被暂停时是否允许直连；设 0 则返回 503
```

单个代理挂了整条链路就断，而且悄无声息——所以做成了池子：多代理轮询，
**连续失败的临时拉黑**，到期自动放回。实测：

```
配置 7890（可用）+ 9999（不可用），连发 8 个请求
→ 全部 http=200（客户端完全无感）
→ 坏代理 used=2 fails=2 banned=true，15s 后自动解封，恢复 2/2 可用
```

仪表盘的「上游健康」会列出每个代理的在线状态与使用次数。

> 邮箱服务（邮件 API 服务 / 自建邮箱服务）的请求**不走**这个代理，直连即可。

### 模型白名单

免费层账号对旗舰模型的限额是 **0**（响应头 `x-ratelimit-limit-req-minute: 0`），直接调用只会拿到 `429 Rate limit exceeded`，很难排查。所以 router 支持一份白名单，把这类模型提前拦掉并返回可读的 400。

配置在 `config/models.available.json`：

```json
{
  "default": "ministral-8b-latest",
  "allow": ["ministral-3b-latest", "ministral-8b-latest", "codestral-latest", "..."]
}
```

行为：

| 场景 | 结果 |
|------|------|
| `GET /v1/models` | 只返回 `allow` 里的模型（上游 46 个被裁到 12 个） |
| 请求 `allow` 外的模型 | `400` + `code: model_not_allowed`，响应里附完整可用清单和 `default` |
| 文件不存在 / `allow` 为空 / JSON 坏了 | 不做过滤，行为与以前完全一致 |

`allow` 支持尾部 `*` 通配（例如 `"ministral-*"`）；文件路径可用 `MODELS_FILE` 覆盖。

**实测可用的 12 个模型**（2026-10-10，免费层新注册账号）：

| 模型 | req/min |
|------|---------|
| `ministral-3b-latest` · `ministral-3b-2512` | 750 |
| `ministral-8b-latest` · `ministral-8b-2512` | 188 |
| `codestral-latest` · `codestral-2508` | 125 |
| `mistral-code-latest` · `mistral-code-fim-latest` | 125 |
| `voxtral-small-latest` · `voxtral-small-2507` | 60 |
| `ministral-14b-latest` · `ministral-14b-2512` | 30 |

**不给的**：`mistral-small-latest`、`mistral-medium-latest`、`mistral-medium-3.x`、`magistral-*`、`mistral-vibe-cli-*`（限额 0）；`labs-leanstral-*`（403 无权限）。

> 复验方法：把 `/v1/models` 里的模型逐个 `POST /v1/chat/completions`，看响应头 `x-ratelimit-limit-req-minute` 是否为 `0`。Mistral 调整免费层策略后，改这个文件的 `allow` 即可，不用改代码。

### 模型能力（实测）

上游 `/v1/models` 自带 `capabilities` 字段，控制台的「模型中心」会直接展示。下面每一项都经过实际调用验证：

| 模型 | 上下文 | 对话 | 函数调用 | 视觉 | 代码补全 | 音频 |
|------|-------:|:----:|:--------:|:----:|:--------:|:----:|
| `ministral-3b-latest` / `-2512` | 128K | ✓ | ✓ | ✓ | | |
| `ministral-8b-latest` / `-2512` | 256K | ✓ | ✓ | ✓ | | |
| `ministral-14b-latest` / `-2512` | 256K | ✓ | ✓ | ✓ | | |
| `codestral-latest` / `-2508` | 256K | ✓ | ✓ | | ✓ | |
| `mistral-code-latest` / `-fim-latest` | 256K | ✓ | ✓ | | ✓ | |
| `voxtral-small-latest` / `-2507` | 32K | ✓ | ✓ | | | ✓ |

> 上下文按 1024 进制计：`262144` = 256K，`131072` = 128K，`32768` = 32K。

**上下文实测（ministral-8b-latest，标称 256K）**

| 输入规模 | 实际 prompt_tokens | 结果 |
|---------|------------------:|------|
| 1,000 行 | 32,915 | ✓ 正常回答 |
| 5,000 行 | **168,915** | ✓ 正常回答（16.9 万 tokens 真实处理） |
| 20,000 行 | 688,915 | ✗ `400 Prompt 688915 > 262144 maximum context length` |

结论：**256K 是实打实的**，不是标称数字。超限时上游会给出明确的 `maximum context length` 报错。

按中文折算，256K tokens 大约能装 **20 万汉字**（《三体》第一部约 20 万字）。作为参照：
GPT-4o 是 128K、Claude Sonnet 4 是 200K、DeepSeek V3 是 128K —— 所以 256K 属于**中等偏上**，
但确实不是 Gemini 2.5 Pro / GPT-4.1 那种 1M 级别。

> 注意上下文和速率是两笔账：单次可以塞满 256K，但账号的每分钟 token 配额只有
> 62.5 万（实测值），一次塞满会吃掉 40% 的分钟配额。长上下文请求建议错峰。

**验证方式与结果**

- **视觉** —— 发一张白底红色圆形的 PNG，`ministral-8b/3b/14b` 均正确答出「红色的圆形」；
  `codestral-latest` 返回 `400 Image input is not enabled for this model`（与能力矩阵一致）。
  请求格式：`content: [{type:"text"},{type:"image_url",image_url:{url:"data:image/png;base64,..."}}]`
- **函数调用** —— 传 `tools` + `tool_choice:"auto"`，ministral / codestral / voxtral 都正确触发
  `get_weather({"city":"北京"})`
- **代码补全** —— `POST /v1/fim/completions`，codestral 与 mistral-code 系列正常补全；
  `ministral-8b-latest` 返回 `400 FIM is not enabled for this model`
- **音频** —— voxtral 用 `content: [{type:"input_audio",input_audio:{data:"<base64>",format:"wav"}}]`
  接收音频，实测 440Hz 纯音被识别为「音调高的警报声」，计费字段为 `prompt_audio_seconds`。
  注意：`/v1/audio/transcriptions` 端点对免费层返回 422，音频要走 chat 接口

**免费层拿不到的**

| 能力 | 状态 | 证据 |
|------|------|------|
| 联网搜索 | ✗ | `tools:[{type:"web_search"}]` → `400 WebSearchTool connector is not supported` |
| 代码执行 | ✗ | `code_interpreter` 同样返回 connector 不支持 |
| 推理模型 | ✗ | `magistral-small/medium-latest` → `429`，`x-ratelimit-limit-req-minute: 0` |
| 图像生成 | ✗ | 需付费计划 |
| OCR | ✗ | `POST /v1/ocr` → `429` |
| 微调 | ✗ | `/v1/fine_tuning/jobs` → `410`，接口已下线 |

**免费层可用的其他端点**（非对话类，不受模型白名单限制）

| 端点 | 状态 | 说明 |
|------|------|------|
| `POST /v1/embeddings` | ✓ | `mistral-embed`，1024 维向量 |
| `POST /v1/moderations` | ✓ | `mistral-moderation-latest` |
| `GET /v1/models` | ✓ | 模型列表（含能力矩阵） |
| `GET /v1/agents` | ✓ | 存在但返回空列表 |

> 模型白名单只约束对话类端点（`/chat/completions`、`/fim/completions`、`/completions`）。
> `embeddings` / `ocr` / `moderations` 用的是另一套模型 id（如 `mistral-embed`），
> 不会被对话白名单拦掉。

常用可调参数：

```bash
CONCURRENCY=8       # 并发数
MAX_ATTEMPTS=20     # 最多注册多少个账号
SPAWN_DELAY_S=0.25  # 每个线程启动间隔（秒）
SKIP_SMOKE=1        # 跳过冒烟测试
OTP_TIMEOUT_S=100   # 等待验证码超时（秒）
LOGIN_RETRIES=4     # 登录重试次数
```

## 输出文件

- `results.json`
- `results/batch_YYYYMMDD_HHMMSS/{accounts,failed,summary}.json`

## 路由器（可选）

```bash
make router
# 管理界面: http://127.0.0.1:7623/admin/
```

或者手动：

```bash
go build -o bin/mistral-router .
FARM_DIR=$PWD DATA_DIR=$PWD/data ./bin/mistral-router
```

> 提示：国内网络编译时若 `proxy.golang.org` 超时，先设置
> `export GOPROXY=https://goproxy.cn,direct`。

## Docker 部署

路由器与注册机打进**同一个镜像**——router 需要拉起 `farm_http.py`，拆成两个容器
反而要额外处理进程通信和共享目录。

```bash
cp .env.example .env      # 填 ADMIN_PASSWORD / 取码通道 / 代理
make docker               # 构建镜像
make docker-up            # docker compose up -d
# → http://127.0.0.1:7623/admin/
```

或者直接用 compose：

```bash
docker compose up -d
docker compose logs -f
```

要点：

| 项 | 说明 |
|---|---|
| 多阶段构建 | Go 静态编译（`CGO_ENABLED=0`）→ `python:3.12-slim` 运行，最终镜像不含 Go 工具链 |
| 端口 | 默认只绑 `127.0.0.1:7623`；公网暴露请在前面加反向代理并启用鉴权 |
| 数据持久化 | `./data`（SQLite 与日志）、`./results`（注册机批次）、`./config`（代理与白名单）都挂到宿主机 |
| 健康检查 | 每 30 秒打一次 `/healthz` |
| 日志轮转 | json-file 驱动，单文件 10MB × 3 份 |
| 密钥 | `.dockerignore` 已排除 `.env` / `proxies.ranked.txt` / `*.db`，镜像里不会带任何凭据 |

**容器访问宿主机代理**：compose 里默认 `MISTRAL_PROXY=http://host.docker.internal:7890`，
并加了 `extra_hosts: host.docker.internal:host-gateway`（Linux 也能解析）。
Linux 上如果仍不通，把 `network_mode` 改成 `host`，或直接填宿主机真实 IP。

## 单元测试

```bash
make test        # 等价于 go test ./...
```

覆盖纯逻辑部分：模型白名单匹配与通配、SSE usage 解析、密钥策略（有效期 / 配额 /
模型与 IP 白名单）、密钥状态推导、账号统计、日志筛选与清理、令牌桶限流、
代理池的轮询 / 拉黑 / 到期解封。

> 这套测试上线后立刻抓到过一个真 bug：`keyState` 用 `time.Parse`（默认按 UTC 解析）
> 却拿本地时间比较，在东八区会让密钥过期判断**晚 8 小时**才生效。现在写入与解析
> 统一用 UTC。
>
> 注意：如果你在修复前就设过带有效期的密钥，那些 `expires_at` 存的是本地时间，
> 升级后会被当作 UTC 解析（提前 8 小时过期）。重新设置一次即可，或手工把旧值加 8 小时。

## 控制台

管理界面在 `http://127.0.0.1:7623/admin/`，共 9 个页面：

| 页面 | 能力 |
|------|------|
| 仪表盘 | 账号健康度、密钥数、今日请求 / Token、成功率、14 天趋势、上游轮询状态 |
| 账号管理 | 存活 / 失效分档、调用量、最近使用时间、恢复与删除 |
| 密钥管理 | 有效期、Token 配额、模型白名单、IP 白名单、启停 |
| 模型中心 | 白名单模型与限速、一键连通性测试、被拦截模型清单 |
| 调用日志 | 全量审计：密钥、账号、模型、来源 IP、状态码、Token、延迟、错误；支持筛选与清空 |
| 用量统计 | 按天 × 模型、按账号两个维度汇总 |
| 在线试玩 | 直接发消息验证链路，首次使用自动创建试玩密钥 |
| 注册机 | 启停、取码通道配置、实时日志、运行历史 |
| 设置 | 轮询阈值、修改密码、接入信息、主题切换 |

### 密钥策略

每把密钥可独立限制：

- **有效期** —— 到期自动失效（`state=expired`）
- **Token 配额** —— 用尽后拒绝（`state=over_quota`）。这是软限制：检查发生在记账之前，所以最后一次请求可能略微超出配额
- **模型白名单** —— 逗号分隔，支持 `ministral-*` 通配
- **IP 白名单** —— 逗号分隔，支持 `10.0.0.*` 通配

任一条件不满足都会返回 `401` 并带上明确错误码（`key_expired` / `key_over_quota` / `model_not_allowed_for_key` / `ip_not_allowed`），同时写入审计日志。

> 代理转发的实现细节：转发时会主动删除客户端的 `Accept-Encoding`。一旦该头存在，
> Go 的 Transport 不会自动解压上游响应，我们拿到的是 gzip 字节流，`usage` 解析失败
> 会让用量与配额统计全部记成 0。删掉后由 Transport 自行协商并透明解压。

### 账号额度

控制台「账号」页可以查每个账号的**实时额度**，点「查询额度」或行内「额度」按钮触发。

额度来自 **chat 响应的速率头**，不依赖登录，最可靠：

```
x-ratelimit-limit-tokens-minute: 625000      ← 每分钟 token 上限
x-ratelimit-remaining-tokens-minute: 624985  ← 本分钟剩余
x-ratelimit-limit-req-minute: 188            ← 每分钟请求上限
x-ratelimit-remaining-req-minute: 187
```

实现方式是并发探测该账号可用的每个模型（`max_tokens=1`），从响应头读速率。
**额度查询同时也验证了模型可用性**——探测结果直接就是「这个账号能调哪些模型」。

**限额是模型级的，不是账号级的。** 这点一开始容易搞错：同一账号调不同模型，
上游给的限额并不一样。

| 模型 | req/min | token/min |
|------|--------:|----------:|
| `ministral-3b-latest` / `-2512` | **750** | 1,300,000 |
| `ministral-8b-latest` / `-2512` | 188 | 625,000 |
| `codestral-*` / `mistral-code-*` | 125 | 625,000 |
| `voxtral-small-*` | 60 | 625,000 |
| `ministral-14b-*` | 30 | 625,000 |

所以账号的「额度」取**可用模型里的最高值**（当前三个账号都是 750 req/min）。
早期实现取的是「第一个成功响应」，读数会随探测顺序在 125~750 之间跳——那是假象，
不是账号之间真有差异。

**关于计划与余额**：这两个字段在 console 侧（`/api/users/v2/organizations` 的
`active_api_plan`、`/api/billing/credits` 的 `wallet_amount`），只能靠账号密码走 Ory
登录流程获取。注册机导入账号时会把 `results.json` 里的密码一并存下（`accounts.password`），
查询时自动尝试登录。

> 已知问题：Mistral 的 Ory 登录对非浏览器客户端会返回 `400 state=choose_method`
> （CSRF 校验失败），此时额度查询会**降级**——仍然返回速率额度与可用模型，只是
> 「计划 / 余额」显示为未取到。这不影响核心功能，因为速率额度才是最实用的那部分。

### 调度与限流

**加权轮询**：账号按实测额度分配流量。额度高的账号拿更多请求，额度未知的按
`defaultWeight = 100` 参与，不会被饿死。算法是 Nginx 那套平滑加权轮询
（每轮各加自身权重、选最大、再减总权重），既按比例分配又不会让低权重账号长时间空转。

额度通过「账号页 → 查询额度」写入 `accounts.rate_limit_req`，查询完会自动 `Reload` 生效。

> 当前三个账号的额度相同（都是 750），所以分配是均等的。这个机制在账号额度出现
> 分化时才有意义——比如某些账号被降级，或未来 Mistral 对不同账号给不同配额。

**上游限流**：全局令牌桶，请求先取令牌，取不到就排队，而不是打上游撞 429。

```bash
UPSTREAM_REQ_PER_MINUTE=300   # 每分钟上限，不设则不限速
UPSTREAM_BURST=5              # 允许的突发量，默认 5
```

> 突发量必须远小于每分钟限额。一开始我把桶容量设成整个分钟额度，结果 10 个并发请求
> 2 秒就跑完了——等于没限流，上游按分钟窗口计数照样撞 429。改成小突发 + 平滑速率后，
> 10 个请求（限额 60/min）实测耗时 5.6s，符合预期。

控制台仪表盘的「上游健康」卡片会展示限流开关、当前限额、排队次数和各账号的流量占比。

**日志保留**：默认保留 30 天，服务启动时清一次、之后每 6 小时一次。

```bash
LOG_RETENTION_DAYS=30   # 0 = 永久保留
```

也可以在「设置 → 日志保留」里手动触发。查询时 `keep_days=0` 表示不清理。

### 告警与自愈

**告警通知**：关键事件推到 Webhook，支持企业微信 / 钉钉 / 飞书 / Telegram / 通用 JSON。

```bash
ALERT_WEBHOOK_URL=https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxx
ALERT_WEBHOOK_TYPE=wecom     # generic(默认) / wecom / dingtalk / feishu / telegram
ALERT_TELEGRAM_TOKEN=...     # 仅 telegram
ALERT_TELEGRAM_CHAT=...
```

触发的事件：

| 事件 | 级别 | 说明 |
|------|------|------|
| 账号池已空 | critical | 所有账号不可用，`/v1` 会返回 503 |
| 账号失效 | warning | 单个账号认证失败被移出轮询 |
| 账号自动恢复 | info | 失效账号探测后重新可用 |

同类告警 **10 分钟内只发一次**（按事件 key 去重），避免故障期间刷屏。设置页有「发送测试」按钮。

> 告警的 HTTP 客户端**不走** `MISTRAL_PROXY`——企业微信/钉钉/飞书在国内直连即可，
> 而代理通常不转发 `127.0.0.1` 这类地址（我一开始复用了模型探测的 transport，
> 本地接收端直接 502）。需要走代理的场景（Telegram）设 `ALERT_USE_PROXY=1`。

**失效账号自动恢复**：

```bash
AUTO_REVIVE_MINUTES=30   # 0 = 关闭
```

账号被判死的原因不都是真死——网络抖动、代理临时不通、上游 5xx 都会让某次请求拿到
401/403，账号就此闲置。这个任务每 N 分钟用 API Key 打一次 `/v1/models`，能通就自动复活。

账号页也有「恢复失效账号」按钮可手动触发，实测：

```
标记 2 个账号为失效 → 触发扫描 → {"revived":2} → 失效 0 / 存活 3
并推送告警：账号已自动恢复: 2 个失效账号探测可用，已重新加入轮询。
```

### 跨域、导出与清理

**CORS**：浏览器可以直接调 `/v1`（默认允许所有来源），只挂在 `/v1` 上——
管理后台是同源访问，不需要跨域头，少开一个口子。

```bash
CORS_ORIGINS=*                              # 默认
CORS_ORIGINS=https://app.example.com,https://foo.dev   # 限定来源
```

预检请求（`OPTIONS`）返回 204 并带上 `Allow-Methods` / `Allow-Headers` / `Max-Age`。

**数据导出**：`GET /admin/api/export?type=accounts|keys|logs&format=json|csv`

| type | 内容 | 条数上限 |
|------|------|---------|
| `accounts` | 账号（含存活与失效）、调用量、实测额度 | 全部 |
| `keys` | 密钥及其策略、用量、状态 | 全部 |
| `logs` | 调用日志 | 5000（`limit` 可调，上限 10000） |

设置页有对应的下载按钮。CSV 用 `Content-Disposition` 直接触发下载，文件名带时间戳。

> 密钥 CSV 含**完整密钥**，注意保管。

**过期密钥清理**：`POST /admin/api/keys/purge-expired {"older_than_days":30}`

只删除「设了有效期且已过期超过 N 天」的密钥。**手动停用（`active=0`）的不会被碰**——
那可能是刻意保留的。留缓冲期也是避免刚过期就删、不好回溯。

### 界面技术

前端是零构建的静态资源（`web/index.html` + `app.css` + `app.js`），由 Go 二进制 embed
分发，沿用 shadcn/ui 的设计 token（oklch 色彩空间、亮暗双主题、0.625rem 圆角）。

## 目录结构

```text
.
  http_farm/           # HTTP 注册机库
    mailapi_otp.py   # 邮件 API 服务 取码通道
    selfhost_otp.py   # 自建邮箱服务 取码通道
    imap_otp.py        # IMAP 取码通道
    otp.py             # 取码通道分发（mailapi / selfhost / imap）
  farm_http.py         # 命令行入口
  Makefile             # setup / smoke / farm / router
  requirements.txt
  .env.example
  config/proxies.example.txt    # 代理模板
  config/models.available.json  # 模型白名单（免费层实测可用模型）
  *.go                 # 路由器
  web/                 # 管理界面（index.html + app.css + app.js，零构建）
  data/ results/       # 运行时数据（已 gitignore）
```

## Make 目标

```bash
make setup          # 建 venv + 装依赖 + 生成 .env 和代理模板
make check          # 检查依赖导入和环境变量
make smoke          # 小批量试跑
make farm N=20 C=8  # 批量注册
make router         # 启动路由器
make clean          # 清理 venv 和构建产物（保留 .env）
```

## 安全提示

- 不要提交 `.env`、代理列表、`data/`、`results/`（已在 gitignore 中）。
- 路由器首次启动时请设置 `ADMIN_PASSWORD`，或使用日志里打印的一次性随机密码。
- 公网 VPS 上建议把管理界面绑定到 localhost，或用反向代理加一层认证。
- 分享压缩包前先跑 `scripts/package.sh`，它会剥离运行时密钥。

## 常见问题

| 现象 | 处理 |
|------|------|
| `错误: 缺少必需的环境变量: IMAP_*` | 复制并编辑 `.env` |
| 大量 `429` | 加代理、降低并发 |
| `OTP timeout`（验证码超时） | 检查 catch-all 收信和 IMAP 凭据 |
| `create_key unauthenticated` | 代理/会话的瞬时问题，重试通常能恢复 |
| Go 编译失败 | 安装 Go 1.25+，或只用 Python 注册机 |

## 备注

- 收验证码是整个流程的主要耗时瓶颈。
- 高并发且不用代理会触发认证接口限流。
- 使用风险自负；请遵守 Mistral 服务条款和当地法律。
