CREATE TABLE IF NOT EXISTS `groups` (
    jid VARCHAR(255) PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    folder VARCHAR(64) NOT NULL UNIQUE,
    trigger_pattern VARCHAR(255) NOT NULL,
    is_main BOOLEAN DEFAULT FALSE,
    requires_trigger BOOLEAN DEFAULT TRUE,
    container_config JSON,
    added_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS messages (
    id VARCHAR(255),
    chat_jid VARCHAR(255),
    sender VARCHAR(255),
    sender_name VARCHAR(255),
    content MEDIUMTEXT,
    timestamp TIMESTAMP(6),
    is_from_me BOOLEAN DEFAULT FALSE,
    is_bot_message BOOLEAN DEFAULT FALSE,
    PRIMARY KEY (id, chat_jid),
    INDEX idx_timestamp (timestamp),
    INDEX idx_chat_timestamp (chat_jid, timestamp)
);

CREATE TABLE IF NOT EXISTS chats (
    jid VARCHAR(255) PRIMARY KEY,
    name VARCHAR(255),
    channel VARCHAR(64),
    is_group BOOLEAN DEFAULT FALSE,
    last_message_time TIMESTAMP(6)
);

CREATE TABLE IF NOT EXISTS scheduled_tasks (
    id VARCHAR(255) PRIMARY KEY,
    group_folder VARCHAR(64) NOT NULL,
    chat_jid VARCHAR(255) NOT NULL,
    prompt MEDIUMTEXT NOT NULL,
    schedule_type ENUM('cron', 'interval', 'once') NOT NULL,
    schedule_value VARCHAR(255) NOT NULL,
    context_mode ENUM('group', 'isolated') DEFAULT 'isolated',
    next_run TIMESTAMP NULL,
    last_run TIMESTAMP NULL,
    last_result MEDIUMTEXT,
    status ENUM('active', 'paused', 'completed') DEFAULT 'active',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_next_run (next_run),
    INDEX idx_status (status)
);

CREATE TABLE IF NOT EXISTS task_run_logs (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    task_id VARCHAR(255) NOT NULL,
    run_at TIMESTAMP NOT NULL,
    duration_ms INT NOT NULL,
    status ENUM('success', 'error') NOT NULL,
    result MEDIUMTEXT,
    error MEDIUMTEXT,
    INDEX idx_task_run (task_id, run_at)
);

CREATE TABLE IF NOT EXISTS sessions (
    group_folder VARCHAR(64) PRIMARY KEY,
    session_id VARCHAR(255) NOT NULL
);

CREATE TABLE IF NOT EXISTS router_state (
    `key` VARCHAR(255) PRIMARY KEY,
    value MEDIUMTEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sender_allowlist (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    chat_jid VARCHAR(255) NOT NULL,
    allow_pattern VARCHAR(255) NOT NULL,
    mode ENUM('trigger', 'drop') NOT NULL DEFAULT 'trigger',
    UNIQUE INDEX idx_chat_pattern (chat_jid, allow_pattern)
);
