ALTER TABLE site_settings
 ADD COLUMN english_name VARCHAR(64) NULL AFTER site_name,
 ADD COLUMN slogan VARCHAR(128) NULL AFTER english_name;
