extends VBoxContainer
## 角色、属性、装备与背包；属性来自服务端视图，客户端不重算。

const UI = preload("res://framework/presentation/hud/ui.gd")
var app
var _body: VBoxContainer

func setup(owner_app) -> void:
	app = owner_app
	name = "角色"
	_body = VBoxContainer.new()
	add_child(_body)
	app.state.changed.connect(func(section): if section in ["character", "inventory"]: refresh())
	refresh()

func refresh() -> void:
	UI.clear(_body)
	var view: Dictionary = app.state.character
	if view.is_empty():
		return
	var s: Dictionary = view["state"]
	var stats: Dictionary = view.get("stats", {})
	_body.add_child(UI.label("%s  Lv.%d  经验 %d/%s  门派：%s" % [s["name"], int(s["level"]), int(s["exp"]), str(view.get("next_level_exp", "-")),
		app.bundle.display_name(str(s.get("sect_id", ""))) if str(s.get("sect_id", "")) != "" else "无"]))
	_body.add_child(UI.label("气血 %d/%d  法力 %d/%d  攻 %d  防 %d  速 %d" % [int(s["hp"]), int(stats.get("max_hp", 0)), int(s["mp"]), int(stats.get("max_mp", 0)),
		int(stats.get("attack", 0)), int(stats.get("defense", 0)), int(stats.get("speed", 0))]))
	_body.add_child(UI.label("技能：" + ", ".join(view.get("skills", []).map(func(id): return app.bundle.display_name(id)))))
	_body.add_child(UI.label("装备："))
	for slot in s.get("equipped", {}):
		var item_id := str(s["equipped"][slot])
		_body.add_child(UI.row([UI.label("%s：%s" % [slot, app.bundle.display_name(item_id)]), UI.button("卸下", func(): app.send_and_log("inventory.unequip", {"slot": slot}))]))
	_body.add_child(UI.label("背包（货币 %d）：" % int(app.state.inventory.get("balance", 0))))
	var items: Dictionary = app.state.inventory.get("items", {})
	for item_id in items:
		var controls := [UI.label("%s ×%d" % [app.bundle.display_name(item_id), int(items[item_id])])]
		if not app.bundle.of_type("equipment").values().filter(func(e): return e.get("item_id") == item_id).is_empty():
			controls.append(UI.button("装备", func(): app.send_and_log("inventory.equip", {"item_id": item_id})))
		if not app.bundle.of_type("consumable").values().filter(func(e): return e.get("item_id") == item_id).is_empty():
			controls.append(UI.button("使用", func(): app.send_and_log("inventory.use", {"item_id": item_id, "idempotency_key": app.new_key()})))
		_body.add_child(UI.row(controls))
