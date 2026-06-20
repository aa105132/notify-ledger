#!/usr/bin/env python3
"""smsf_post.py — 向 smsf_bridge.php 发一条伪造的 SmsForwarder 微信到账通知。

用 urllib 直接 POST，避开 shell 转义/路径/中文问题。生成唯一毫秒时间戳保证 event_id 唯一。
用法： python smsf_post.py <device_no> <token> <amount> [base_url]
       加 --bad-token 用错误 token 测桥接拒绝；加 --bad-sig 直接打 Go 测签名拒绝。
"""
import json, sys, time, urllib.request, urllib.error, hmac, hashlib, secrets

def main():
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    flags = [a for a in sys.argv[1:] if a.startswith("--")]
    device_no, token, amount = args[0], args[1], args[2]
    base = args[3] if len(args) > 3 else "http://127.0.0.1:8088/smsf_bridge.php"
    ts_ms = int(time.time() * 1000)

    if "--bad-sig" in flags:
        # 直接打 Go，用错误密钥签名，测 Go 签名拒绝
        secret = "wrong-secret"
        go_url = "http://127.0.0.1:8098/api/device/notifications"
        payload = {"device_no": device_no, "package_name": "com.tencent.mm",
                   "channel": "wxpay", "title": "微信收款助手",
                   "text": f"微信支付收款{amount}元(朋友到店)", "notify_time": ts_ms}
        body = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        ts = str(int(time.time())); nonce = secrets.token_hex(16)
        sig = hmac.new(secret.encode(), f"{ts}\n{nonce}\n".encode() + body, hashlib.sha256).hexdigest()
        req = urllib.request.Request(go_url, data=body, method="POST",
            headers={"Content-Type": "application/json", "X-Timestamp": ts,
                     "X-Nonce": nonce, "X-Signature": sig})
        print(f"POST {go_url}  (wrong-signature, expect Go 401)")
        try:
            print("-> resp =", urllib.request.urlopen(req, timeout=10).read().decode())
        except urllib.error.HTTPError as e:
            print(f"-> HTTP {e.code}: {e.read().decode()}  <- 被Go拒绝")
        return

    body = {
        "device_mark": device_no, "token": token, "title": "微信收款助手",
        "msg": f"微信支付收款{amount}元(朋友到店)",
        "receive_time": time.strftime("%Y-%m-%d %H:%M:%S"),
        "timestamp": str(ts_ms), "package_name": "com.tencent.mm",
    }
    body_json = json.dumps(body, ensure_ascii=False).encode("utf-8")
    url = f"{base}?device_no={device_no}"
    print(f"POST {url}")
    print(f"  device_no={device_no} amount={amount} notify_ms={ts_ms}")
    print(f"  body={body_json.decode()}")
    req = urllib.request.Request(url, data=body_json, method="POST",
        headers={"Content-Type": "application/json"})
    try:
        print("-> resp =", urllib.request.urlopen(req, timeout=12).read().decode())
    except urllib.error.HTTPError as e:
        print(f"-> HTTP {e.code}: {e.read().decode()}")
    except Exception as e:
        print(f"-> ERROR: {e}")

main()
