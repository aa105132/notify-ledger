# SmsForwarder 桥接部署指南（多微信账号）

本方案用开源安卓应用 **SmsForwarder**（gitee.com/pp/SmsForwarder）替代自研监听端，
监听微信「微信收款助手」的到账通知，经一个 PHP 桥接服务转发给收款管理端（Go）。

支持 **多微信账号**：每部手机 = 一个 `device_no` = 一条 SmsForwarder 转发规则 = Go 里一个绑定到
`(device_no, channel=wxpay)` 的账号。Go 的 `pickAccount` 会在所有启用账号间分配订单。

## 为什么需要桥接

Go 服务端 `/api/device/notifications` 要求 **HMAC-SHA256 签名** 鉴权（`X-Timestamp` / `X-Nonce` /
`X-Signature`），而 SmsForwarder 只能以 webhook 发送原始通知字段、无法计算 HMAC。
桥接服务在中间补上签名，并对每台手机做 token 鉴权，防止伪造通知。

## 架构

```
微信到账通知
   │ (安卓通知栏)
   ▼
SmsForwarder (每部手机一条规则，body 里带本机 device_no + 桥接 token)
   │  POST JSON
   ▼
smsf_bridge.php  (部署在 EPay 站点，复用 EPay 的 web 入口)
   │  1. 按 device_no 查 smsf_devices.json 拿 secret + 校验 token
   │  2. 字段映射 -> Go notificationReq
   │  3. 计算 HMAC: hex(HmacSHA256(secret, ts + "\n" + nonce + "\n" + body))
   │  4. 带 X-Timestamp / X-Nonce / X-Signature 转发
   ▼
notify-ledger-server (Go) /api/device/notifications
   │  authDevice 验签 -> 按 (device_no, channel) 找到账号 -> 入库 -> matchEvent
   ▼
匹配到待收款会话 -> 回调 EPay 完成订单
```

## 文件清单

| 文件 | 位置 | 作用 |
|------|------|------|
| `smsf_bridge.php` | `Epay/smsf_bridge.php` | 桥接服务，随 EPay 站点一起跑（共用 8088 端口） |
| `smsf_devices.json` | `wxzf/smsf_devices.json`（**EPay web 根之外**） | device_no → {secret, token} 映射，放外面避免被下载泄露密钥 |
| `smsf_bridge.log` | `wxzf/smsf_bridge.log`（可选） | 转发日志，路径在配置里指定 |

> ⚠️ **`smsf_devices.json` 必须放在 EPay web 根目录之外**。EPay 的预览路由会把 web 根下的任意
> 真实文件当静态资源直接吐出，若放进 `Epay/` 内会泄露所有设备密钥。

## 一、Go 收款管理端：建设备 + 建账号

每个微信账号对应一部手机，先在 Go 管理端建好「设备」和「账号」。

1. 打开 `http://127.0.0.1:8098/admin`（带 `?token=<NL_ADMIN_TOKEN>`）。
2. `view=devices` → 新建设备，记下 `device_no`（如 `phone-a`）和 **secret**（密钥只在创建时展示一次，
   也可在数据库 `pre_nl_device.secret` 里查）。
3. `view=accounts` → 新建账号：
   - `channel` = `wxpay`
   - `account_alias` = 该微信号备注（如「微信小号A」）
   - `device_no` = 上一步的 `phone-a`
   - `qrcode_url` = 该微信号的收款码图片 URL（可选，用于收款页展示二维码）

重复以上两步建 N 个微信账号 = N 部手机。Go 的 `pickAccount` 会按策略
（`NL_ACCOUNT_PICK_STRATEGY`：`least_amount` / `least_orders` / `round_robin` / `random`）
在所有启用的 wxpay 账号间分配订单。

## 二、桥接配置：smsf_devices.json

为每个 `device_no` 配一条 `{secret, token}`。`secret` 来自 Go 管理端；
`token` 是你自己生成的随机串（每台手机一个，用作 SmsForwarder → 桥接的鉴权）。

生成 token：
```bash
# Linux / WSL / macOS
openssl rand -hex 16
# 或
head -c 16 /dev/urandom | od -An -tx1 | tr -d ' '

# Windows PowerShell
-join ((48..57)+(97..102) | Get-Random -Count 32 | % {[char]$_})
```

配置示例：
```json
{
  "go_server_url": "http://127.0.0.1:8098",
  "default_channel": "wxpay",
  "allow_ips": [],
  "log_file": "/mnt/d/Desktop/wxzf/smsf_bridge.log",
  "devices": {
    "phone-a": { "secret": "Go管理端里phone-a的密钥", "token": "随机串A" },
    "phone-b": { "secret": "Go管理端里phone-b的密钥", "token": "随机串B" }
  }
}
```

字段说明：
- `go_server_url`：Go 收款服务地址
- `default_channel`：兜底渠道（SmsForwarder 没带包名时用，默认 `wxpay`）
- `allow_ips`：可选 IP 白名单（空数组 = 不限制）；生产建议填 SmsForwarder 出口 IP 或桥接所在服务器
- `log_file`：日志路径，留空则不记日志
- `devices.<device_no>.secret`：Go 设备密钥
- `devices.<device_no>.token`：桥接 token，写进该手机 SmsForwarder 的转发体

改完无需重启——桥接每次请求都重新读配置文件。

## 三、SmsForwarder 每部手机的转发规则

每部手机装一个 SmsForwarder，**设备标识** 填该手机的 `device_no`（如 `phone-a`），
新建一条「转发」规则，目标选「通用接口 / webhook」，请求方式 `POST`，内容类型 `application/json`，
URL 填桥接地址，Body 用下面的模板（**把 `token` 换成 `smsf_devices.json` 里该手机的 token**）：

**URL**（device_no 走 query，方便区分手机）：
```
http://<桥接服务器IP>:8088/smsf_bridge.php?device_no=phone-a
```

**Body 模板**（SmsForwarder 的 `[xxx]` 是它自带占位符，发送时自动替换）：
```json
{
  "device_mark": "[device_mark]",
  "token": "bridge-token-phone-a-please-change",
  "title": "[title]",
  "msg": "[msg]",
  "receive_time": "[receive_time]",
  "timestamp": "[timestamp]",
  "package_name": "[package_name]"
}
```

> 模板里的 `token` 是**写死**的字符串（每部手机填自己的），不是 SmsForwarder 占位符。
> `device_mark` 用 SmsForwarder 占位符，会自动填成你设的设备标识（即 `device_no`），
> 这样即使忘了在 URL 里带 `?device_no=`，桥接也能从 body 里取到。

**触发条件**（SmsForwarder 的「匹配规则」）：
- 包名 = `com.tencent.mm`（微信）
- 标题或内容包含 `微信支付收款` 或 `微信收款助手`
- （支付宝同理：包名 `com.eg.android.alipaygphone`，桥接会自动识别为 `alipay` 渠道）

桥接收到后做的事：
1. 用 `device_no` 查配置拿 `secret`，校验 `token`
2. 把字段映射成 Go 的 `notificationReq`：
   - `title` ← `[title]`
   - `text` ← `[msg]`
   - `package_name` ← `[package_name]`（用来推断渠道：tencent.mm→wxpay，alipay→alipay）
   - `notify_time` ← `[timestamp]`（毫秒；若是秒级会自动 ×1000；若无则用 `[receive_time]` 解析）
   - `channel` ← 由包名推断，否则取 `default_channel`
3. 计算 `X-Signature = hex(HmacSHA256(secret, timestamp + "\n" + nonce + "\n" + body))`
4. POST 到 Go `/api/device/notifications`，透传 Go 响应给 SmsForwarder

## 四、字段映射对照表

| SmsForwarder 占位符 | 桥接 body 字段 | Go notificationReq 字段 | 说明 |
|---|---|---|---|
| `[device_mark]` | `device_mark` | `device_no` | 手机标识，决定走哪个微信号 |
| （写死的串） | `token` | — | 桥接鉴权，不转发给 Go |
| `[title]` | `title` | `title` | 通知标题，如「微信收款助手」 |
| `[msg]` | `msg` | `text` | 通知正文，金额从这里解析 |
| `[receive_time]` | `receive_time` | `notify_time` | 备用时间（秒级字符串） |
| `[timestamp]` | `timestamp` | `notify_time` | 毫秒时间戳，优先用这个 |
| `[package_name]` | `package_name` | `package_name` | 推断渠道（wxpay/alipay） |

Go 端金额解析：`parseAmount(title + " " + text)`，匹配 `收款/到账/收钱/付款 X.XX 元` 等，
所以只要 `[msg]` 里有「微信支付收款0.01元」就能解析出金额，无需额外字段。

## 五、健康检查与联调

```bash
# 桥接健康检查（列出已配置的 device_no）
curl 'http://127.0.0.1:8088/smsf_bridge.php?act=ping'

# 手动模拟一条微信到账通知（替换 device_no / token）
curl -X POST 'http://127.0.0.1:8088/smsf_bridge.php?device_no=phone-a' \
  -H 'Content-Type: application/json' \
  -d '{"device_mark":"phone-a","token":"bridge-token-phone-a-please-change",
       "title":"微信收款助手","msg":"微信支付收款0.01元(朋友到店)",
       "receive_time":"2026-06-20 16:20:00","timestamp":"1779500000000",
       "package_name":"com.tencent.mm"}'
```

期望返回：
```json
{"code":0,"match_status":"pending","msg":"ok","trade_no":""}
```
- `match_status=pending`：通知已入库，但当前没有待收款会话在等这个金额（正常，单独测通知链路时就是这样）
- `match_status=matched` + `trade_no=...`：匹配到了某个 EPay 订单，会自动回调 EPay 完成支付

验证入库：
```bash
mysql -uepay -epay123 -h127.0.0.1 epay -e \
  "SELECT id,device_no,account_id,channel,raw_text,parsed_amount,match_status
   FROM pre_nl_notification_event ORDER BY id DESC LIMIT 5"
```
`account_id` 应对应该 `device_no` 的 wxpay 账号 ID——这就是多账号按手机分流的证据。

## 六、多账号上线清单（N 部手机）

每加一个微信号，做这 4 步：

1. **Go 管理端** `view=devices` 新建设备 → 拿 `device_no` + `secret`
2. **Go 管理端** `view=accounts` 新建 `channel=wxpay` 账号，`device_no` 填上一步的
3. **`smsf_devices.json`** 加一条 `"device_no": {"secret": "...", "token": "新生成的随机串"}`
4. **该手机 SmsForwarder** 新建转发规则，URL 带 `?device_no=...`，Body 模板里 `token` 写上一步的随机串

全部启用后，Go `pickAccount` 会自动在所有启用账号间分配订单；某部手机离线/停用，
把对应 Go 账号 `status` 设为 0 即可，不影响其他手机。

## 七、安全注意

- `smsf_devices.json` 放 EPay web 根**外面**，别图省事放进去
- 生产环境务必把 `.env` 里的 `NL_DEFAULT_DEVICE_SECRET`、`NL_ADMIN_TOKEN`、
  `NL_EPAY_INTERNAL_SECRET` 都改成强随机值，并在 Go 管理端为每台手机设**独立** secret（别都用默认值）
- 桥接 token 每台手机不同，泄露一台不影响其他
- `allow_ips` 建议填 SmsForwarder 出口 IP（或多台手机 NAT 后的出口 IP）
- Go 的签名有 5 分钟时间窗 + nonce 防重放，桥接和 Go 服务器务必校准系统时间（NTP）
