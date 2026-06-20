<?php
$nosession = true;
include("./includes/common.php");
@header('Content-Type: application/json; charset=UTF-8');

function nl_json($code, $msg, $http=200){
	http_response_code($http);
	exit(json_encode(['code'=>$code, 'msg'=>$msg], JSON_UNESCAPED_UNICODE));
}
function nl_callback_save_nonce($nonce){
	global $DB;
	$now = time();
	$key = 'nl_nonce_'.substr(hash('sha256', $nonce), 0, 20);
	$DB->exec("DELETE FROM pre_cache WHERE k LIKE 'nl_nonce_%' AND expire>0 AND expire<:now", [':now'=>$now]);
	return $DB->exec("INSERT INTO pre_cache (`k`,`v`,`expire`) VALUES (:k,'1',:expire)", [':k'=>$key, ':expire'=>$now + 600]) !== false;
}
function nl_callback_table_exists($name){
	global $DB;
	$table = DBQZ.'_'.$name;
	return $DB->getColumn("SHOW TABLES LIKE :table", [':table'=>$table]) ? true : false;
}

$body = file_get_contents('php://input');
$data = json_decode($body, true);
if(!$data) nl_json(-1, 'JSON格式错误', 400);

$trade_no = trim($data['trade_no'] ?? '');
$event_id = trim($data['event_id'] ?? '');
$amount = round(floatval($data['amount'] ?? 0), 2);
$buyer = trim($data['buyer_hint'] ?? '');
$paid_at = trim($data['paid_at'] ?? '');
if(empty($trade_no) || empty($event_id) || $amount <= 0) nl_json(-1, '参数不完整', 400);
if(!preg_match('/^[0-9]{19}$/', $trade_no)) nl_json(-1, '订单号格式错误', 400);

$order = $DB->getRow("SELECT A.*,B.name typename,B.showname typeshowname FROM pre_order A LEFT JOIN pre_type B ON A.type=B.id WHERE A.trade_no=:trade_no LIMIT 1", [':trade_no'=>$trade_no]);
if(!$order) nl_json(-1, '订单不存在', 404);

$userrow = $DB->find('user', 'gid,channelinfo', ['uid'=>$order['uid']]);
$groupconfig = getGroupConfig($userrow['gid']);
$conf = array_merge($conf, $groupconfig);
$channel = $order['subchannel'] > 0 ? \lib\Channel::getSub($order['subchannel']) : \lib\Channel::get($order['channel'], $userrow['channelinfo']);
if(!$channel) nl_json(-1, '支付通道不存在', 409);
$order['plugin'] = $channel['plugin'];

$secret = !empty($conf['notifyledger_internal_secret']) ? $conf['notifyledger_internal_secret'] : (!empty($channel['appkey']) ? $channel['appkey'] : SYS_KEY);
if(empty($secret)) nl_json(-1, '内部通信密钥未配置', 500);

$timestamp = $_SERVER['HTTP_X_TIMESTAMP'] ?? '';
$nonce = $_SERVER['HTTP_X_NONCE'] ?? '';
$signature = $_SERVER['HTTP_X_SIGNATURE'] ?? '';
if(empty($timestamp) || empty($nonce) || empty($signature)) nl_json(-1, '缺少签名头', 401);
if(abs(time() - intval($timestamp)) > 300) nl_json(-1, '请求已过期', 401);
$expect = hash_hmac('sha256', $timestamp."\n".$nonce."\n".$body, $secret);
if(!hash_equals($expect, $signature)) nl_json(-1, '签名错误', 401);

if(!nl_callback_save_nonce($nonce)){
	nl_json(-1, '重复 nonce', 401);
}
if(round(floatval($order['realmoney']), 2) != $amount) nl_json(-1, '金额不一致', 409);

if($order['status'] == 0){
	$api_trade_no = 'BF'.$event_id;
	$end_time = null;
	if(!empty($paid_at) && strtotime($paid_at)) $end_time = date('Y-m-d H:i:s', strtotime($paid_at));
	processNotify($order, $api_trade_no, $buyer, null, null, $end_time);
}

if(nl_callback_table_exists('nl_collect_session')){
	$DB->update('nl_collect_session', ['status'=>'paid', 'paid_at'=>'NOW()', 'updated_at'=>'NOW()'], ['epay_trade_no'=>$trade_no]);
}
nl_json(0, 'success');
