#!/usr/bin/env python3
"""smsf_bridge_verify.py — 复现 smsf_bridge.php 的核心逻辑，验证桥接→Go 链路。

本机环境无 PHP，无法直接跑 smsf_bridge.php；此脚本读取同一个 smsf_devices.json，
按 smsf_bridge.php 完全一致的方式：校验 token、字段映射、计算 HMAC-SHA256 签名，
再 POST 到 Go 收款管理端 /api/device/notifications，确认 Go 入库并绑定到正确微信账号。

用法：
    python smsf_bridge_verify.py <device_no> <token> [amount]
示例：
    python smsf_bridge_verify.py phone-01 bridge-token-phone-01-please-change 0.05
"""
import hashlib
import hmac
import json
import secrets
import sys
import time
import urllib.request

CONFIG_PATH = r"D:\Desktop\wxzf\smsf_devices.json"


def load_config():
    with open(CONFIG_PATH, "r", encoding="utf-8") as f:
        return json.load(f)


def bridge_pick(d, keys, default=""):
    for k in keys:
        v = d.get(k)
        if v not in (None, "", 0):
            return v
    return default


def channel_from_package(pkg: str) -> str:
    p = pkg.lower()
    if "tencent.mm" in p or "wechat" in p:
        return "wxpay"
    if "alipay" in p:
        return "alipay"
    return ""


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        sys.exit(2)
    device_no = sys.argv[1]
    token = sys.argv[2]
    amount = sys.argv[3] if len(sys.argv) > 3 else "0.05"

    cfg = load_config()
    go_url = str(cfg.get("go_server_url", "http://127.0.0.1:8098")).rstrip("/")
    default_channel = str(cfg.get("default_channel", "wxpay"))
    devices = cfg.get("devices", {})

    dev = devices.get(device_no)
    if not dev:
        print(f"FAIL 未知设备: {device_no}")
        sys.exit(1)
    secret = str(dev.get("secret", ""))
    dev_token = str(dev.get("token", ""))
    if not secret or not dev_token:
        print("FAIL 设备配置不完整（缺少 secret 或 token）")
        sys.exit(1)
    # hash_equals 等价的常量时间比较
    if not hmac.compare_digest(dev_token, token):
        print("FAIL 设备 token 校验失败")
        sys.exit(1)

    # --- 模拟一条 SmsForwarder 转发过来的微信到账通知 ---
    pkg = "com.tencent.mm"
    title = "微信收款助手"
    text = f"微信支付收款{amount}元(朋友到店)"
    # 用一个递增的毫秒时间戳，保证 event_id 唯一，不与历史通知重复
    notify_ms = int(time.time() * 1000)

    smsf = {
        "device_mark": device_no,
        "token": token,
        "title": title,
        "msg": text,
        "receive_time": time.strftime("%Y-%m-%d %H:%M:%S"),
        "timestamp": str(notify_ms),
        "package_name": pkg,
    }

    # --- 字段映射: 与 smsf_bridge.php 一致 ---
    channel = channel_from_package(pkg) or str(bridge_pick(smsf, ["channel"])) or default_channel
    payload = {
        "device_no": device_no,
        "package_name": pkg,
        "channel": channel,
        "title": title,
        "text": text,
        "from": str(bridge_pick(smsf, ["from", "sender"])),
        "notify_time": notify_ms,
    }
    payload = {k: v for k, v in payload.items() if v not in ("", 0)}
    body = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")

    # --- HMAC 签名: sig = hex(HmacSHA256(secret, ts + "\n" + nonce + "\n" + body)) ---
    ts = str(int(time.time()))
    nonce = secrets.token_hex(16)
    sig = hmac.new(secret.encode("utf-8"), f"{ts}\n{nonce}\n".encode("utf-8") + body, hashlib.sha256).hexdigest()

    url = go_url + "/api/device/notifications"
    req = urllib.request.Request(
        url,
        data=body,
        method="POST",
        headers={
            "Content-Type": "application/json",
            "X-Timestamp": ts,
            "X-Nonce": nonce,
            "X-Signature": sig,
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            resp_body = resp.read().decode("utf-8")
            http_code = resp.status
    except urllib.error.HTTPError as e:
        resp_body = e.read().decode("utf-8")
        http_code = e.code
    except Exception as e:
        print(f"FAIL 转发失败: {e}")
        sys.exit(1)

    print(f"POST {url}")
    print(f"  device_no  = {device_no}")
    print(f"  channel    = {channel}")
    print(f"  amount     = {amount} 元  (text={text})")
    print(f"  notify_ms  = {notify_ms}")
    print(f"  body       = {body.decode('utf-8')}")
    print(f"  signature  = {sig}")
    print(f"-> go_http   = {http_code}")
    print(f"-> go_resp   = {resp_body}")
    try:
        rj = json.loads(resp_body)
        if rj.get("code") == 0:
            print("OK 通知已被 Go 接收入库")
            print(f"   match_status = {rj.get('match_status')}")
            print(f"   trade_no     = {rj.get('trade_no') or '(空=无待收款会话匹配，属正常)'}")
        else:
            print("FAIL Go 返回非零 code")
            sys.exit(1)
    except Exception:
        print("FAIL Go 响应非 JSON")
        sys.exit(1)


if __name__ == "__main__":
    main()
