CREATE TABLE import_jobs (
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 admin_id BIGINT UNSIGNED NOT NULL,
 digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 format VARCHAR(16) NOT NULL,
 status VARCHAR(32) NOT NULL,
 result_json TEXT NULL,
 error_text TEXT NULL,
 categories_json LONGTEXT NULL,
 created_at DATETIME(6) NOT NULL,
 expires_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 PRIMARY KEY (id),
 KEY idx_import_jobs_admin_created (admin_id, created_at),
 KEY idx_import_jobs_status_expires (status, expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
