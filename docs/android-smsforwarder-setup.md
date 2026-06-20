# 安卓端 SmsForwarder 配置流程（每部手机一份）

本指南面向**拿到一部新手机、要把它接入收款监听**的运维人员。从装机开始，一步步把
[SmsForwarder](https://github.com/pppscn/SmsForwarder) 配好，让手机收到微信/支付宝到账通知后能自动
上报到收款管理端。

> 阅读前请先看一遍 [`smsforwarder-bridge.md`](smsforwarder-bridge.md) 了解整体链路：
> 手机 → PHP 桥接 → Go 服务。本指南只讲**手机这一端**怎么配。

---

## 0. 前置准备：先在服务端办好两件事

在动手机之前，让会操作服务端的人（或你自己）先把下面两件事办了，**把两个值拿在手上**，否则手机配到一半没法填：

1. **在 Go 管理端建好这台手机的设备，拿到 `device_no` 和 `secret`**
   - 打开 `http://<Go服务器>:8098/admin?token=<NL_ADMIN_TOKEN>`
   - `监听设备` → 新建设备，起一个 `device_no`（如 `phone-01`），**记下创建时只展示一次的 `secret`**
   - `收款账号` → 为这个 `device_no` 新建一个 `channel=wxpay` 的微信账号（多微信账号就多建几台手机）

2. **在 `smsf_devices.json` 里给这台手机加一条，拿到 `token`**
   - 让配桥接的人在 `devices` 里加一行：
     ```json
     "phone-01": { "secret": "<上一步 Go 给的 secret>", "token": "<新生成的随机串>" }
     ```
   - `token` 是**新生成的随机串**（生成方法见 `smsf_devices.example.json` 里的 `_how_to_generate_token`），
     每台手机一个，互不相同。

**手机端只需要两个值：`device_no`（如 `phone-01`） 和 `token`（上面那串随机串）。** 把它们记牢，下面要用。

---

## 1. 安装 SmsForwarder

SmsForwarder 是开源安卓 App，**无需 root**。

- 官方下载：https://github.com/pppscn/SmsForwarder/releases （选最新版 `SmsForwarder_xxx.apk`）
- 国内镜像：gitee 上同名仓库的 releases

装好后打开，首次启动会要求授予一堆权限，按下面顺序开。

> 建议把 SmsForwarder 加到**电池优化白名单 / 自启动白名单 / 后台运行白名单**，
> 否则国产 ROM（MIUI/EMUI/ColorOS/OriginOS 等）锁屏后会杀后台，错过到账通知。
> 具体路径各品牌不同，搜「<你的品牌> 后台运行白名单」即可。

---

## 2. 授予通知监听权限（关键）

SmsForwarder 靠「通知监听」抓微信/支付宝的到账通知，这一步**没开就什么都收不到**。

1. App 首页 → 右上角 ⚙️ 或菜单 → `通用` / `设置` → `启用转发` 打开。
2. 找到 `通知使用权` / `通知监听` → 点进去，系统会跳到「通知使用权」设置页。
3. 在列表里找到 **SmsForwarder**，打开开关。系统会弹「是否允许 SmsForwarder 读取通知」→ 允许。
4. 返回 App，首页顶部应显示「**通知监听服务：已开启**」之类的状态。

验证：让任何人给你发条微信消息，看 SmsForwarder 首页的「**消息记录**」/「**转发日志**」里有没有出现这条通知。有就说明监听通了。

> 如果列表里没有 SmsForwarder，说明系统没把它当通知监听候选——重启一次手机再开。

---

## 3. 设置设备标识（device_no）

让桥接知道这条通知来自哪部手机、对应哪个收款账号。

1. App → `通用` / `设置` → 找到 `设备标识` / `device_mark`。
2. 填入服务端给你的 `device_no`（如 `phone-01`），**严格一致**，大小写、横杠都不能错。
3. 保存。

> 这个值会通过 SmsForwarder 的 `[device_mark]` 占位符带进转发体，桥接靠它查 `smsf_devices.json`
> 找到对应的 `secret` 和 `token`。填错 → 桥接返回 `403 设备未找到`。

---

## 4. 新建转发规则（通用接口 / webhook）

SmsForwarder 把通知以 HTTP POST 发给桥接。

1. App → `转发` / `转发规则` → 右下角 `+` 新建。
2. 各字段按下表填：

| 字段 | 填什么 | 说明 |
|------|--------|------|
| `转发类型` / `发送方式` | **通用接口** / **WebHook** | 不要选短信/邮件/钉钉那些 |
| `请求方式` | `POST` | |
| `内容类型` / `Content-Type` | `application/json` | |
| `目标地址` / `WebHook URL` | `http://<桥接服务器IP>:8088/smsf_bridge.php?device_no=phone-01` | 把 IP 换成桥接服务器对外地址；`device_no=` 后面填本机的 `device_no` |
| `请求头` | 留空 | 桥接不要求额外 header |
| `请求体` / `Body` | 见下面模板 | 整段复制，**只把 `token` 改成本机的桥接 token** |

**Body 模板**（`[xxx]` 是 SmsForwarder 内置占位符，发送时自动替换；`token` 是写死的串，不要动）：

```json
{
  "device_mark": "[device_mark]",
  "token": "本机的桥接token，从smsf_devices.json里抄过来",
  "title": "[title]",
  "msg": "[msg]",
  "receive_time": "[receive_time]",
  "timestamp": "[timestamp]",
  "package_name": "[package_name]"
}
```

3. `匹配规则` / `触发条件` 先不设（先打通链路，下一步再加过滤）。保存。

> 如果你的 SmsForwarder 版本把 `设备标识` 叫 `device_mark`、把 `通用接口` 叫 `WebHook`，
> 都是同一个东西，按字面找就行。不同版本菜单名略有差异。

---

## 5. 设置触发匹配条件（只转收款通知）

不设匹配会把你微信的所有通知都发到桥接，浪费流量还可能误解析。按下面只放行到账通知：

1. 编辑上一步建的转发规则 → `匹配规则` / `触发条件`。
2. 添加条件（不同版本叫法不同，按语义找）：

**微信收款**（本方案主用）：

| 条件字段 | 匹配方式 | 值 |
|---------|---------|-----|
| `包名` / `PACKAGE_NAME` | 等于 | `com.tencent.mm` |
| `标题` 或 `内容` | 包含 | `微信支付收款` |
| `标题` 或 `内容` | 包含 | `微信收款助手` |

> 一般写成「**包名 = com.tencent.mm** 且（**内容包含 `微信支付收款`** 或 **内容包含 `微信收款助手`**）」。
> SmsForwarder 支持与/或组合，按它的规则编辑器拼即可。

**支付宝收款**（可选，桥接会自动按包名识别成 `alipay` 渠道）：

| 条件字段 | 匹配方式 | 值 |
|---------|---------|-----|
| `包名` | 等于 | `com.eg.android.alipaygphone` |
| `标题` 或 `内容` | 包含 | `收款` 或 `到账` |

3. 保存。

> 桥接靠 `package_name` 推断渠道：`com.tencent.mm` → `wxpay`，`com.eg.android.alipaygphone` → `alipay`，
> 所以这里包名一定要卡死，别用通配。Go 端金额解析会扫 `标题 + 内容` 里「收款/到账 X.XX 元」字样，
> 不用担心模板里没单独带金额字段。

---

## 6. 手机端自测（不依赖服务端）

先确认 SmsForwarder 这一段能发出 HTTP：

1. App 里转发规则通常有「**测试**」/「**发送测试**」按钮 → 点一下，看返回。
2. 或直接用真通知触发：让另一台手机给你的这个微信发一笔小额收款（0.01 元即可），看 App 的
   `转发日志` / `发送记录`。

正常日志里应有：
- 请求状态：`200`
- 响应体：`{"code":0,"match_status":"pending","msg":"ok",...}`

可能出现的报错与处理：

| 现象 | 原因 | 处理 |
|------|------|------|
| 连接超时 / `Connection refused` | 桥接服务器没跑 / IP 端口不对 / 防火墙拦 | 让服务端确认 `php -S` 或 nginx 是否在 `8088` 监听，手机能否访问该端口 |
| `403 设备未找到` 或 `403 设备 token 校验失败` | `device_no` 拼错 / `token` 与 `smsf_devices.json` 里不一致 | 核对 `device_no` 大小写横杠、`token` 是否一字不差（不要带多余空格） |
| `502` / `504` | 桥接起来了但 Go 服务没跑 | 让服务端 `curl http://<Go>:8098/healthz` 确认 |
| 日志里**没有**任何请求记录 | 通知监听权限没开 / 匹配规则太严 / 后台被杀 | 回到第 2 步重开通知使用权；暂时放宽匹配规则；加后台白名单 |

---

## 7. 端到端联调（真通知 → Go 入库）

服务端那边也通的情况下，做一次真通知验证：

1. 在 Go 管理端 `收款账号` 里确认这台 `device_no` 对应的账号 `status=启用`。
2. 让人给你的微信发一笔 **0.01 元** 收款。
3. 手机收到微信通知 → SmsForwarder 转发 → 桥接 → Go 入库。
4. 服务端查证（让有 DB 权限的人执行）：
   ```sql
   SELECT id, device_no, account_id, channel, raw_text, parsed_amount, match_status, created_at
   FROM pre_nl_notification_event
   ORDER BY id DESC LIMIT 5;
   ```
   期望看到一条 `device_no=phone-01 channel=wxpay parsed_amount=0.01` 的记录，
   `account_id` 对应该手机的 wxpay 账号 ID。

到这步，**手机端配置完成**。之后 Go 的 `pickAccount` 会自动把订单分配到启用的账号，通知一来就匹配、回写 EPay。

---

## 8. 多手机：每部都走一遍

每加一部手机，重复整套流程，**关键是不串号**：

1. 服务端 Go 管理端建一个**新** `device_no`（如 `phone-02`）+ 新 `secret` + 新 wxpay 账号。
2. `smsf_devices.json` 加一条 `phone-02`，用**新生成**的 `token`（不要复用 phone-01 的）。
3. 新手机按本指南 1~5 步配，`device_no` 填 `phone-02`，Body 里 `token` 填 phone-02 的。
4. 自测、端到端联调各跑一遍。

> 某部手机要下线，**不用动手机**：在 Go 管理端把对应账号 `status` 设为停用即可，
> 订单不会再分配给它，其他手机照常轮转。

---

## 9. 日常排错速查

| 现象 | 先查这里 |
|------|---------|
| 收了钱但 EPay 订单没自动完成 | ① 手机 SmsForwarder 是否在后台活着 ② 桥接 `smsf_bridge.log` 有无该通知 ③ Go `pre_nl_notification_event` 有无记录 ④ 该 `device_no` 账号是否启用 |
| 手机重启后不转发 | 多半被国产 ROM 杀了后台，加白名单 + 锁屏后别清后台 |
| 偶尔丢通知 | 同上，电池优化白名单没加；或微信「通知详情」被关了，去微信设置里开 |
| 同一笔被记了两次 | SmsForwarder 的「去重」没开，在转发规则里打开 `去重` / `防重复`，按 `notify_time` 去重即可（Go 端也会按 `(deviceNo+pkg+title+text+notifyTime)` 去重，双保险） |
| 换了微信号但没生效 | 微信换了账号后通知包名没变，但 Go 里绑的还是旧 `device_no`。要么在 Go 里新建账号绑定，要么把这部手机重新配 `device_no` |

---

## 附：本指南涉及的值一览（填完自己核对一遍）

| 值 | 来自哪里 | 填在手机哪里 |
|----|---------|-------------|
| `device_no`（如 `phone-01`） | Go 管理端新建设备时自定 | SmsForwarder `设备标识` + 转发规则 URL 的 `?device_no=` |
| `secret` | Go 管理端建设备时生成 | **手机端不填**，由桥接用，别带进手机 |
| `token` | `smsf_devices.json` 里该手机那条 | 转发规则 Body 模板里的 `"token": "..."` |
| 桥接 URL | 服务端告诉你 | 转发规则 `目标地址` |

四个值都对、权限都开、后台不被杀，链路就通了。
