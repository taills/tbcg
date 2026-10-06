package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestAdminRepositoryRejectsAccountAndRoleBindingWrites(t *testing.T) {
	fixture := newAdminACLFixture(t, "3")
	for _, check := range []struct {
		table     string
		column    string
		privilege string
	}{
		{table: "admin_users", column: "password_hash", privilege: "UPDATE"},
		{table: "admin_users", column: "username", privilege: "INSERT"},
		{table: "admin_role_bindings", column: "role", privilege: "INSERT"},
		{table: "admin_role_bindings", column: "role", privilege: "UPDATE"},
	} {
		for _, inherited := range []bool{false, true} {
			name := check.table + "/" + check.privilege + "/直接权限"
			if inherited {
				name = check.table + "/" + check.privilege + "/继承权限"
			}
			t.Run(name, func(t *testing.T) {
				fixture.grantColumn(t, check.table, check.column, check.privilege, inherited)
				assertAdminStartupRejected(t, fixture)
			})
		}
	}
	assertAdminStartupAllowed(t, fixture)
}

func TestAdminRepositoryRejectsDirectAndInheritedBuildTablePrivileges(t *testing.T) {
	fixture := newAdminACLFixture(t, "4")
	for _, check := range []struct {
		table     string
		column    string
		privilege string
	}{
		{table: "game_build_profiles", column: "asset_manifest_sha256", privilege: "SELECT"},
		{table: "admin_build_jobs", column: "input_snapshot", privilege: "SELECT"},
		{table: "admin_build_jobs", column: "status", privilege: "UPDATE"},
		{table: "release_version_counters", column: "next_release_version", privilege: "UPDATE"},
		{table: "release_entries", column: "revision", privilege: "INSERT"},
	} {
		for _, inherited := range []bool{false, true} {
			name := check.table + "/" + check.privilege + "/直接权限"
			if inherited {
				name = check.table + "/" + check.privilege + "/继承权限"
			}
			t.Run(name, func(t *testing.T) {
				fixture.grantColumn(t, check.table, check.column, check.privilege, inherited)
				assertAdminStartupRejected(t, fixture)
			})
		}
	}
	assertAdminStartupAllowed(t, fixture)
}

func TestAdminRepositoryRejectsAuditAndApprovalMutation(t *testing.T) {
	fixture := newAdminACLFixture(t, "3")
	for _, check := range []struct {
		table  string
		column string
	}{
		{table: "audit_logs", column: "details"},
		{table: "revision_approvals", column: "decision"},
	} {
		for _, inherited := range []bool{false, true} {
			name := check.table + "/UPDATE/直接权限"
			if inherited {
				name = check.table + "/UPDATE/继承权限"
			}
			t.Run(name, func(t *testing.T) {
				fixture.grantColumn(t, check.table, check.column, "UPDATE", inherited)
				assertAdminStartupRejected(t, fixture)
			})
		}
		for _, privilege := range []string{"DELETE", "TRUNCATE"} {
			t.Run(check.table+"/"+privilege, func(t *testing.T) {
				grant := privilege + " ON " + pgx.Identifier{"public", check.table}.Sanitize() + " TO " + adminControlRole
				execACLStatement(t, fixture.ctx, fixture.db, "GRANT "+grant)
				t.Cleanup(func() {
					cleanupACLStatement(t, fixture.db, "REVOKE "+privilege+" ON "+pgx.Identifier{"public", check.table}.Sanitize()+" FROM "+adminControlRole)
				})
				assertAdminStartupRejected(t, fixture)
			})
		}
	}
	assertAdminStartupAllowed(t, fixture)
}
