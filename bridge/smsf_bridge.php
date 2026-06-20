<?php
/**
 * smsf_bridge.php — SmsForwarder 通知桥接服务
 *
 * 作用：把 Android 端 SmsForwarder 转发过来的微信/支付宝到账通知，
 *       重新组装为 notify-ledger-server (Go 收款管理端) 期望的 notificationReq，
 *       并按设备密钥计算 HMAC-SHA256 签名后转发到 Go 的 /api/device/notifications。
 *
 * 为什么需要这个桥接：SmsForwarder 只能以 webhook 发送原始通知字段，
 *       无法计算 Go 服务端要求的 HMAC 签名（X-Timestamp / X-Nonce / X-Signature）。
 *       本桥接在中间补上签名，并对每台手机做 token 鉴权，防止伪造通知。
 *
 * 多微信账号模型：每部手机 = 一个 device_no = 一条 SmsForwarder 转发规则 = Go 里一个绑定到
 *       (device_no, channel=wxpay) 的账号。Go 的 pickAccount 会在所有启用账号间分配订单。
 *
 * 配置文件：与本项目同级目录的 ../smsf_devices.json（刻意放在 web 根目录之外，避免被下载泄露密钥）。
 *
 * SmsForwarder 推荐模板（每部手机一条规则，token 写死成该手机的桥接 token）：
 *   {"device_mark":"[device_mark]","token":"本机桥接token","title":"[title]","msg":"[msg]",
 *    "receive_time":"[receive_time]","timestamp":"[timestamp]","package_name":"[package_name]"}
 * 并在 SmsForwarder 的“设备标识”里填入该手机的 device_no（如 phone-a）。
 *
 * 调用示例：
 *   健康检查：GET  /smsf_bridge.php?act=ping
 *   通知转发：POST /smsf_bridge.php   （device_no / token 从 query 或 body 取）
 */

declare(strict_types=1);

// 本桥接只输出纯 JSON 给 SmsForwarder，任何 PHP 警告/notice 都不能泄漏进响应体，
// 否则下游解析会失败。这里强制关闭屏幕错误显示（生产 php.ini 也建议 display_errors=Off）。
error_reporting(E_ALL);
ini_set('display_errors', '0');
ini_set('log_errors', '1');

/* ------------------------------ 配置加载 ------------------------------ */

$webRoot    = __DIR__;                       // D:\Desktop\wxzf\Epay (web 根，会被服务端暴露)
$configFile = dirname($webRoot) . '/smsf_devices.json';   // 上一级目录，web 不可达
if (!is_file($configFile)) {
    http_response_code(500);
    header('Content-Type: application/json; charset=utf-8');
    echo json_encode(['code' => 500, 'msg' => '桥接配置文件不存在: smsf_devices.json'], JSON_UNESCAPED_UNICODE);
    exit;
}
$config = json_decode((string)file_get_contents($configFile), true);
if (!is_array($config)) {
    http_response_code(500);
    header('Content-Type: application/json; charset=utf-8');
    echo json_encode(['code' => 500, 'msg' => '桥接配置文件解析失败'], JSON_UNESCAPED_UNICODE);
    exit;
}

$goServerUrl    = rtrim((string)($config['go_server_url'] ?? 'http://127.0.0.1:8098'), '/');
$devices        = $config['devices'] ?? [];
$allowIps       = $config['allow_ips'] ?? [];
$defaultChannel = (string)($config['default_channel'] ?? 'wxpay');
$logFile        = (string)($config['log_file'] ?? dirname($webRoot) . '/smsf_bridge.log');
$logEnabled     = $logFile !== '';

/* ------------------------------ 工具函数 ------------------------------ */

function bridge_log(string $msg): void
{
    global $logEnabled, $logFile;
    if (!$logEnabled) {
        return;
    }
    $line = '[' . date('Y-m-d H:i:s') . '] ' . $msg . "\n";
    @file_put_contents($logFile, $line, FILE_APPEND | LOCK_EX);
}

function bridge_fail(int $code, string $msg, int $http = 400): void
{
    bridge_log("FAIL code={$code} msg={$msg}");
    http_response_code($http);
    header('Content-Type: application/json; charset=utf-8');
    echo json_encode(['code' => $code, 'msg' => $msg], JSON_UNESCAPED_UNICODE);
    exit;
}

function bridge_pick(array $a, array $keys, $def = '')
{
    foreach ($keys as $k) {
        if (isset($a[$k]) && $a[$k] !== '' && $a[$k] !== null) {
            return $a[$k];
        }
    }
    return $def;
}

function client_ip(): string
{
    $fwd = $_SERVER['HTTP_X_FORWARDED_FOR'] ?? '';
    if ($fwd !== '') {
        return trim(explode(',', $fwd)[0]);
    }
    return $_SERVER['REMOTE_ADDR'] ?? '';
}

/* ------------------------------ 健康检查 ------------------------------ */

if (($_SERVER['REQUEST_METHOD'] ?? 'GET') === 'GET' && ($_GET['act'] ?? '') === 'ping') {
    header('Content-Type: application/json; charset=utf-8');
    echo json_encode([
        'code'       => 0,
        'msg'        => 'pong',
        'go_server'  => $goServerUrl,
        'devices'    => array_keys($devices),
        'bridge_ip'  => client_ip(),
    ], JSON_UNESCAPED_UNICODE);
    exit;
}

/* ------------------------------ 仅接受 POST ------------------------------ */

if (($_SERVER['REQUEST_METHOD'] ?? 'GET') !== 'POST') {
    bridge_fail(405, '仅支持 POST 请求（健康检查请用 GET ?act=ping）', 405);
}

/* ------------------------------ 可选 IP 白名单 ------------------------------ */

if (!empty($allowIps) && is_array($allowIps)) {
    $ip = client_ip();
    if (!in_array($ip, $allowIps, true)) {
        bridge_fail(403, '来源 IP 不在白名单: ' . $ip, 403);
    }
}

/* ------------------------------ 读取 SmsForwarder 原始请求体 ------------------------------ */

$rawBody = file_get_contents('php://input');
if ($rawBody === false || $rawBody === '') {
    bridge_fail(400, '请求体为空');
}
$smsf = json_decode($rawBody, true);
if (!is_array($smsf)) {
    // 兼容个别情况下以表单方式提交的转发
    $smsf = $_POST;
    if (empty($smsf)) {
        bridge_fail(400, '请求体不是有效 JSON');
    }
}

/* ------------------------------ 解析 device_no 与 token ------------------------------ */

$deviceNo = (string)bridge_pick($_GET, ['device_no'], bridge_pick($smsf, ['device_no', 'device_mark']));
$token    = (string)bridge_pick($_GET, ['token'], bridge_pick($smsf, ['token']));

if ($deviceNo === '') {
    bridge_fail(400, '缺少 device_no（请通过 ?device_no=xxx 或 body.device_mark 提供）');
}
if (!isset($devices[$deviceNo]) || !is_array($devices[$deviceNo])) {
    bridge_fail(403, '未知设备: ' . $deviceNo, 403);
}
$dev      = $devices[$deviceNo];
$secret   = (string)($dev['secret'] ?? '');
$devToken = (string)($dev['token'] ?? '');
if ($secret === '' || $devToken === '') {
    bridge_fail(500, '设备配置不完整（缺少 secret 或 token）', 500);
}
if ($token === '') {
    bridge_fail(403, '缺少 token');
}
if (!hash_equals($devToken, $token)) {
    bridge_fail(403, '设备 token 校验失败', 403);
}

/* ------------------------------ 字段映射: SmsForwarder -> Go notificationReq ------------------------------ */

$packageName = (string)bridge_pick($smsf, ['package_name', 'packageName', 'app_package', 'pkg']);
$title       = (string)bridge_pick($smsf, ['title', 'subject']);
$text        = (string)bridge_pick($smsf, ['msg', 'text', 'content', 'body', 'message']);
$from        = (string)bridge_pick($smsf, ['from', 'sender']);

// 通知时间 -> 毫秒级时间戳
$notifyTime = 0;
$tsVal       = bridge_pick($smsf, ['timestamp', 'notify_time', 'time', 'ts']);
if (is_numeric($tsVal) && (float)$tsVal > 0) {
    $notifyTime = (int)$tsVal;
    if ($notifyTime < 1000000000000) {   // 秒级时间戳 -> 毫秒
        $notifyTime *= 1000;
    }
} else {
    $recvTime = (string)bridge_pick($smsf, ['receive_time', 'receiveTime', 'recv_time']);
    if ($recvTime !== '') {
        $dt = DateTime::createFromFormat('Y-m-d H:i:s', $recvTime)
            ?: DateTime::createFromFormat('Y/m/d H:i:s', $recvTime);
        if ($dt instanceof DateTime) {
            $notifyTime = (int)($dt->getTimestamp() * 1000);
        }
    }
}

// 渠道：优先按包名推断，其次取 body 显式 channel，最后用默认渠道
$channel = '';
if ($packageName !== '') {
    $pkgLower = strtolower($packageName);
    if (strpos($pkgLower, 'tencent.mm') !== false || strpos($pkgLower, 'wechat') !== false) {
        $channel = 'wxpay';
    } elseif (strpos($pkgLower, 'alipay') !== false) {
        $channel = 'alipay';
    }
}
if ($channel === '') {
    $channel = (string)bridge_pick($smsf, ['channel']);
}
if ($channel === '') {
    $channel = $defaultChannel;   // 兜底（默认 wxpay），避免 Go 报“未知渠道”
}

$payload = [
    'device_no'    => $deviceNo,
    'package_name' => $packageName,
    'channel'      => $channel,
    'title'        => $title,
    'text'         => $text,
    'from'         => $from,
    'notify_time'  => $notifyTime,
    // event_id 留空：Go 服务端会按 (deviceNo + pkg + title + text + notifyTime) 自动生成，并做去重
];
// 去掉空字符串 / 0 值，保持请求体干净（Go 端对这些字段均有兜底逻辑）
$payload = array_filter($payload, static function ($v) {
    return $v !== '' && $v !== 0;
});

$body = json_encode($payload, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE);
if ($body === false) {
    bridge_fail(500, 'JSON 编码失败', 500);
}

/* ------------------------------ 计算 HMAC 签名 ------------------------------ */
/* 与 Go authDevice 完全一致：
 *   sig = hex( HmacSHA256(key=secret, msg = timestamp + "\n" + nonce + "\n" + body) )
 * 头部：X-Timestamp / X-Nonce / X-Signature，timestamp 为 Unix 秒，5 分钟有效。 */

$ts    = (string)time();
$nonce = bin2hex(random_bytes(16));
$sig   = hash_hmac('sha256', $ts . "\n" . $nonce . "\n" . $body, $secret);

/* ------------------------------ 转发到 Go 收款服务端 ------------------------------ */

$url = $goServerUrl . '/api/device/notifications';
$ch  = curl_init($url);
curl_setopt_array($ch, [
    CURLOPT_POST           => true,
    CURLOPT_POSTFIELDS     => $body,
    CURLOPT_RETURNTRANSFER => true,
    CURLOPT_TIMEOUT        => 10,
    CURLOPT_CONNECTTIMEOUT => 5,
    CURLOPT_HTTPHEADER     => [
        'Content-Type: application/json',
        'X-Timestamp: ' . $ts,
        'X-Nonce: ' . $nonce,
        'X-Signature: ' . $sig,
    ],
]);
$resp     = curl_exec($ch);
$httpCode = (int)curl_getinfo($ch, CURLINFO_HTTP_CODE);
$curlErr  = curl_error($ch);
// 注：curl_close() 自 PHP 8.0 起为无操作，8.5 起被标记 deprecated（会往响应体里塞 Deprecated 警告），
// 此处仅取最后两个值后让 $ch 自然结束生命周期即可，无需显式 close。

if ($resp === false) {
    bridge_log("CURL ERROR url={$url} device={$deviceNo} err={$curlErr}");
    bridge_fail(502, '转发到收款服务失败: ' . $curlErr, 502);
}

bridge_log(sprintf(
    "OK device=%s channel=%s pkg=%s notify_ms=%d text=%s -> go_http=%d resp=%s",
    $deviceNo,
    $channel,
    $packageName,
    $notifyTime,
    mb_substr($title . ' | ' . $text, 0, 120),
    $httpCode,
    (string)$resp
));

/* ------------------------------ 透传 Go 响应给 SmsForwarder ------------------------------ */

http_response_code($httpCode >= 200 && $httpCode < 300 ? 200 : $httpCode);
header('Content-Type: application/json; charset=utf-8');
echo $resp;
