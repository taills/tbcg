-- 由 setup.sh 以数据库超级用户执行；每条语句都可重复运行。

-- realms 外键引用 games。
INSERT INTO games (game_id, display_name) VALUES ('harbor', '雾港奇谭') ON CONFLICT (game_id) DO NOTHING;

-- 受控构建配置：固定兼容版本，并钉住资源清单的确切字节哈希。
INSERT INTO game_build_profiles (game_id, content_schema_version, instance_schema_version, ruleset_version, protocol_version,
                                 client_min_build, asset_manifest_sha256)
VALUES ('harbor', 1, 1, 1, 1, '0.1.0', decode(:'manifest_sha', 'hex'))
ON CONFLICT (game_id) DO UPDATE
   SET asset_manifest_sha256 = EXCLUDED.asset_manifest_sha256,
       profile_version = game_build_profiles.profile_version + 1,
       updated_at = now()
 WHERE game_build_profiles.asset_manifest_sha256 IS DISTINCT FROM EXCLUDED.asset_manifest_sha256;

-- 管理服务与构建 worker 各用一个只属于对应 NOLOGIN 组的登录；框架启动时会拒绝权限过大的登录。
SELECT format('CREATE ROLE harbor_admin_app LOGIN PASSWORD %L', :'admin_db_password')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'harbor_admin_app') \gexec
SELECT format('CREATE ROLE harbor_build_worker LOGIN PASSWORD %L', :'worker_db_password')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'harbor_build_worker') \gexec
ALTER ROLE harbor_admin_app PASSWORD :'admin_db_password';
ALTER ROLE harbor_build_worker PASSWORD :'worker_db_password';
GRANT tbcg_admin_control_plane TO harbor_admin_app;
GRANT tbcg_content_build_worker TO harbor_build_worker;
