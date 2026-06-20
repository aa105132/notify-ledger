# 微信多账号管理与对外 API 方案

> 基于 [`aa105132/wechat-decrypt`](https://github.com/aa105132/wechat-decrypt) 的二次封装设计文档
> 目标：在一台 Linux VPS 上用 Docker 多开挂多个微信号，通过**一个统一项目**管理所有账号、监控所有账号消息、对指定账号轮询消息，并把这些能力以 **API 形式提供给外部**。

---

## 0. 先认清这个工具的本质（避免走错路）

`wechat-decrypt` 是一个**本地数据库解密 + 被动监听**工具，不是机器人框架，也不是登录工具。

| 它能做 | 它不能做 |
|---|---|
| 从运行中的微信进程内存提取密钥 | ❌ 自动 / 批量登录账号（必须手机扫码） |
| 解密本地 SQLCipher4 数据库 | ❌ 无头登录、账号密码登录 |
| 轮询本地 `session.db` 拿到「新消息通知」 | ❌ 主动发消息、自动回复、群发 |
| 查历史消息 / 搜索 / 联系人 / 图片语音 | ❌ 提供任何官方 API（个人微信永远没有） |

**核心结论**：登录这一步无法自动化，每个号都要人工扫码一次；登录后，监控/查询是「轮询本地解密数据」实现的，不是腾讯推送。本方案做的是——把「N 个已登录账号的本地数据」聚合成一套对外 API。

> 如果你的真实场景是**商业级、可自动化的多账号消息收发**，正道是**企业微信（WeCom）官方 API**（本仓库也支持企业微信库解密：`decrypt_wxwork_db.py` / `find_wxwork_keys.py`）。个人微信走本方案，请务必读第 7 节风险。

---

## 1. 总体架构

```
┌──────────────────────────────────────────────────────────────┐
│                     对外消费方（你的业务系统）                   │
│         REST 拉取 / SSE 订阅 / Webhook 回调（带鉴权）            │
└───────────────────────────────┬──────────────────────────────┘
                                 │  统一 API（一个入口）
┌───────────────────────────────▼──────────────────────────────┐
│                    Gateway 聚合网关（新写，本方案核心）          │
│  - 账号注册表  wxid → 容器地址 / 状态 / 轮询游标               │
│  - 路由：把 /accounts/{wxid}/* 转发到对应容器                  │
│  - 聚合：合并所有账号的新消息，转成 SSE / Webhook 推送          │
│  - 鉴权：API Key / Token；限流；审计日志                       │
└──────┬───────────────────────┬───────────────────────┬────────┘
       │ HTTP                  │ HTTP                   │ HTTP
┌──────▼──────┐         ┌──────▼──────┐          ┌──────▼──────┐
│ 容器 acct-A │         │ 容器 acct-B │   ...    │ 容器 acct-N │
│ ┌─────────┐ │         │             │          │             │
│ │微信GUI  │ │  每个容器 = 一个微信号，数据目录 / 网络完全隔离      │
│ │Xvfb+VNC │ │         │             │          │             │
│ ├─────────┤ │         │             │          │             │
│ │wechat-  │ │  find_all_keys_linux → decrypt → 本地 HTTP 适配器  │
│ │decrypt  │ │         │             │          │             │
│ ├─────────┤ │         │             │          │             │
│ │代理出口 │ │  每容器独立代理（可选）→ 独立出口 IP              │
│ └─────────┘ │         │             │          │             │
└─────────────┘         └─────────────┘          └─────────────┘
```

设计要点：
- **一个账号一个容器**：天然隔离数据目录（`~/Documents/xwechat_files/<wxid>/db_storage`）、进程、网络出口。
- **每个容器自带一份 `config.json`**，`db_dir` 各指各的。这样就绕过了原工具「单 `db_dir` / 检测到多目录要二选一」的限制（见 `wechat-decrypt/config.py` 的 `_choose_candidate`）。
- **Gateway 是唯一新增的核心代码**，负责把 N 个容器聚合成一套对外 API。

---

## 2. 多开挂号怎么挂（单机多容器）

### 2.1 单容器需要装什么

| 组件 | 作用 | 备注 |
|---|---|---|
| 微信 Linux 原生客户端 | 登录 + 实时收消息，把密文写进本地库 | 官方 `.deb`/`.rpm`，GUI 程序 |
| `Xvfb` | 虚拟显示器（无头服务器没有屏幕） | GUI 程序必须有 X server |
| `x11vnc` + `noVNC` | 把界面投出来，让你**扫码登录** | 仅登录时需要，登录后可关 |
| Python + `wechat-decrypt` | 提取密钥、解密、监听、对外暴露本地接口 | `requirements.txt` |
| 代理出口（可选） | 让该容器走独立 IP | 见第 2.4 |

> 微信 Linux 客户端**没有现成 Docker 镜像**，需要自己写 `Dockerfile`：`FROM debian:12` → 安装微信 `.deb` → 装 `xvfb x11vnc` → 装 Python 依赖 → 入口脚本启动 Xvfb + 微信 + 监听。

### 2.2 关键权限：读进程内存

密钥提取靠读 `/proc/<pid>/maps` + `/proc/<pid>/mem`，**需要 root 或 `CAP_SYS_PTRACE`**（见 `wechat-decrypt/find_all_keys_linux.py` 第 8-9 行、第 125-142 行的权限检查）。

容器启动加 `--cap-add=SYS_PTRACE` 即可，**不需要 `--privileged`**。Electron/Qt 客户端在容器里通常还要 `--security-opt seccomp=unconfined` 或 `--no-sandbox`。

### 2.3 docker-compose 骨架（示例，需按实际镜像调整）

```yaml
# docker-compose.yml —— 每个微信号一个 service
x-wechat-base: &wechat-base
  build: ./wechat-node          # 自建镜像：微信客户端 + wechat-decrypt
  cap_add: [SYS_PTRACE]         # 读进程内存提取密钥
  security_opt: [seccomp:unconfined]
  restart: unless-stopped

services:
  acct-a:
    <<: *wechat-base
    container_name: wx-acct-a
    network_mode: "service:proxy-a"   # ★ 全流量走 proxy-a（见 2.4）
    volumes:
      - ./data/acct-a:/root/Documents/xwechat_files  # 数据隔离
      - ./conf/acct-a/config.json:/app/config.json   # 各自配置
    # VNC 端口不在这里映射，因为共享了 proxy-a 的网络栈，端口在 proxy-a 上开

  proxy-a:                            # acct-a 的独立出口
    image: ghcr.io/your/proxy-sidecar # 例：tun2socks / wireguard / gluetun
    container_name: wx-proxy-a
    cap_add: [NET_ADMIN]
    devices: ["/dev/net/tun"]
    ports:
      - "5901:5901"   # noVNC：扫码登录用（用完可关）
    environment:
      - UPSTREAM_PROXY=socks5://住宅代理A:端口

  acct-b:
    <<: *wechat-base
    container_name: wx-acct-b
    network_mode: "service:proxy-b"
    volumes:
      - ./data/acct-b:/root/Documents/xwechat_files
      - ./conf/acct-b/config.json:/app/config.json
  proxy-b:
    image: ghcr.io/your/proxy-sidecar
    cap_add: [NET_ADMIN]
    devices: ["/dev/net/tun"]
    ports: ["5902:5901"]
    environment:
      - UPSTREAM_PROXY=socks5://住宅代理B:端口

  gateway:                            # 聚合网关（第 4、5 节）
    build: ./gateway
    ports: ["8080:8080"]             # 唯一对外端口
    environment:
      - ACCOUNTS=acct-a@http://acct-a:8000,acct-b@http://acct-b:8000
      - API_TOKEN=换成你的强随机Token
```

### 2.4 每容器独立代理（降低封号关联）

- 用 `network_mode: "service:proxy-x"` 让微信容器**全部流量**走代理 sidecar。微信走自有 TCP 协议（mmtls），**普通 HTTP 代理穿不过**，必须是 SOCKS5 全局隧道 / WireGuard / tun2socks。
- **代理类型比数量重要**：便宜的机房代理仍被风控标记，等于白花钱。要用**住宅 / 4G 代理**，且**固定 IP 不轮换**（IP 跳变像盗号，触发安全锁），地区尽量贴近账号注册地。
- 这只压低「同机房 IP 关联」这个最大信号，**不能根治封号**（见第 7 节）。

### 2.5 登录流程（每个号一次性人工操作）

1. `docker compose up -d acct-a proxy-a`
2. 浏览器开 `http://VPS:5901`（noVNC）→ 看到微信二维码
3. 手机扫码 + 确认登录
4. 登录成功后微信开始把消息写进 `db_storage`，容器内的监听/适配器即可工作
5. （可选）关掉 VNC 端口，只保留后台运行

---

## 3. 单容器内部流水线（每个账号怎么出数据）

```
微信客户端(已登录)
   │ 写入密文库 db_storage/{session,message,contact}/*.db
   ▼
find_all_keys_linux.py   →  all_keys.json   （提取每个库的 enc_key）
   │  ⚠ 新库/密钥轮换时需周期性重跑（见 3.3）
   ▼
按需解密（不必全量落盘）
   ├─ session.db   →  SessionTable   = 「新消息通知源」（最新摘要/未读/时间）
   └─ message_N.db →  Msg_<md5(wxid)> = 消息正文（分片存储）
   ▼
本地出口（三选一，见第 4 节）
```

### 3.1 监控的数据源：`session.db` → `SessionTable`

这是「消息通知」的核心。字段（见 `monitor.py` / `mcp_server.py` 的查询）：

```sql
SELECT username, unread_count, summary, last_timestamp,
       last_msg_type, last_msg_sender, last_sender_display_name
FROM SessionTable WHERE last_timestamp > 0 ORDER BY last_timestamp DESC;
```

`monitor.py` 每 **3 秒**（`POLL_INTERVAL=3`）重新解密这个 2MB 小库，对比出新消息——这就是「轮询」的本质，延迟≈轮询间隔。

### 3.2 消息正文：`message_N.db` → `Msg_<md5(username)>`

每个聊天对象一张表，表名 = `Msg_` + `md5(wxid)`，可能跨多个 `message_0.db / message_1.db ...` 分片。字段：

```sql
SELECT local_id, local_type, create_time, real_sender_id,
       message_content, WCDB_CT_message_content   -- =4 表示 zstd 压缩
FROM [Msg_xxxx] ORDER BY create_time DESC;
```

`mcp_server.py` 已实现了完整的内容解析（文本/图片/语音/转账/红包/引用/合并转发/位置/名片等），可直接复用。

### 3.3 密钥会变，需周期性重扫

新建聊天、客户端更新会产生**新库或新 salt**，`all_keys.json` 会缺键。容器内应**定时跑 `find_all_keys_linux.py`**（如每 10-30 分钟，或检测到 `MISSING` 时触发），否则新消息可能解不出。`key_scan_common.py` 的 `cross_verify_keys` 会用已知 key 交叉补齐同 salt 的库。

---

## 4. 统一管理层：把 N 个容器聚合成一个项目

原工具已有**三种现成出口**，可作为容器内的「本地接口」：

| 出口 | 形态 | 端点 / 工具 | 适合 |
|---|---|---|---|
| `monitor_web.py` | HTTP + SSE，端口 5678 | `GET /stream`(SSE 实时推) `GET /api/sessions` `GET /api/history` `GET /api/tags` | **最省事**：直接每容器跑一个，网关反代聚合 |
| `mcp_server.py` | MCP (stdio) | `get_recent_sessions` `get_chat_history` `search_messages` `get_new_messages` `get_contacts` … | 给 Claude/LLM 用；非 HTTP，不便对外 |
| `monitor.py` | CLI stdout | 实时打印 | 调试 / 日志采集 |

### 推荐做法：每容器一个「薄 HTTP 适配器」

直接**复用 `mcp_server.py` 里的查询函数**（它们都是纯函数，不依赖 MCP 框架），用 FastAPI 包成标准 REST。这样对外 API 干净可控。

```python
# wechat-node/adapter.py  —— 跑在每个容器内，监听容器内网 :8000
# 复用 mcp_server 已写好的解密/查询逻辑，只做 HTTP 封装
from fastapi import FastAPI
import mcp_server as wx          # 直接 import 现成模块

app = FastAPI()

@app.get("/healthz")
def health():
    return {"wxid": wx._get_self_username(), "keys": len(wx.ALL_KEYS)}

@app.get("/sessions")           # 最近会话（= 新消息通知列表）
def sessions(limit: int = 30):
    return {"text": wx.get_recent_sessions(limit)}

@app.get("/messages")           # 指定聊天的历史
def messages(chat: str, limit: int = 50, offset: int = 0,
             start: str = "", end: str = ""):
    return {"text": wx.get_chat_history(chat, limit, offset, start, end)}

@app.get("/new")                # 增量：自上次以来的新消息（轮询用）
def new():
    return {"text": wx.get_new_messages()}

@app.get("/search")
def search(keyword: str, chat: str | None = None, limit: int = 20):
    return {"text": wx.search_messages(keyword, chat, limit=limit)}
```

> 注：`get_new_messages` 内部用模块级 `_last_check_state` 记录游标，**单进程内自然增量**。多消费方共享一个游标会互相吞消息，所以游标应上移到 Gateway 层（见 5.3），适配器只提供「当前全量会话状态」，由网关做差分。

---

## 5. 对外 API 设计（提供给外部的统一接口）

Gateway 对外暴露一套带鉴权的 REST + 实时通道。所有请求头带 `Authorization: Bearer <API_TOKEN>`。

### 5.1 账号管理

| Method | Path | 说明 |
|---|---|---|
| `GET` | `/v1/accounts` | 列出所有账号：wxid、昵称、在线状态、最近活跃时间、容器健康 |
| `GET` | `/v1/accounts/{wxid}` | 单账号详情与健康（keys 数、session.db mtime、代理出口 IP） |
| `POST` | `/v1/accounts/{wxid}/rescan` | 手动触发 `find_all_keys`（密钥补扫） |

示例响应 `GET /v1/accounts`：
```json
{
  "accounts": [
    {"wxid": "wxid_aaa", "nickname": "客服01", "online": true,  "last_active": 1739000000, "egress_ip": "1.2.3.4"},
    {"wxid": "wxid_bbb", "nickname": "客服02", "online": false, "last_active": 1738990000, "egress_ip": "5.6.7.8"}
  ]
}
```

### 5.2 监控所有账号消息（聚合）

| Method | Path | 说明 |
|---|---|---|
| `GET` | `/v1/messages/new?cursor=<token>` | **拉取所有账号**自 cursor 以来的新消息，返回新 cursor |
| `GET` | `/v1/stream` (SSE) | **订阅所有账号**实时新消息（网关把内部轮询转成推送） |
| `POST` | `/v1/webhooks` | 注册回调 URL，有新消息时网关 POST 给你 |

SSE 事件示例（`GET /v1/stream`）：
```
event: message
data: {"wxid":"wxid_aaa","chat":"张三","chat_type":"private","sender":"张三",
       "type":"text","content":"在吗","ts":1739000123}

event: message
data: {"wxid":"wxid_bbb","chat":"项目群","chat_type":"group","sender":"李四",
       "type":"image","content":"[图片]","ts":1739000130}
```

### 5.3 指定微信号轮询消息

| Method | Path | 说明 |
|---|---|---|
| `GET` | `/v1/accounts/{wxid}/new?cursor=<ts>` | **指定账号**增量轮询：返回 `> cursor` 的新消息 + 新 cursor |
| `GET` | `/v1/accounts/{wxid}/sessions?limit=30` | 指定账号最近会话列表（含未读/摘要） |
| `GET` | `/v1/accounts/{wxid}/messages?chat=张三&limit=50&offset=0&start=&end=` | 指定账号、指定聊天的历史消息 |
| `GET` | `/v1/accounts/{wxid}/search?keyword=合同&chat=&limit=20` | 指定账号内搜索 |
| `GET` | `/v1/accounts/{wxid}/contacts?query=` | 联系人 |

**轮询游标设计（关键）**：cursor 用 `last_timestamp`。Gateway 为「每个消费方 × 每个账号」维护独立游标，差分逻辑放在网关（参考 `get_new_messages` 的 `_last_check_state` 差分算法，但状态外置到网关的 Redis/DB），这样多个外部系统各自轮询互不干扰。

轮询响应示例 `GET /v1/accounts/wxid_aaa/new?cursor=1739000000`：
```json
{
  "wxid": "wxid_aaa",
  "messages": [
    {"chat":"张三","sender":"张三","type":"text","content":"在吗","ts":1739000123}
  ],
  "next_cursor": 1739000123
}
```

### 5.4 鉴权与安全（对外必做）

- **强制 Token**：所有端点校验 `Authorization`；不同消费方发不同 Token，便于审计/吊销。
- **最小暴露**：Gateway 是唯一对外端口；容器适配器（`:8000`）、VNC（`:5901`）只在内网/SSH 隧道可达。
- **限流 + 审计**：消息是高度隐私数据，记录谁在何时拉了哪个账号的什么数据。
- **传输加密**：对外走 HTTPS（网关前置 Nginx/Caddy）。

---

## 6. 轮询 vs 推送（延迟说明）

底层只有「轮询本地库」一种机制（微信不给推送）。链路延迟：

```
对方发消息 → 微信客户端收到并写库（秒级）
           → 容器内每 3s 解密 session.db 检出新消息
           → 网关聚合 → SSE/Webhook 推给你
总延迟 ≈ 轮询间隔（默认 3s，可调）
```

- 想更快：调小容器内轮询间隔（`monitor.py` 的 `POLL_INTERVAL`），代价是 CPU 略升。
- 对外即便提供 SSE/Webhook「推送」，本质仍是网关把内部轮询转成推送，延迟下限 = 轮询间隔。

---

## 7. 风险与限制（务必读）

| 风险 | 说明 | 缓解 |
|---|---|---|
| **封号（最大风险）** | 多账号同机房 IP、GUI 24h 挂机、行为雷同，是腾讯风控重点特征 | 每号独立**住宅/4G 固定 IP**；只挂自己的号；控制规模。**仍无法根治** |
| **登录无法自动化** | 每个号必须人工扫码，且偶发掉线需重新扫 | 接受人工环节；做掉线告警 |
| **密钥轮换** | 新库/更新导致 `all_keys.json` 缺键，消息解不出 | 容器内定时重跑 `find_all_keys_linux.py` |
| **资源** | 每个微信 GUI 容器约几百 MB ~ 1GB RAM | 按内存规划 VPS；CPU 多数时间空闲 |
| **设备指纹** | IP 之外，设备/行为也会被聚类 | 代理只解决 IP 维度，别指望万全 |
| **合规** | 仅可监控**自己拥有或已获授权**的账号数据；微信个人版无官方 API，属逆向/取证用途，违反腾讯使用条款 | 自负风险；商用请转**企业微信官方 API** |

---

## 8. 实施路线（建议顺序）

1. **跑通单容器**：Dockerfile（微信 + Xvfb + VNC + wechat-decrypt）→ 扫码登录 → `find_all_keys_linux.py` 出 `all_keys.json` → `monitor.py` 能打印新消息。
2. **容器内薄适配器**：写 `adapter.py`（FastAPI 复用 `mcp_server` 函数），容器内 `:8000` 可查 `/sessions /new /messages`。
3. **Gateway 网关**：账号注册表 + 路由转发 + 鉴权 + `/v1/messages/new` 聚合差分 + `/v1/stream` SSE。
4. **代理 sidecar**：每容器接独立住宅 IP，`network_mode: service:proxy-x` 全局隧道。
5. **加固**：HTTPS、限流、审计、掉线/缺键告警、密钥定时重扫。

> 唯一需要新写的代码是第 2 步的 `adapter.py`（几十行）和第 3 步的 Gateway。解密、查询、内容解析、密钥提取全部复用 `wechat-decrypt` 现成模块。

---

## 附：关键源码索引（基于 clone 的仓库）

| 能力 | 文件 | 关键点 |
|---|---|---|
| 配置 / 单账号绑定 | `config.py` | `db_dir` 单账号；`_choose_candidate` 多目录二选一 |
| Linux 密钥提取 | `find_all_keys_linux.py` | `/proc/<pid>/mem`，需 root/`CAP_SYS_PTRACE`；`get_pids` 枚举所有微信进程 |
| 扫描算法 | `key_scan_common.py` | `collect_db_files` / `scan_memory_for_keys` / `cross_verify_keys` / `save_results`→`all_keys.json` |
| 数据库解密 | `decrypt_db.py` | SQLCipher4 AES-256-CBC + HMAC-SHA512，page=4096，reserve=80 |
| 实时监听(CLI) | `monitor.py` | 轮询 `session.db`，`POLL_INTERVAL=3` |
| 实时监听(Web) | `monitor_web.py` | 端口 5678；`GET /stream`(SSE) `/api/sessions` `/api/history` |
| 查询/解析(可复用) | `mcp_server.py` | `get_recent_sessions` `get_chat_history` `search_messages` `get_new_messages`；含全类型消息解析 |
| 企业微信 | `decrypt_wxwork_db.py` `find_wxwork_keys.py` | 商用建议改走企业微信官方 API |
