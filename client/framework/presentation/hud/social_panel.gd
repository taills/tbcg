extends VBoxContainer
## 好友与公会。

const UI = preload("res://framework/presentation/hud/ui.gd")
var app
var _body: VBoxContainer

func setup(owner_app) -> void:
	app = owner_app
	name = "社交"
	_body = VBoxContainer.new()
	add_child(_body)
	app.state.changed.connect(func(section): if section in ["friends", "guild"]: refresh())
	refresh()

func refresh() -> void:
	UI.clear(_body)
	var f: Dictionary = app.state.friends
	_body.add_child(UI.label("好友："))
	for friend in f.get("friends", []):
		var id := str(friend["character_id"])
		_body.add_child(UI.row([UI.label("%s %s" % [friend["name"], "在线" if friend["online"] else "离线"]), UI.button("删除", func(): app.send_and_log("friend.remove", {"character_id": id}))]))
	for request in f.get("incoming", []):
		var id := str(request["character_id"])
		_body.add_child(UI.row([UI.label("%s 请求加好友" % request["name"]), UI.button("同意", func(): app.send_and_log("friend.respond", {"character_id": id, "accept": true})),
			UI.button("拒绝", func(): app.send_and_log("friend.respond", {"character_id": id, "accept": false}))]))
	var friend_name := UI.line_edit("角色名")
	_body.add_child(UI.row([friend_name, UI.button("加好友", func(): app.send_and_log("friend.request", {"character_name": friend_name.text}))]))
	_body.add_child(HSeparator.new())
	var g: Dictionary = app.state.guild
	var guild = g.get("guild")
	if guild is Dictionary:
		_body.add_child(UI.label("公会【%s】我的职位：%s\n公告：%s" % [guild["name"], guild["my_role"], guild["notice"]]))
		for member in guild.get("members", []):
			var id := str(member["character_id"])
			var controls := [UI.label("%s（%s）%s" % [member["name"], member["role"], "在线" if member["online"] else ""])]
			if guild["my_role"] == "leader" and id != app.state.character_id():
				controls.append(UI.button("设为官员", func(): app.send_and_log("guild.set_role", {"character_id": id, "role": "officer"})))
				controls.append(UI.button("转让会长", func(): app.send_and_log("guild.set_role", {"character_id": id, "role": "leader"})))
			if guild["my_role"] != "member" and id != app.state.character_id():
				controls.append(UI.button("踢出", func(): app.send_and_log("guild.kick", {"character_id": id})))
			_body.add_child(UI.row(controls))
		for applicant in guild.get("applications", []):
			var id := str(applicant["character_id"])
			_body.add_child(UI.row([UI.label("%s 申请加入" % applicant["name"]), UI.button("批准", func(): app.send_and_log("guild.review", {"character_id": id, "accept": true})),
				UI.button("拒绝", func(): app.send_and_log("guild.review", {"character_id": id, "accept": false}))]))
		var invitee := UI.line_edit("邀请角色名")
		var notice := UI.line_edit("新公告")
		_body.add_child(UI.row([invitee, UI.button("邀请", func(): app.send_and_log("guild.invite", {"character_name": invitee.text}))]))
		_body.add_child(UI.row([notice, UI.button("更新公告", func(): app.send_and_log("guild.set_notice", {"notice": notice.text}))]))
		_body.add_child(UI.button("离开公会", func(): app.send_and_log("guild.leave", {})))
	else:
		for invite in g.get("invites", []):
			var guild_id := str(invite["guild_id"])
			if invite["kind"] == "invite":
				_body.add_child(UI.row([UI.label("公会 %s 邀请你" % invite["name"]), UI.button("加入", func(): app.send_and_log("guild.respond", {"guild_id": guild_id, "accept": true})),
					UI.button("拒绝", func(): app.send_and_log("guild.respond", {"guild_id": guild_id, "accept": false}))]))
			else:
				_body.add_child(UI.label("已申请：%s" % invite["name"]))
		var guild_name := UI.line_edit("公会名")
		_body.add_child(UI.row([guild_name, UI.button("创建（消耗货币）", func(): app.send_and_log("guild.create", {"name": guild_name.text, "idempotency_key": app.new_key()})),
			UI.button("申请加入", func(): app.send_and_log("guild.apply", {"guild_name": guild_name.text}))]))
