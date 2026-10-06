-- 迁移 003/004 要求部署方预先创建的 NOLOGIN 角色；仅在数据卷首次初始化时执行。
CREATE ROLE tbcg_admin_control_plane NOLOGIN;
CREATE ROLE tbcg_content_build_owner NOLOGIN;
CREATE ROLE tbcg_content_build_worker NOLOGIN;
