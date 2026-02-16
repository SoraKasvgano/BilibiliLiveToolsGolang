package store

var schemaStatements = []string{
	`PRAGMA journal_mode=WAL;`,
	`PRAGMA synchronous=NORMAL;`,
	`PRAGMA foreign_keys=ON;`,
	`PRAGMA busy_timeout=5000;`,
	`PRAGMA temp_store=MEMORY;`,
	`CREATE TABLE IF NOT EXISTS materials (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		path TEXT NOT NULL,
		size_kb INTEGER NOT NULL DEFAULT 0,
		file_type INTEGER NOT NULL DEFAULT 0,
		description TEXT NOT NULL DEFAULT '',
		media_info TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`,
	`CREATE TABLE IF NOT EXISTS live_settings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		area_id INTEGER NOT NULL DEFAULT 0,
		room_name TEXT NOT NULL DEFAULT '',
		room_id INTEGER NOT NULL DEFAULT 0,
		content TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`,
	`CREATE TABLE IF NOT EXISTS push_settings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		model INTEGER NOT NULL DEFAULT 1,
		ffmpeg_command TEXT NOT NULL DEFAULT '',
		is_auto_retry INTEGER NOT NULL DEFAULT 1,
		retry_interval INTEGER NOT NULL DEFAULT 30,
		is_update INTEGER NOT NULL DEFAULT 0,
		input_type TEXT NOT NULL DEFAULT 'video',
		output_resolution TEXT NOT NULL DEFAULT '1280x720',
		output_quality INTEGER NOT NULL DEFAULT 2,
		output_bitrate_kbps INTEGER NOT NULL DEFAULT 0,
		custom_output_params TEXT NOT NULL DEFAULT '',
		custom_video_codec TEXT NOT NULL DEFAULT '',
		video_material_id INTEGER NULL,
		audio_material_id INTEGER NULL,
		is_mute INTEGER NOT NULL DEFAULT 0,
		input_screen TEXT NOT NULL DEFAULT '',
		input_audio_source TEXT NOT NULL DEFAULT 'file',
		input_audio_device_name TEXT NOT NULL DEFAULT '',
		input_device_name TEXT NOT NULL DEFAULT '',
		input_device_resolution TEXT NOT NULL DEFAULT '',
		input_device_framerate INTEGER NOT NULL DEFAULT 30,
		input_device_plugins TEXT NOT NULL DEFAULT '',
		rtsp_url TEXT NOT NULL DEFAULT '',
		mjpeg_url TEXT NOT NULL DEFAULT '',
		onvif_endpoint TEXT NOT NULL DEFAULT '',
		onvif_username TEXT NOT NULL DEFAULT '',
		onvif_password TEXT NOT NULL DEFAULT '',
		onvif_profile_token TEXT NOT NULL DEFAULT '',
		multi_input_enabled INTEGER NOT NULL DEFAULT 0,
		multi_input_layout TEXT NOT NULL DEFAULT '2x2',
		multi_input_urls TEXT NOT NULL DEFAULT '[]',
		multi_input_meta TEXT NOT NULL DEFAULT '[]',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(video_material_id) REFERENCES materials(id) ON DELETE SET NULL,
		FOREIGN KEY(audio_material_id) REFERENCES materials(id) ON DELETE SET NULL
	);`,
	`CREATE TABLE IF NOT EXISTS monitor_settings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		is_enabled INTEGER NOT NULL DEFAULT 0,
		room_id INTEGER NOT NULL DEFAULT 0,
		room_url TEXT NOT NULL DEFAULT '',
		is_enable_email_notice INTEGER NOT NULL DEFAULT 0,
		smtp_server TEXT NOT NULL DEFAULT '',
		smtp_ssl INTEGER NOT NULL DEFAULT 0,
		smtp_port INTEGER NOT NULL DEFAULT 25,
		mail_address TEXT NOT NULL DEFAULT '',
		mail_name TEXT NOT NULL DEFAULT '',
		password TEXT NOT NULL DEFAULT '',
		receivers TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`,
	`CREATE TABLE IF NOT EXISTS cookie_settings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		content TEXT NOT NULL DEFAULT '',
		refresh_token TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`,
	`CREATE TABLE IF NOT EXISTS danmaku_ptz_rules (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		keyword TEXT NOT NULL UNIQUE,
		action TEXT NOT NULL DEFAULT 'ptz',
		ptz_direction TEXT NOT NULL DEFAULT 'center',
		ptz_speed INTEGER NOT NULL DEFAULT 1,
		enabled INTEGER NOT NULL DEFAULT 1,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`,
	`CREATE TABLE IF NOT EXISTS webhook_settings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		url TEXT NOT NULL,
		secret TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`,
	`CREATE TABLE IF NOT EXISTS api_key_settings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		api_key TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`,
	`CREATE TABLE IF NOT EXISTS integration_tasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_type TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		priority INTEGER NOT NULL DEFAULT 100,
		payload TEXT NOT NULL DEFAULT '',
		attempt INTEGER NOT NULL DEFAULT 0,
		max_attempts INTEGER NOT NULL DEFAULT 3,
		next_run_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		locked_at DATETIME NULL,
		last_error TEXT NOT NULL DEFAULT '',
		rate_key TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		finished_at DATETIME NULL
	);`,
	`CREATE TABLE IF NOT EXISTS danmaku_consumer_settings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		enabled INTEGER NOT NULL DEFAULT 0,
		provider TEXT NOT NULL DEFAULT 'http_polling',
		endpoint TEXT NOT NULL DEFAULT '',
		auth_token TEXT NOT NULL DEFAULT '',
		config_json TEXT NOT NULL DEFAULT '{}',
		poll_interval_sec INTEGER NOT NULL DEFAULT 3,
		batch_size INTEGER NOT NULL DEFAULT 20,
		room_id INTEGER NOT NULL DEFAULT 0,
		cursor TEXT NOT NULL DEFAULT '',
		last_poll_at DATETIME NULL,
		last_error TEXT NOT NULL DEFAULT '',
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`,
	`CREATE TABLE IF NOT EXISTS integration_feature_settings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		simple_mode INTEGER NOT NULL DEFAULT 0,
		enable_danmaku_consumer INTEGER NOT NULL DEFAULT 0,
		enable_webhook INTEGER NOT NULL DEFAULT 1,
		enable_bot INTEGER NOT NULL DEFAULT 1,
		enable_advanced_stats INTEGER NOT NULL DEFAULT 1,
		enable_task_queue INTEGER NOT NULL DEFAULT 1,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`,
	`CREATE TABLE IF NOT EXISTS integration_queue_settings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		webhook_rate_gap_ms INTEGER NOT NULL DEFAULT 300,
		bot_rate_gap_ms INTEGER NOT NULL DEFAULT 300,
		max_workers INTEGER NOT NULL DEFAULT 3,
		lease_interval_ms INTEGER NOT NULL DEFAULT 500,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`,
	`CREATE TABLE IF NOT EXISTS webhook_delivery_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		webhook_id INTEGER NULL,
		webhook_name TEXT NOT NULL DEFAULT '',
		event_type TEXT NOT NULL DEFAULT '',
		request_body TEXT NOT NULL DEFAULT '',
		response_status INTEGER NOT NULL DEFAULT 0,
		response_body TEXT NOT NULL DEFAULT '',
		success INTEGER NOT NULL DEFAULT 0,
		error_message TEXT NOT NULL DEFAULT '',
		duration_ms INTEGER NOT NULL DEFAULT 0,
		attempt INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(webhook_id) REFERENCES webhook_settings(id) ON DELETE SET NULL
	);`,
	`CREATE TABLE IF NOT EXISTS bilibili_api_alert_settings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		enabled INTEGER NOT NULL DEFAULT 1,
		window_minutes INTEGER NOT NULL DEFAULT 10,
		threshold INTEGER NOT NULL DEFAULT 8,
		cooldown_minutes INTEGER NOT NULL DEFAULT 15,
		webhook_event TEXT NOT NULL DEFAULT 'bilibili.api.alert',
		last_alert_at DATETIME NULL,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`,
	`CREATE TABLE IF NOT EXISTS bilibili_api_error_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		endpoint TEXT NOT NULL DEFAULT '',
		method TEXT NOT NULL DEFAULT '',
		stage TEXT NOT NULL DEFAULT '',
		http_status INTEGER NOT NULL DEFAULT 0,
		attempt INTEGER NOT NULL DEFAULT 1,
		retryable INTEGER NOT NULL DEFAULT 0,
		request_form TEXT NOT NULL DEFAULT '',
		response_headers TEXT NOT NULL DEFAULT '',
		response_body TEXT NOT NULL DEFAULT '',
		error_message TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`,
	`CREATE TABLE IF NOT EXISTS maintenance_settings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		enabled INTEGER NOT NULL DEFAULT 1,
		retention_days INTEGER NOT NULL DEFAULT 7,
		auto_vacuum INTEGER NOT NULL DEFAULT 1,
		last_cleanup_at DATETIME NULL,
		last_vacuum_at DATETIME NULL,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`,
	`CREATE TABLE IF NOT EXISTS stream_sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		ended_at DATETIME NULL,
		status TEXT NOT NULL DEFAULT 'unknown',
		note TEXT NOT NULL DEFAULT ''
	);`,
	`CREATE TABLE IF NOT EXISTS live_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id INTEGER NULL,
		event_type TEXT NOT NULL,
		payload TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(session_id) REFERENCES stream_sessions(id) ON DELETE SET NULL
	);`,
	`CREATE TABLE IF NOT EXISTS danmaku_records (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		room_id INTEGER NOT NULL DEFAULT 0,
		uid INTEGER NOT NULL DEFAULT 0,
		uname TEXT NOT NULL DEFAULT '',
		content TEXT NOT NULL,
		raw_payload TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`,
	`CREATE TABLE IF NOT EXISTS camera_sources (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		source_type TEXT NOT NULL DEFAULT 'rtsp',
		rtsp_url TEXT NOT NULL DEFAULT '',
		mjpeg_url TEXT NOT NULL DEFAULT '',
		onvif_endpoint TEXT NOT NULL DEFAULT '',
		onvif_username TEXT NOT NULL DEFAULT '',
		onvif_password TEXT NOT NULL DEFAULT '',
		onvif_profile_token TEXT NOT NULL DEFAULT '',
		usb_device_name TEXT NOT NULL DEFAULT '',
		usb_device_resolution TEXT NOT NULL DEFAULT '1280x720',
		usb_device_framerate INTEGER NOT NULL DEFAULT 30,
		description TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`,
	`CREATE INDEX IF NOT EXISTS idx_live_events_created_at ON live_events(created_at);`,
	`CREATE INDEX IF NOT EXISTS idx_danmaku_records_created_at ON danmaku_records(created_at);`,
	`CREATE INDEX IF NOT EXISTS idx_danmaku_records_room_id ON danmaku_records(room_id);`,
	`CREATE INDEX IF NOT EXISTS idx_camera_sources_type_enabled ON camera_sources(source_type, enabled);`,
	`CREATE INDEX IF NOT EXISTS idx_camera_sources_updated_at ON camera_sources(updated_at);`,
	`CREATE INDEX IF NOT EXISTS idx_webhook_delivery_logs_created_at ON webhook_delivery_logs(created_at);`,
	`CREATE INDEX IF NOT EXISTS idx_webhook_delivery_logs_event_type ON webhook_delivery_logs(event_type);`,
	`CREATE INDEX IF NOT EXISTS idx_integration_tasks_status_next_run ON integration_tasks(status, next_run_at, id);`,
	`CREATE INDEX IF NOT EXISTS idx_integration_tasks_task_type_status ON integration_tasks(task_type, status);`,
	`CREATE INDEX IF NOT EXISTS idx_danmaku_consumer_settings_updated_at ON danmaku_consumer_settings(updated_at);`,
	`CREATE INDEX IF NOT EXISTS idx_integration_feature_settings_updated_at ON integration_feature_settings(updated_at);`,
	`CREATE INDEX IF NOT EXISTS idx_integration_queue_settings_updated_at ON integration_queue_settings(updated_at);`,
	`CREATE INDEX IF NOT EXISTS idx_bilibili_api_alert_settings_updated_at ON bilibili_api_alert_settings(updated_at);`,
	`CREATE INDEX IF NOT EXISTS idx_bilibili_api_error_logs_created_at ON bilibili_api_error_logs(created_at);`,
	`CREATE INDEX IF NOT EXISTS idx_bilibili_api_error_logs_endpoint ON bilibili_api_error_logs(endpoint);`,
	`CREATE INDEX IF NOT EXISTS idx_maintenance_settings_updated_at ON maintenance_settings(updated_at);`,
}

var seedStatements = []string{
	`INSERT INTO push_settings (
		model,
		ffmpeg_command,
		is_auto_retry,
		retry_interval,
		is_update,
		input_type,
		output_resolution,
		output_quality,
		input_device_resolution,
		input_device_framerate
	)
	SELECT 1,
	'ffmpeg -f dshow -video_size 1280x720 -i video="HD Pro Webcam C920" -vcodec libx264 -pix_fmt yuv420p -r 30 -s 1280x720 -g 60 -b:v 5000k -an -preset:v ultrafast -tune:v zerolatency -f flv {URL}',
	1,
	30,
	0,
	'video',
	'1280x720',
	2,
	'1280x720',
	30
	WHERE NOT EXISTS (SELECT 1 FROM push_settings LIMIT 1);`,
	`INSERT INTO live_settings (area_id, room_name, room_id, content)
	SELECT 0, '', 0, ''
	WHERE NOT EXISTS (SELECT 1 FROM live_settings LIMIT 1);`,
	`INSERT INTO monitor_settings (
		is_enabled,
		room_id,
		room_url,
		is_enable_email_notice,
		smtp_server,
		smtp_ssl,
		smtp_port,
		mail_address,
		mail_name,
		password,
		receivers
	)
	SELECT 0, 0, '', 0, '', 0, 25, '', '', '', ''
	WHERE NOT EXISTS (SELECT 1 FROM monitor_settings LIMIT 1);`,
	`INSERT INTO cookie_settings (content, refresh_token)
	SELECT '', ''
	WHERE NOT EXISTS (SELECT 1 FROM cookie_settings LIMIT 1);`,
	`INSERT INTO api_key_settings (name, api_key, description)
	SELECT 'dingtalk', '', 'Plain text API key for DingTalk robot'
	WHERE NOT EXISTS (SELECT 1 FROM api_key_settings WHERE name = 'dingtalk');`,
	`INSERT INTO api_key_settings (name, api_key, description)
	SELECT 'telegram', '', 'Plain text API key for Telegram bot'
	WHERE NOT EXISTS (SELECT 1 FROM api_key_settings WHERE name = 'telegram');`,
	`INSERT INTO api_key_settings (name, api_key, description)
	SELECT 'bilibili', '', 'Plain text API key for future Bilibili automation'
	WHERE NOT EXISTS (SELECT 1 FROM api_key_settings WHERE name = 'bilibili');`,
	`INSERT INTO api_key_settings (name, api_key, description)
	SELECT 'pushoo', '', 'Pushoo service base url (for example: http://127.0.0.1:8084/send)'
	WHERE NOT EXISTS (SELECT 1 FROM api_key_settings WHERE name = 'pushoo');`,
	`INSERT INTO api_key_settings (name, api_key, description)
	SELECT 'pushoo_token', '', 'Optional token for pushoo /send api'
	WHERE NOT EXISTS (SELECT 1 FROM api_key_settings WHERE name = 'pushoo_token');`,
	`INSERT INTO api_key_settings (name, api_key, description)
	SELECT 'provider_inbound_secret', '', 'Shared HMAC secret for /integration/provider/inbound/{provider}'
	WHERE NOT EXISTS (SELECT 1 FROM api_key_settings WHERE name = 'provider_inbound_secret');`,
	`INSERT INTO api_key_settings (name, api_key, description)
	SELECT 'provider_inbound_whitelist', '', 'Optional inbound source IP whitelist, supports CIDR and comma-separated IPs'
	WHERE NOT EXISTS (SELECT 1 FROM api_key_settings WHERE name = 'provider_inbound_whitelist');`,
	`INSERT INTO api_key_settings (name, api_key, description)
	SELECT 'telegram_inbound_secret', '', 'HMAC secret for telegram inbound webhook (optional override)'
	WHERE NOT EXISTS (SELECT 1 FROM api_key_settings WHERE name = 'telegram_inbound_secret');`,
	`INSERT INTO api_key_settings (name, api_key, description)
	SELECT 'telegram_inbound_secret_token', '', 'Official Telegram webhook secret token (X-Telegram-Bot-Api-Secret-Token)'
	WHERE NOT EXISTS (SELECT 1 FROM api_key_settings WHERE name = 'telegram_inbound_secret_token');`,
	`INSERT INTO api_key_settings (name, api_key, description)
	SELECT 'dingtalk_inbound_secret', '', 'HMAC secret for dingtalk inbound webhook (optional override)'
	WHERE NOT EXISTS (SELECT 1 FROM api_key_settings WHERE name = 'dingtalk_inbound_secret');`,
	`INSERT INTO api_key_settings (name, api_key, description)
	SELECT 'dingtalk_inbound_sign_secret', '', 'Official DingTalk sign secret for timestamp+sign verification'
	WHERE NOT EXISTS (SELECT 1 FROM api_key_settings WHERE name = 'dingtalk_inbound_sign_secret');`,
	`INSERT INTO api_key_settings (name, api_key, description)
	SELECT 'dingtalk_callback_token', '', 'Official DingTalk callback token for msg_signature verification'
	WHERE NOT EXISTS (SELECT 1 FROM api_key_settings WHERE name = 'dingtalk_callback_token');`,
	`INSERT INTO api_key_settings (name, api_key, description)
	SELECT 'provider_inbound_skew_sec', '300', 'Allowed timestamp skew (seconds) for inbound webhook signature'
	WHERE NOT EXISTS (SELECT 1 FROM api_key_settings WHERE name = 'provider_inbound_skew_sec');`,
	`INSERT INTO danmaku_consumer_settings (enabled, provider, endpoint, auth_token, config_json, poll_interval_sec, batch_size, room_id, cursor, last_error)
	SELECT 0, 'http_polling', '', '', '{}', 3, 20, 0, '', ''
	WHERE NOT EXISTS (SELECT 1 FROM danmaku_consumer_settings LIMIT 1);`,
	`INSERT INTO integration_feature_settings (
		simple_mode, enable_danmaku_consumer, enable_webhook, enable_bot, enable_advanced_stats, enable_task_queue, updated_at
	) SELECT 0, 0, 1, 1, 1, 1, CURRENT_TIMESTAMP
	WHERE NOT EXISTS (SELECT 1 FROM integration_feature_settings LIMIT 1);`,
	`INSERT INTO integration_queue_settings (
		webhook_rate_gap_ms, bot_rate_gap_ms, max_workers, lease_interval_ms, updated_at
	) SELECT 300, 300, 3, 500, CURRENT_TIMESTAMP
	WHERE NOT EXISTS (SELECT 1 FROM integration_queue_settings LIMIT 1);`,
	`INSERT INTO bilibili_api_alert_settings (enabled, window_minutes, threshold, cooldown_minutes, webhook_event)
	SELECT 1, 10, 8, 15, 'bilibili.api.alert'
	WHERE NOT EXISTS (SELECT 1 FROM bilibili_api_alert_settings LIMIT 1);`,
	`INSERT INTO maintenance_settings (enabled, retention_days, auto_vacuum)
	SELECT 1, 7, 1
	WHERE NOT EXISTS (SELECT 1 FROM maintenance_settings LIMIT 1);`,
}
