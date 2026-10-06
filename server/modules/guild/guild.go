// Package guild 是公会 owner（任务 0015.7）：创建成本、成员关系、职位权限、申请/邀请与公告。
// 每次变更锁定公会行并递增 state_version；管理端不能绕过此 owner 修改成员或资产。
package guild

import (
	"context"
	"database/sql"
	"errors"
	"unicode/utf8"

	rt "example.com/tbcg/server/modules/runtime"
	"github.com/jackc/pgx/v5/pgconn"
)

func init() {
	rt.Register("guild.info", true, info)
	rt.Register("guild.create", true, create)
	rt.Register("guild.apply", true, apply)
	rt.Register("guild.invite", true, invite)
	rt.Register("guild.respond", true, respond)
	rt.Register("guild.review", true, review)
	rt.Register("guild.leave", true, leave)
	rt.Register("guild.kick", true, kick)
	rt.Register("guild.set_role", true, setRole)
	rt.Register("guild.set_notice", true, setNotice)
	rt.RegisterSnapshot("guild", func(ctx context.Context, r *rt.Realm, p *rt.Player) (any, error) { return View(ctx, r, p.CharacterID) })
}

type membership struct {
	GuildID string
	Role    string
}

func memberOf(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, r *rt.Realm, characterID string) (*membership, error) {
	var m membership
	err := q.QueryRowContext(ctx, `SELECT guild_id::text, role FROM guild_members WHERE game_id=$1 AND realm_id=$2 AND character_id=$3`, r.GameID, r.RealmID, characterID).Scan(&m.GuildID, &m.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &m, err
}

// View 返回角色所属公会（以及待处理邀请）。
func View(ctx context.Context, r *rt.Realm, characterID string) (map[string]any, error) {
	m, err := memberOf(ctx, r.DB, r, characterID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		invites := make([]map[string]any, 0)
		rows, err := r.DB.QueryContext(ctx, `SELECT i.guild_id::text, g.name, i.kind FROM guild_invites i JOIN guilds g USING (game_id, realm_id, guild_id)
WHERE i.game_id=$1 AND i.realm_id=$2 AND i.character_id=$3`, r.GameID, r.RealmID, characterID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id, name, kind string
			if err := rows.Scan(&id, &name, &kind); err != nil {
				return nil, err
			}
			invites = append(invites, map[string]any{"guild_id": id, "name": name, "kind": kind})
		}
		return map[string]any{"guild": nil, "invites": invites}, rows.Err()
	}
	var name, leader, notice string
	var version int64
	if err := r.DB.QueryRowContext(ctx, `SELECT name, leader_id, notice, state_version FROM guilds WHERE game_id=$1 AND realm_id=$2 AND guild_id=$3::uuid`,
		r.GameID, r.RealmID, m.GuildID).Scan(&name, &leader, &notice, &version); err != nil {
		return nil, err
	}
	members := make([]map[string]any, 0)
	rows, err := r.DB.QueryContext(ctx, `SELECT character_id, role FROM guild_members WHERE game_id=$1 AND realm_id=$2 AND guild_id=$3::uuid ORDER BY joined_at`, r.GameID, r.RealmID, m.GuildID)
	if err != nil {
		return nil, err
	}
	var ids, roles []string
	for rows.Next() {
		var id, role string
		if err := rows.Scan(&id, &role); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids, roles = append(ids, id), append(roles, role)
	}
	_ = rows.Close()
	for i, id := range ids {
		members = append(members, map[string]any{"character_id": id, "name": r.CharacterName(ctx, id), "role": roles[i], "online": r.Player(id) != nil})
	}
	applications := make([]map[string]any, 0)
	if m.Role != "member" {
		rows, err := r.DB.QueryContext(ctx, `SELECT character_id FROM guild_invites WHERE game_id=$1 AND realm_id=$2 AND guild_id=$3::uuid AND kind='application'`, r.GameID, r.RealmID, m.GuildID)
		if err != nil {
			return nil, err
		}
		var applicants []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, err
			}
			applicants = append(applicants, id)
		}
		_ = rows.Close()
		for _, id := range applicants {
			applications = append(applications, map[string]any{"character_id": id, "name": r.CharacterName(ctx, id)})
		}
	}
	return map[string]any{"guild": map[string]any{"guild_id": m.GuildID, "name": name, "leader_id": leader, "notice": notice, "state_version": version,
		"my_role": m.Role, "members": members, "applications": applications}}, nil
}

func notifyGuild(tx *rt.Tx, guildID string, extra ...string) {
	tx.After(func() {
		ctx := context.Background()
		rows, err := tx.Realm.DB.QueryContext(ctx, `SELECT character_id FROM guild_members WHERE game_id=$1 AND realm_id=$2 AND guild_id=$3::uuid`, tx.Realm.GameID, tx.Realm.RealmID, guildID)
		if err != nil {
			return
		}
		ids := append([]string(nil), extra...)
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		_ = rows.Close()
		for _, id := range ids {
			if view, err := View(ctx, tx.Realm, id); err == nil {
				tx.Realm.Push(id, "guild.updated", view)
			}
		}
	})
}

func lockGuild(tx *rt.Tx, guildID string) (int64, error) {
	if !rt.ValidUUID(guildID) {
		return 0, rt.Fail("guild_not_found", "公会不存在")
	}
	var version int64
	err := tx.QueryRowContext(tx.Ctx, `SELECT state_version FROM guilds WHERE game_id=$1 AND realm_id=$2 AND guild_id=$3::uuid FOR UPDATE`, tx.Realm.GameID, tx.Realm.RealmID, guildID).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, rt.Fail("guild_not_found", "公会不存在")
	}
	return version, err
}

func bump(tx *rt.Tx, guildID string) error {
	_, err := tx.ExecContext(tx.Ctx, `UPDATE guilds SET state_version=state_version+1 WHERE game_id=$1 AND realm_id=$2 AND guild_id=$3::uuid`, tx.Realm.GameID, tx.Realm.RealmID, guildID)
	return err
}

func info(c *rt.Ctx) (any, *rt.ProtocolError) {
	view, err := View(c.Context, c.Realm, c.Player.CharacterID)
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return view, nil
}

func create(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		Name           string `json:"name"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	rules := c.Content().Guild
	if rules == nil {
		return nil, rt.Fail("feature_disabled", "当前 Release 未开放公会")
	}
	if count := utf8.RuneCountInString(input.Name); count < 2 || count > 24 {
		return nil, rt.Fail("invalid_name", "公会名需 2–24 个字符")
	}
	if c.Player.State.Level < rules.Public.CreateMinLevel {
		return nil, rt.Fail("level_too_low", "等级不足，不能创建公会")
	}
	me := c.Player.CharacterID
	result, _, err := c.Realm.Operation(c.Context, me, "guild.create", input.IdempotencyKey, input.Name, func(tx *rt.Tx) (any, error) {
		if m, err := memberOf(tx.Ctx, tx.Tx, tx.Realm, me); err != nil || m != nil {
			if err != nil {
				return nil, err
			}
			return nil, rt.Fail("already_in_guild", "已经加入公会")
		}
		if rules.Public.CreateCost > 0 {
			if _, err := tx.AdjustCurrency(me, -rules.Public.CreateCost, "guild_create"); err != nil {
				return nil, err
			}
		}
		guildID := rt.NewUUID()
		if _, err := tx.ExecContext(tx.Ctx, `INSERT INTO guilds (game_id, realm_id, guild_id, name, leader_id) VALUES ($1,$2,$3::uuid,$4,$5)`, tx.Realm.GameID, tx.Realm.RealmID, guildID, input.Name, me); err != nil {
			if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" {
				return nil, rt.Fail("name_taken", "公会名已被占用")
			}
			return nil, err
		}
		if _, err := tx.ExecContext(tx.Ctx, `INSERT INTO guild_members (game_id, realm_id, character_id, guild_id, role) VALUES ($1,$2,$3,$4::uuid,'leader')`, tx.Realm.GameID, tx.Realm.RealmID, me, guildID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(tx.Ctx, `DELETE FROM guild_invites WHERE game_id=$1 AND realm_id=$2 AND character_id=$3`, tx.Realm.GameID, tx.Realm.RealmID, me); err != nil {
			return nil, err
		}
		if err := tx.Emit("guild", "guild.created", map[string]any{"guild_id": guildID, "leader_id": me, "cost": rules.Public.CreateCost}); err != nil {
			return nil, err
		}
		notifyGuild(tx, guildID)
		return map[string]any{"guild_id": guildID, "name": input.Name}, nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return rt.RawJSON(result), nil
}

func addMember(tx *rt.Tx, guildID, characterID string, maxMembers int) error {
	if m, err := memberOf(tx.Ctx, tx.Tx, tx.Realm, characterID); err != nil || m != nil {
		if err != nil {
			return err
		}
		return rt.Fail("already_in_guild", "目标已加入公会")
	}
	var count int
	if err := tx.QueryRowContext(tx.Ctx, `SELECT count(*) FROM guild_members WHERE game_id=$1 AND realm_id=$2 AND guild_id=$3::uuid`, tx.Realm.GameID, tx.Realm.RealmID, guildID).Scan(&count); err != nil {
		return err
	}
	if count >= maxMembers {
		return rt.Fail("guild_full", "公会人数已满")
	}
	if _, err := tx.ExecContext(tx.Ctx, `INSERT INTO guild_members (game_id, realm_id, character_id, guild_id, role) VALUES ($1,$2,$3,$4::uuid,'member')`, tx.Realm.GameID, tx.Realm.RealmID, characterID, guildID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(tx.Ctx, `DELETE FROM guild_invites WHERE game_id=$1 AND realm_id=$2 AND character_id=$3`, tx.Realm.GameID, tx.Realm.RealmID, characterID); err != nil {
		return err
	}
	return bump(tx, guildID)
}

func apply(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		GuildName string `json:"guild_name"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	me := c.Player.CharacterID
	var guildID string
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		if err := tx.QueryRowContext(tx.Ctx, `SELECT guild_id::text FROM guilds WHERE game_id=$1 AND realm_id=$2 AND name=$3`, tx.Realm.GameID, tx.Realm.RealmID, input.GuildName).Scan(&guildID); err != nil {
			return rt.Fail("guild_not_found", "公会不存在")
		}
		if m, err := memberOf(tx.Ctx, tx.Tx, tx.Realm, me); err != nil || m != nil {
			if err != nil {
				return err
			}
			return rt.Fail("already_in_guild", "已经加入公会")
		}
		if _, err := tx.ExecContext(tx.Ctx, `INSERT INTO guild_invites (game_id, realm_id, guild_id, character_id, kind) VALUES ($1,$2,$3::uuid,$4,'application') ON CONFLICT DO NOTHING`,
			tx.Realm.GameID, tx.Realm.RealmID, guildID, me); err != nil {
			return err
		}
		notifyGuild(tx, guildID, me)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"guild_id": guildID, "applied": true}, nil
}

func requireOfficer(tx *rt.Tx, characterID string, allowOfficer bool) (*membership, error) {
	m, err := memberOf(tx.Ctx, tx.Tx, tx.Realm, characterID)
	if err != nil {
		return nil, err
	}
	if m == nil || (m.Role != "leader" && !(allowOfficer && m.Role == "officer")) {
		return nil, rt.Fail("forbidden", "职位权限不足")
	}
	if _, err := lockGuild(tx, m.GuildID); err != nil {
		return nil, err
	}
	return m, nil
}

func invite(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		CharacterName string `json:"character_name"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	rules := c.Content().Guild
	if rules == nil {
		return nil, rt.Fail("feature_disabled", "当前 Release 未开放公会")
	}
	target, ok := c.Realm.CharacterIDByName(c.Context, input.CharacterName)
	if !ok {
		return nil, rt.Fail("invalid_target", "目标角色不存在")
	}
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		m, err := requireOfficer(tx, c.Player.CharacterID, rules.Server.OfficerCanInvite)
		if err != nil {
			return err
		}
		if existing, err := memberOf(tx.Ctx, tx.Tx, tx.Realm, target); err != nil || existing != nil {
			if err != nil {
				return err
			}
			return rt.Fail("already_in_guild", "目标已加入公会")
		}
		if _, err := tx.ExecContext(tx.Ctx, `INSERT INTO guild_invites (game_id, realm_id, guild_id, character_id, kind) VALUES ($1,$2,$3::uuid,$4,'invite')
ON CONFLICT (game_id, realm_id, guild_id, character_id) DO UPDATE SET kind='invite'`, tx.Realm.GameID, tx.Realm.RealmID, m.GuildID, target); err != nil {
			return err
		}
		notifyGuild(tx, m.GuildID, target)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"invited": target}, nil
}

func respond(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		GuildID string `json:"guild_id"`
		Accept  bool   `json:"accept"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	rules := c.Content().Guild
	if rules == nil {
		return nil, rt.Fail("feature_disabled", "当前 Release 未开放公会")
	}
	me := c.Player.CharacterID
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		if _, err := lockGuild(tx, input.GuildID); err != nil {
			return err
		}
		result, err := tx.ExecContext(tx.Ctx, `DELETE FROM guild_invites WHERE game_id=$1 AND realm_id=$2 AND guild_id=$3::uuid AND character_id=$4 AND kind='invite'`, tx.Realm.GameID, tx.Realm.RealmID, input.GuildID, me)
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows == 0 {
			return rt.Fail("no_invite", "没有该公会的邀请")
		}
		if input.Accept {
			if err := addMember(tx, input.GuildID, me, rules.Public.MaxMembers); err != nil {
				return err
			}
		}
		notifyGuild(tx, input.GuildID, me)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"guild_id": input.GuildID, "joined": input.Accept}, nil
}

func review(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		CharacterID string `json:"character_id"`
		Accept      bool   `json:"accept"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	rules := c.Content().Guild
	if rules == nil {
		return nil, rt.Fail("feature_disabled", "当前 Release 未开放公会")
	}
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		m, err := requireOfficer(tx, c.Player.CharacterID, rules.Server.OfficerCanInvite)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(tx.Ctx, `DELETE FROM guild_invites WHERE game_id=$1 AND realm_id=$2 AND guild_id=$3::uuid AND character_id=$4 AND kind='application'`, tx.Realm.GameID, tx.Realm.RealmID, m.GuildID, input.CharacterID)
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows == 0 {
			return rt.Fail("no_application", "没有该申请")
		}
		if input.Accept {
			if err := addMember(tx, m.GuildID, input.CharacterID, rules.Public.MaxMembers); err != nil {
				return err
			}
		}
		notifyGuild(tx, m.GuildID, input.CharacterID)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"character_id": input.CharacterID, "accepted": input.Accept}, nil
}

func leave(c *rt.Ctx) (any, *rt.ProtocolError) {
	me := c.Player.CharacterID
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		m, err := memberOf(tx.Ctx, tx.Tx, tx.Realm, me)
		if err != nil {
			return err
		}
		if m == nil {
			return rt.Fail("not_in_guild", "未加入公会")
		}
		if _, err := lockGuild(tx, m.GuildID); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(tx.Ctx, `SELECT count(*) FROM guild_members WHERE game_id=$1 AND realm_id=$2 AND guild_id=$3::uuid`, tx.Realm.GameID, tx.Realm.RealmID, m.GuildID).Scan(&count); err != nil {
			return err
		}
		if m.Role == "leader" && count > 1 {
			return rt.Fail("leader_must_transfer", "会长需先转让职位才能离开")
		}
		if _, err := tx.ExecContext(tx.Ctx, `DELETE FROM guild_members WHERE game_id=$1 AND realm_id=$2 AND character_id=$3`, tx.Realm.GameID, tx.Realm.RealmID, me); err != nil {
			return err
		}
		if count == 1 {
			if _, err := tx.ExecContext(tx.Ctx, `DELETE FROM guilds WHERE game_id=$1 AND realm_id=$2 AND guild_id=$3::uuid`, tx.Realm.GameID, tx.Realm.RealmID, m.GuildID); err != nil {
				return err
			}
		} else if err := bump(tx, m.GuildID); err != nil {
			return err
		}
		notifyGuild(tx, m.GuildID, me)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"left": true}, nil
}

func kick(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		CharacterID string `json:"character_id"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	rules := c.Content().Guild
	if rules == nil {
		return nil, rt.Fail("feature_disabled", "当前 Release 未开放公会")
	}
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		m, err := requireOfficer(tx, c.Player.CharacterID, rules.Server.OfficerCanKick)
		if err != nil {
			return err
		}
		target, err := memberOf(tx.Ctx, tx.Tx, tx.Realm, input.CharacterID)
		if err != nil {
			return err
		}
		if target == nil || target.GuildID != m.GuildID || target.Role == "leader" || (m.Role == "officer" && target.Role != "member") {
			return rt.Fail("forbidden", "不能踢出该成员")
		}
		if _, err := tx.ExecContext(tx.Ctx, `DELETE FROM guild_members WHERE game_id=$1 AND realm_id=$2 AND character_id=$3`, tx.Realm.GameID, tx.Realm.RealmID, input.CharacterID); err != nil {
			return err
		}
		if err := bump(tx, m.GuildID); err != nil {
			return err
		}
		notifyGuild(tx, m.GuildID, input.CharacterID)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"kicked": input.CharacterID}, nil
}

func setRole(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		CharacterID string `json:"character_id"`
		Role        string `json:"role"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	if input.Role != "leader" && input.Role != "officer" && input.Role != "member" {
		return nil, rt.Fail("invalid_role", "职位无效")
	}
	me := c.Player.CharacterID
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		m, err := requireOfficer(tx, me, false)
		if err != nil {
			return err
		}
		target, err := memberOf(tx.Ctx, tx.Tx, tx.Realm, input.CharacterID)
		if err != nil {
			return err
		}
		if target == nil || target.GuildID != m.GuildID || input.CharacterID == me {
			return rt.Fail("not_member", "目标不是本公会成员")
		}
		if input.Role == "leader" {
			if _, err := tx.ExecContext(tx.Ctx, `UPDATE guild_members SET role='officer' WHERE game_id=$1 AND realm_id=$2 AND character_id=$3`, tx.Realm.GameID, tx.Realm.RealmID, me); err != nil {
				return err
			}
			if _, err := tx.ExecContext(tx.Ctx, `UPDATE guilds SET leader_id=$4 WHERE game_id=$1 AND realm_id=$2 AND guild_id=$3::uuid`, tx.Realm.GameID, tx.Realm.RealmID, m.GuildID, input.CharacterID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(tx.Ctx, `UPDATE guild_members SET role=$4 WHERE game_id=$1 AND realm_id=$2 AND character_id=$3`, tx.Realm.GameID, tx.Realm.RealmID, input.CharacterID, input.Role); err != nil {
			return err
		}
		if err := bump(tx, m.GuildID); err != nil {
			return err
		}
		notifyGuild(tx, m.GuildID)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"character_id": input.CharacterID, "role": input.Role}, nil
}

func setNotice(c *rt.Ctx) (any, *rt.ProtocolError) {
	var input struct {
		Notice string `json:"notice"`
	}
	if failure := c.Decode(&input); failure != nil {
		return nil, failure
	}
	rules := c.Content().Guild
	if rules == nil {
		return nil, rt.Fail("feature_disabled", "当前 Release 未开放公会")
	}
	if utf8.RuneCountInString(input.Notice) > rules.Public.NoticeMaxLength {
		return nil, rt.Fail("notice_too_long", "公告过长")
	}
	err := c.Realm.Tx(c.Context, func(tx *rt.Tx) error {
		m, err := requireOfficer(tx, c.Player.CharacterID, true)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(tx.Ctx, `UPDATE guilds SET notice=$4, state_version=state_version+1 WHERE game_id=$1 AND realm_id=$2 AND guild_id=$3::uuid`, tx.Realm.GameID, tx.Realm.RealmID, m.GuildID, input.Notice); err != nil {
			return err
		}
		notifyGuild(tx, m.GuildID)
		return nil
	})
	if err != nil {
		return nil, rt.FailFromError(err)
	}
	return map[string]any{"notice": input.Notice}, nil
}
