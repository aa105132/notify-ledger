-- 不凡收款管理端基础表
-- 默认表前缀按 EPay 的 pre_ 编写；如果你的 EPay 使用其他前缀，请替换 `pre_`。

CREATE TABLE IF NOT EXISTS `pre_nl_device` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `device_no` varchar(80) NOT NULL,
  `device_name` varchar(120) DEFAULT NULL,
  `secret` varchar(160) NOT NULL,
  `status` tinyint(1) NOT NULL DEFAULT 1,
  `app_version` varchar(40) DEFAULT NULL,
  `last_heartbeat_at` datetime DEFAULT NULL,
  `last_ip` varchar(64) DEFAULT NULL,
  `battery_level` int DEFAULT NULL,
  `notification_permission` tinyint(1) NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_device_no` (`device_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `pre_nl_account` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `channel` varchar(24) NOT NULL,
  `account_alias` varchar(120) NOT NULL,
  `account_identifier` varchar(120) DEFAULT NULL,
  `device_no` varchar(80) NOT NULL,
  `qrcode_url` varchar(500) DEFAULT NULL,
  `status` tinyint(1) NOT NULL DEFAULT 1,
  `daily_limit_amount` decimal(12,2) NOT NULL DEFAULT 0.00,
  `daily_received_amount` decimal(12,2) NOT NULL DEFAULT 0.00,
  `last_assigned_at` datetime DEFAULT NULL,
  `last_notify_at` datetime DEFAULT NULL,
  `remark` varchar(255) DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_channel_status` (`channel`,`status`),
  KEY `idx_device_no` (`device_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `pre_nl_collect_session` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `session_no` varchar(40) NOT NULL,
  `epay_trade_no` char(19) NOT NULL,
  `epay_out_trade_no` varchar(150) NOT NULL,
  `uid` int(11) NOT NULL,
  `channel` varchar(24) NOT NULL,
  `account_id` bigint unsigned DEFAULT NULL,
  `amount` decimal(12,2) NOT NULL,
  `status` varchar(24) NOT NULL DEFAULT 'waiting',
  `expire_at` datetime NOT NULL,
  `paid_at` datetime DEFAULT NULL,
  `notification_event_id` bigint unsigned DEFAULT NULL,
  `match_score` int NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_session_no` (`session_no`),
  UNIQUE KEY `uk_epay_trade_no` (`epay_trade_no`),
  KEY `idx_match` (`channel`,`account_id`,`amount`,`status`,`expire_at`),
  KEY `idx_uid` (`uid`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `pre_nl_notification_event` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `event_id` varchar(80) NOT NULL,
  `device_no` varchar(80) NOT NULL,
  `account_id` bigint unsigned DEFAULT NULL,
  `channel` varchar(24) NOT NULL,
  `package_name` varchar(120) DEFAULT NULL,
  `raw_title` varchar(255) DEFAULT NULL,
  `raw_text` text,
  `parsed_amount` decimal(12,2) DEFAULT NULL,
  `parsed_payer` varchar(160) DEFAULT NULL,
  `notify_time` datetime DEFAULT NULL,
  `received_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `match_status` varchar(24) NOT NULL DEFAULT 'pending',
  `matched_trade_no` char(19) DEFAULT NULL,
  `raw_payload_hash` char(64) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_event_id` (`event_id`),
  KEY `idx_match_status` (`match_status`,`received_at`),
  KEY `idx_device_channel` (`device_no`,`channel`,`received_at`),
  KEY `idx_trade_no` (`matched_trade_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `pre_nl_audit_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `actor` varchar(80) NOT NULL,
  `action` varchar(80) NOT NULL,
  `target_type` varchar(40) DEFAULT NULL,
  `target_id` varchar(80) DEFAULT NULL,
  `detail` text,
  `ip` varchar(64) DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_action_time` (`action`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `pre_nl_nonce` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `scope` varchar(40) NOT NULL,
  `nonce` varchar(80) NOT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_scope_nonce` (`scope`,`nonce`),
  KEY `idx_created_at` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `pre_nl_config` (
  `k` varchar(80) NOT NULL,
  `v` text,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`k`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


INSERT IGNORE INTO `pre_config` (`k`,`v`) VALUES
('notifyledger_server_url', 'http://127.0.0.1:8098'),
('notifyledger_internal_secret', '');
