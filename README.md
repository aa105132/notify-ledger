# 不凡收款管理端（notify-ledger-server）

一套**手机通知驱动的个人收款管理端**：用安卓手机上跑 [SmsForwarder](https://github.com/pppscn/SmsForwarder) 监听微信/支付宝到账通知，经 PHP 桥接签名后上报到本 Go 服务，由 Go 在多收款账号间分配订单、匹配到账通知，再回写支付结果给 EPay（彩虹易支付）的 `notifyledger` 插件完成订单。

适合**多微信/支付宝账号轮转收款、免挂机、无需 root** 的个人码商场景。

---

## 整体架构

```
  ┌─────────────┐   webhook(原始通知)   ┌──────────────────┐  HMAC 签名+转发   ┌──────────────────────┐
  │  安卓手机    │ ───────────────────▶ │ smsf_bridge.php  │ ───────────────▶ │ notify-ledger-server │
  │ SmsForwarder │   (device_mark+token)│  (PHP 桥接)       │  /api/device/     │       (Go)           │
  └─────────────┘                       │  +smsf_devices   │   notifications   │  - 账号分配/匹配      │
                                        │   .json          │                    │  - 通知流水入库       │
                                        └──────────────────┘                    │  - 管理端 /admin     │
                                               ▲                               └─────────┬────────────┘
                                               │ go_server_url                           │ HMAC /internal/*
                                               │                                         ▼
                                        ┌──────┴───────────────────────────────────────────────┐
                                        │  EPay（彩虹易支付）+ notifyledger_internal.php 插件    │
                                        │  下单 → /internal/collect-sessions → 等待匹配 → 回写    │
                                        └────────────────────────────────────────────────────────┘
```

**为什么需要 PHP 桥接**：SmsForwarder 只能用 webhook 发原始通知字段，无法计算 Go 要求的 HMAC-SHA256 签名（`X-Timestamp`/`X-Nonce`/`X-Signature`）。桥接在中间补上签名，并对每台手机做独立 token 鉴权，防止伪造通知。

**多账号模型**：每部手机 = 一个 `device_no` = 一条 SmsForwarder 转发规则 = Go 里一个绑定到 `(device_no, channel=wxpay)` 的收款账号。Go 的 `pickAccount` 会在所有启用账号间按策略分配订单。

---

## 仓库结构

```
notify-ledger-server/
├── cmd/bufan-ledger/          # Go 服务源码（main.go + main_test.go）
│   └── migrations/001_init.sql # 建表 SQL（启动时自动迁移）
├── bridge/                     # SmsForwarder <-> Go 的 PHP 桥接
│   ├── smsf_bridge.php         #   桥接主文件（部署到 EPay web 根目录）
│   └── notifyledger_internal.php # EPay 侧 Go 回调入口（EPay 插件文件）
├── docs/                       # 设计与对接文档
│   ├── smsforwarder-bridge.md
│   ├── 通知收款监听与Epay对接规划.md
│   └── 微信多账号管理与对外API方案.md
├── deploy/                     # 部署示例
│   ├── bufan-ledger.service.example   # systemd
│   └── nginx-bufan-ledger.conf.example # nginx 反代
├── scripts/                    # 测试脚本
│   ├── smoke.sh                #   Go 健康检查
│   ├── smsf_post.py            #   向 PHP 桥接发伪造通知（端到端联调）
│   └── smsf_bridge_verify.py   #   无 PHP 时复现桥接逻辑直连 Go 验证
├── .env.example                # Go 配置模板
├── smsf_devices.example.json   # 桥接配置模板（真实文件不入库）
└── go.mod / go.sum
```

---

## 快速开始（本机联调）

需要：Go 1.23+、MySQL 5.7+、PHP 8.0+（带 curl/mbstring/openssl 扩展）。

### 1. 配置并启动 Go 服务

```bash
cd notify-ledger-server
cp .env.example .env
# 编辑 .env：填数据库 DSN、EPAY_INTERNAL_SECRET、ADMIN_TOKEN、DEFAULT_DEVICE_SECRET
```

`.env` 关键项：

| 变量 | 说明 |
|------|------|
| `NL_DSN` | MySQL 连接串，如 `user:pass@tcp(127.0.0.1:3306)/epay?parseTime=true&loc=Asia%2FShanghai&charset=utf8mb4` |
| `NL_EPAY_INTERNAL_SECRET` | Go ↔ EPay 内部接口的 HMAC 密钥，两边必须一致 |
| `NL_ADMIN_TOKEN` | 管理端 `/admin?token=...` 的访问 token |
| `NL_DEFAULT_DEVICE_SECRET` | 新建设备的默认密钥（生产建议每台独立密钥） |
| `NL_ACCOUNT_PICK_STRATEGY` | 账号分配策略：`least_amount`/`least_orders`/`round_robin`/`random` |
| `NL_ADB_PATH` | adb 路径，用于管理端「一键扫描已连接手机」 |

```bash
# Windows
GOPROXY=https://goproxy.cn,direct go build -buildvcs=false -o bufan-ledger.exe ./cmd/bufan-ledger
./bufan-ledger.exe

# Linux
GOPROXY=https://goproxy.cn,direct go build -buildvcs=false -o bufan-ledger ./cmd/bufan-ledger
./bufan-ledger
```

启动后会自动建表迁移。健康检查：

```bash
curl http://127.0.0.1:8098/healthz   # {"app":"不凡收款管理端","code":0,...}
```

### 2. 在 Go 管理端配置设备与账号

打开 `http://127.0.0.1:8098/admin?token=<NL_ADMIN_TOKEN>`：

1. **监听设备** → 新建设备，记下 `device_no`（如 `phone-01`）和 `secret`（密钥仅展示一次）。也可用「扫描已连接设备」通过 adb 批量导入。
2. **收款账号** → 为该 `device_no` 新建一个 `channel=wxpay` 的微信账号。

> 多账号：每部手机各建一个 device + 一个 wxpay 账号，Go 会按策略在它们之间分配订单。

### 3. 配置 PHP 桥接

把 `bridge/smsf_bridge.php` 放到 EPay 的 web 根目录。在 **EPay web 根目录的上一级**（刻意放在 web 不可达处）创建 `smsf_devices.json`：

```bash
cp smsf_devices.example.json ../smsf_devices.json
# 编辑 ../smsf_devices.json：go_server_url、每台设备的 secret(Go 设备密钥) 和 token(随机串)
```

```jsonc
{
  "go_server_url": "http://127.0.0.1:8098",
  "default_channel": "wxpay",
  "allow_ips": [],          // 可选：只允许指定 IP 转发，留空=不限
  "log_file": "/var/log/smsf_bridge.log",
  "devices": {
    "phone-01": { "secret": "<Go 设备密钥>", "token": "<随机桥接 token>" }
  }
}
```

> ⚠️ `smsf_devices.json` 含密钥，**必须放在 web 根目录之外**，否则会被当静态文件下载泄露。本仓库只提交了 `smsf_devices.example.json`，真实文件已被 `.gitignore`。

用 PHP 内置服务器快速联调（生产用 nginx/apache）：

```bash
php -S 127.0.0.1:8088 -t /path/to/Epay
curl 'http://127.0.0.1:8088/smsf_bridge.php?act=ping'
# {"code":0,"msg":"pong","go_server":"http://127.0.0.1:8098","devices":["phone-01"],...}
```

### 4. 配置 SmsForwarder（安卓端，每部手机一份）

> 动手机前先把两个值拿到手：① Go 管理端建好这台手机的设备，拿到 `device_no`（如 `phone-01`）和 `secret`；
> ② 在 `smsf_devices.json` 里给这台手机加一条，生成一个随机 `token`。**手机端只需要 `device_no` 和 `token`**
> （`secret` 由桥接用，不进手机）。完整图文流程见 [`docs/android-smsforwarder-setup.md`](docs/android-smsforwarder-setup.md)。

**(1) 安装与权限**
- 从 [SmsForwarder releases](https://github.com/pppscn/SmsForwarder/releases) 装 apk，无需 root。
- `设置` → 打开 `启用转发`，再进 `通知使用权` 把 SmsForwarder 的开关打开（**这步没开什么都收不到**）。
- 把 App 加进电池优化/自启动/后台运行白名单，否则国产 ROM 锁屏会杀后台丢通知。

**(2) 设置设备标识**
- `设置` → `设备标识` / `device_mark`，填该手机的 `device_no`（如 `phone-01`），大小写横杠严格一致。

**(3) 新建转发规则**（`转发` → `+`）

| 字段 | 值 |
|------|-----|
| 转发类型 | 通用接口 / WebHook |
| 请求方式 | `POST` |
| 内容类型 | `application/json` |
| 目标地址 | `http://<桥接服务器IP>:8088/smsf_bridge.php?device_no=phone-01` |
| 请求体 | 见下方模板 |

请求体模板（`[xxx]` 是 SmsForwarder 内置占位符会自动替换；`token` 是写死的串，换成该手机在 `smsf_devices.json` 里的 token）：

```json
{"device_mark":"[device_mark]","token":"本机桥接token","title":"[title]","msg":"[msg]",
 "receive_time":"[receive_time]","timestamp":"[timestamp]","package_name":"[package_name]"}
```

**(4) 匹配条件**（只转收款通知，避免把所有微信通知都发出去）

- 微信：包名 = `com.tencent.mm`，且内容包含 `微信支付收款` 或 `微信收款助手`
- 支付宝（可选）：包名 = `com.eg.android.alipaygphone`，且内容包含 `收款` 或 `到账`

桥接按 `package_name` 自动推断渠道（`com.tencent.mm`→wxpay、`com.eg.android.alipaygphone`→alipay），无需手填；Go 端从 `标题+内容` 解析金额，无需单独带金额字段。

**(5) 手机端自测**
- 规则里的「测试」按钮，或让人给你微信发 0.01 元收款触发。
- 正常返回 `{"code":0,"match_status":"pending","msg":"ok",...}`。
- `403 设备未找到/token 校验失败` → 核对 `device_no` 拼写和 `token` 是否与 `smsf_devices.json` 一致；
  连接超时 → 桥接没跑或端口不通；日志无记录 → 通知使用权没开或后台被杀。

**(6) 多手机**：每部都走一遍 (1)~(5)，`device_no`、`token` 各用各的，别串号。某部下线只需在 Go 管理端
把对应账号 `status` 设为停用，不动手机。

### 5. 端到端验证

无需真机即可联调——用仓库自带的脚本发一条伪造通知：

```bash
# A. 经 PHP 桥接 → Go（需 PHP 桥接在跑）
python scripts/smsf_post.py phone-01 bridge-token-phone-01-please-change 0.13
# -> {"code":0,"match_status":"pending","msg":"ok","trade_no":""}

# B. 本机无 PHP 时，复现桥接签名逻辑直连 Go
python scripts/smsf_bridge_verify.py phone-01 bridge-token-phone-01-please-change 0.05
```

确认入库（Go 内部接口，需 `NL_EPAY_INTERNAL_SECRET`）：

```bash
curl -X POST http://127.0.0.1:8098/internal/events/recent \
  -H "Content-Type: application/json" \
  -H "X-Timestamp: <ts>" -H "X-Nonce: <nonce>" -H "X-Signature: <hmac>" \
  -d '{"limit":5}'
```

正常会看到刚才那条通知，`device_no=phone-01 channel=wxpay acct_id=1 amt=0.13`。

---

## Go 服务接口一览

### 设备上报（SmsForwarder 经桥接调用）

`POST /api/device/notifications` — HMAC 签名头：

```
X-Timestamp: Unix 秒
X-Nonce:     随机 nonce
X-Signature: hex(HMAC-SHA256(device_secret, timestamp + "\n" + nonce + "\n" + raw_body))
```

请求体（`notificationReq`）：

```json
{"device_no":"phone-01","package_name":"com.tencent.mm","channel":"wxpay",
 "title":"微信收款助手","text":"微信支付收款0.13元(朋友到店)","notify_time":1781946781602}
```

`event_id` 留空即可，Go 按 `(deviceNo+pkg+title+text+notifyTime)` 自动生成并去重。

### 内部接口（EPay ↔ Go，`NL_EPAY_INTERNAL_SECRET` 签名）

| 方法/路径 | 用途 |
|----------|------|
| `POST /internal/collect-sessions` | EPay 下单后创建收款会话，Go 分配账号并等待匹配 |
| `POST /internal/status` | EPay 查询 Go 状态、分配策略、接口列表 |
| `POST /internal/stats` | 近 14 天/12 个月汇总、最近会话与通知 |
| `POST /internal/accounts/summary` | 收款账号只读概览 |
| `POST /internal/sessions/query` | 按订单/商户/状态查询收款会话 |
| `POST /internal/sessions/cancel` | 取消仍在等待中的收款会话 |
| `POST /internal/events/recent` | 最近通知流水 |
| `GET  /healthz` | 健康检查（无需签名） |
| `GET  /admin?token=...` | 管理端 |
| `GET  /pay/{session_no}` | 支付页 |

所有 `/internal/*` 用同一种 HMAC 签名（密钥为 `NL_EPAY_INTERNAL_SECRET`），timestamp 5 分钟有效，nonce 防重放。

### 账号分配策略

`NL_ACCOUNT_PICK_STRATEGY`：

- `least_amount`（默认）：金额最少优先，均衡每日收款额
- `least_orders`：订单数最少优先，均衡订单量
- `round_robin`：轮询，多手机平均轮转
- `random`：随机，弱化规律

策略只由 Go 管理端控制；EPay 只负责下单联动，不管码商分配。

---

## EPay 侧对接

`bridge/notifyledger_internal.php` 是 EPay 插件文件，部署到 EPay 站点。它做三件事：

1. 接收 Go 回写的 `{trade_no, event_id, amount, buyer_hint, paid_at}`；
2. 用 `notifyledger_internal_secret`（EPay 分组/通道配置，或 `SYS_KEY` 兜底）做 `hash_equals` HMAC 校验 + nonce 防重放 + 金额一致性校验；
3. 把订单标记为已支付，走 EPay 原生回调链路通知商户。

EPay 侧需配置 `notifyledger_internal_secret` 与 Go 的 `NL_EPAY_INTERNAL_SECRET` 完全一致。

---

## 安全要点

- **三层鉴权**：SmsForwarder→桥接用每台手机独立的 `token`；桥接→Go 用设备 `secret` 的 HMAC；Go→EPay 用 `NL_EPAY_INTERNAL_SECRET` 的 HMAC。
- **签名时间窗**：timestamp 5 分钟有效，nonce 防重放。
- **配置文件隔离**：`smsf_devices.json` 与 `.env` 不入仓库、不进 web 根目录。
- **响应体纯净**：桥接强制 `display_errors=0`，避免 PHP 警告混进 JSON 响应（PHP 8.5 已移除 `curl_close()` 调用，因其 deprecated 会污染响应）。
- **可选 IP 白名单**：`smsf_devices.json` 的 `allow_ips` 可限制只接受指定来源转发。

---

## 验证 / 测试

```bash
# Go 单元测试 + 编译
GOPROXY=https://goproxy.cn,direct go test ./...
GOPROXY=https://goproxy.cn,direct go build -buildvcs=false -o bufan-ledger ./cmd/bufan-ledger

# 健康检查脚本
./scripts/smoke.sh http://127.0.0.1:8098

# PHP 语法检查
php -l bridge/smsf_bridge.php
php -l bridge/notifyledger_internal.php
```

### 实测过的安全/幂等行为

| 场景 | 期望 | 结果 |
|------|------|------|
| 正常通知（phone-01/phone-02） | Go 入库并路由到对应账号 | ✅ phone-01→acct 1，phone-02→acct 2 |
| 重复 `notify_time` | Go 返回 `duplicate`，不重复入库 | ✅ |
| 错误 token（伪造） | PHP 桥接 `403 设备 token 校验失败` | ✅ 不到达 Go |
| 错误签名（篡改） | Go `401 签名错误` | ✅ |

---

## 部署

生产建议：

- Go 服务用 `deploy/bufan-ledger.service.example` 注册为 systemd 服务；
- 前面挂 nginx（`deploy/nginx-bufan-ledger.conf.example`）做 HTTPS 与 `/internal/*` 内网限制；
- PHP 桥接部署在 EPay 站点（nginx + php-fpm），`smsf_devices.json` 放 web 根目录之外；
- 每台手机用**独立**设备密钥与桥接 token，不要全用默认值。

---

## 文档

- [`docs/android-smsforwarder-setup.md`](docs/android-smsforwarder-setup.md) — **安卓端 SmsForwarder 配置流程**（装机、权限、转发规则、自测、多手机）
- [`docs/smsforwarder-bridge.md`](docs/smsforwarder-bridge.md) — 桥接设计细节
- [`docs/通知收款监听与Epay对接规划.md`](docs/通知收款监听与Epay对接规划.md) — 整体方案与对接规划
- [`docs/微信多账号管理与对外API方案.md`](docs/微信多账号管理与对外API方案.md) — 多账号模型与对外 API

---

## 技术栈

Go 1.23 · `go-sql-driver/mysql` · PHP 8.0+（curl/mbstring/openssl） · MySQL 5.7+ · SmsForwarder（Android）
