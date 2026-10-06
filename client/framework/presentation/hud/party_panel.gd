extends VBoxContainer
## 队伍与副本：所有权限（队长、准备、离队）由服务端 Party/Dungeon owner 判定。

const UI = preload("res://framework/presentation/hud/ui.gd")
var app
var _body: VBoxContainer
var _invite_name: LineEdit
var invites: Array = []

func setup(owner_app) -> void:
	app = owner_app
	name = "队伍"
	_body = VBoxContainer.new()
	add_child(_body)
	app.state.changed.connect(func(section): if section in ["party", "dungeon"]: refresh())
	refresh()

func add_invite(data: Dictionary) -> void:
	invites.append(data)
	refresh()

func refresh() -> void:
	UI.clear(_body)
	for invite in invites:
		var party_id := str(invite["party_id"])
		_body.add_child(UI.row([UI.label("%s 邀请你入队" % invite["leader_name"]),
			UI.button("接受", func(): _respond(party_id, true)), UI.button("拒绝", func(): _respond(party_id, false))]))
	var party = app.state.party
	if party == null or not party is Dictionary or party.is_empty():
		_body.add_child(UI.button("创建队伍", func(): app.send_and_log("party.create", {})))
	else:
		_body.add_child(UI.label("队伍 %s  队长：%s  %s" % [str(party["party_id"]).left(8), app.name_of(str(party["leader_id"])), str(party.get("busy", ""))]))
		for member in party.get("members", []):
			var id := str(member["character_id"])
			var controls := [UI.label("%s %s %s" % [member["name"], "在线" if member["online"] else "离线", "已准备" if member["ready"] else ""])]
			if str(party["leader_id"]) == app.state.character_id() and id != app.state.character_id():
				controls.append(UI.button("踢出", func(): app.send_and_log("party.kick", {"character_id": id})))
				controls.append(UI.button("移交队长", func(): app.send_and_log("party.transfer", {"character_id": id})))
			_body.add_child(UI.row(controls))
		_invite_name = UI.line_edit("角色名")
		_body.add_child(UI.row([_invite_name, UI.button("邀请", func(): app.send_and_log("party.invite", {"character_name": _invite_name.text}))]))
		_body.add_child(UI.row([UI.button("准备", func(): app.send_and_log("party.ready", {"ready": true})), UI.button("取消准备", func(): app.send_and_log("party.ready", {"ready": false})),
			UI.button("离队", func(): app.send_and_log("party.leave", {}))]))
	var run = app.state.dungeon
	if run is Dictionary and not run.is_empty():
		var run_id := str(run["run_id"])
		var names: Array = run.get("stage_names", [])
		var stage := int(run["stage_index"])
		_body.add_child(UI.label("副本 %s：%s，阶段 %d/%d %s" % [app.bundle.display_name(str(run["dungeon_id"])), str(run["status"]), mini(stage + 1, names.size()), names.size(),
			names[stage] if stage < names.size() else ""]))
		if run["status"] == "active":
			_body.add_child(UI.row([UI.button("开始阶段", func(): app.send_and_log("dungeon.start_stage", {"run_id": run_id})), UI.button("离开副本", func(): app.send_and_log("dungeon.leave", {"run_id": run_id}))]))
		elif run["status"] == "completed":
			_body.add_child(UI.button("领取副本奖励", func(): app.send_and_log("dungeon.claim", {"run_id": run_id, "idempotency_key": app.new_key()})))

func _respond(party_id: String, accept: bool) -> void:
	invites = invites.filter(func(i): return str(i["party_id"]) != party_id)
	app.send_and_log("party.respond", {"party_id": party_id, "accept": accept})
	refresh()
