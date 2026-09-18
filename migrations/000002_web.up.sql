CREATE TABLE admin_users (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, username VARCHAR(32) COLLATE utf8mb4_bin NOT NULL,
 password_hash VARBINARY(255) NOT NULL, enabled BOOLEAN NOT NULL DEFAULT TRUE, created_by BIGINT UNSIGNED NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
 last_login_at DATETIME(6) NULL,
 PRIMARY KEY (id), UNIQUE KEY uk_admin_users_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE submissions (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, content TEXT NOT NULL, category_id BIGINT UNSIGNED NOT NULL,
 source VARCHAR(255) NULL, author VARCHAR(128) NULL, nickname VARCHAR(32) NULL, contact VARCHAR(255) NULL,
 client_ip VARBINARY(16) NULL, content_sha256 BINARY(32) NOT NULL, status TINYINT UNSIGNED NOT NULL DEFAULT 0,
 reject_reason VARCHAR(255) NULL, reviewed_by BIGINT UNSIGNED NULL, reviewed_at DATETIME(6) NULL,
 sentence_id BIGINT UNSIGNED NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
 PRIMARY KEY (id), UNIQUE KEY uk_submissions_sentence (sentence_id),
 KEY idx_submissions_status_created (status, created_at), KEY idx_submissions_pending_hash (status, content_sha256),
 KEY idx_submissions_reviewed (status, reviewed_at),
 CONSTRAINT fk_submissions_category FOREIGN KEY (category_id) REFERENCES categories(id),
 CONSTRAINT fk_submissions_reviewer FOREIGN KEY (reviewed_by) REFERENCES admin_users(id),
 CONSTRAINT fk_submissions_sentence FOREIGN KEY (sentence_id) REFERENCES sentences(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE admin_sessions (
 token_hash BINARY(32) NOT NULL, admin_id BIGINT UNSIGNED NOT NULL, csrf_token BINARY(32) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), last_seen_at DATETIME(6) NOT NULL, expires_at DATETIME(6) NOT NULL,
 PRIMARY KEY (token_hash), KEY idx_admin_sessions_admin (admin_id), KEY idx_admin_sessions_expires (expires_at),
 CONSTRAINT fk_admin_sessions_admin FOREIGN KEY (admin_id) REFERENCES admin_users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
