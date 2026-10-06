extends PanelContainer
## 战斗界面：显示服务端单位状态、提交本回合意图，并按 event_seq 回放权威事件。

const BattleReplay = preload("res://framework/state/battle_replay.gd")

var app
var battle_id := ""
var round := 0
var my_unit := ""
var units: Array = []
var replay = BattleReplay.new()
var _units_box: VBoxContainer
var _log: RichTextLabel
var _target: OptionButton
var _skill: OptionButton
var _item: OptionButton
var _status: Label

func setup(owner_app) -> void:
	app = owner_app
	custom_minimum_size = Vector2(560, 420)
	var root := VBoxContainer.new()
	add_child(root)
	_status = Label.new()
	root.add_child(_status)
	_units_box = VBoxContainer.new()
	root.add_child(_units_box)
	var row := HBoxContainer.new()
	root.add_child(row)
	_target = OptionButton.new()
	_skill = OptionButton.new()
	_item = OptionButton.new()
	row.add_child(_target)
	row.add_child(_skill)
	row.add_child(_item)
	var actions := HBoxContainer.new()
	root.add_child(actions)
	for spec in [["攻击", "attack"], ["技能", "skill"], ["防御", "defend"], ["物品", "item"], ["逃跑", "flee"]]:
		var button := Button.new()
		button.text = spec[0]
		button.pressed.connect(_submit.bind(spec[1]))
		actions.add_child(button)
	_log = RichTextLabel.new()
	_log.custom_minimum_size = Vector2(540, 180)
	_log.scroll_following = true
	root.add_child(_log)
	hide()

func start(data: Dictionary) -> void:
	battle_id = str(data["battle_id"])
	round = int(data["round"])
	my_unit = str(data["your_unit_id"])
	units = data["units"]
	replay = BattleReplay.new()
	replay.load_units(units)
	_log.clear()
	_log.append_text("遭遇：%s\n" % app.bundle.display_name(str(data["encounter_id"])))
	_refresh_options(data.get("inventory", {}))
	_render()
	show()

func on_round(data: Dictionary) -> void:
	if str(data.get("battle_id", "")) != battle_id:
		return
	if data.has("events"):
		var before: int = replay.lines.size()
		replay.apply_events(data["events"])
		for i in range(before, replay.lines.size()):
			_log.append_text(replay.lines[i] + "\n")
		if replay.desynced:
			_log.append_text("[客户端失步，已以服务端单位状态为准]\n")
		units = data.get("units", units)
		replay.load_units(units)
		round = int(data.get("round", round))
		_refresh_options({})
	_render()

func on_end(data: Dictionary) -> void:
	if str(data.get("battle_id", "")) != battle_id:
		return
	_log.append_text("结果：%s\n" % str(data.get("outcome", "")))
	await get_tree().create_timer(1.5).timeout
	hide()
	battle_id = ""

func _refresh_options(inventory: Dictionary) -> void:
	_target.clear()
	for unit in units:
		if int(unit["hp"]) > 0:
			_target.add_item("%s（%s）" % [unit["name"], "敌" if int(unit["side"]) == 1 else "友"])
			_target.set_item_metadata(_target.item_count - 1, unit["id"])
	_skill.clear()
	for unit in units:
		if unit["id"] == my_unit:
			for skill_id in unit.get("skill_ids", []):
				_skill.add_item(app.bundle.display_name(skill_id))
				_skill.set_item_metadata(_skill.item_count - 1, skill_id)
	if not inventory.is_empty():
		_item.clear()
		for item_id in inventory:
			_item.add_item("%s ×%d" % [app.bundle.display_name(item_id), int(inventory[item_id])])
			_item.set_item_metadata(_item.item_count - 1, item_id)

func _render() -> void:
	for child in _units_box.get_children():
		child.queue_free()
	_status.text = "第 %d 回合" % round
	for id in replay.units:
		var unit: Dictionary = replay.units[id]
		var bar := ProgressBar.new()
		bar.max_value = max(1, int(unit["max_hp"]))
		bar.value = int(unit["hp"])
		bar.custom_minimum_size = Vector2(200, 14)
		var line := HBoxContainer.new()
		var label := Label.new()
		label.text = "%s%s %d/%d" % ["★" if id == my_unit else "", unit["name"], int(unit["hp"]), int(unit["max_hp"])]
		label.custom_minimum_size = Vector2(220, 0)
		line.add_child(label)
		line.add_child(bar)
		_units_box.add_child(line)

func _selected(option: OptionButton) -> String:
	if option.selected < 0:
		return ""
	return str(option.get_item_metadata(option.selected))

func _submit(kind: String) -> void:
	var command := {"kind": kind}
	if kind in ["attack", "skill", "item"]:
		command["target_id"] = _selected(_target)
	if kind == "skill":
		command["skill_id"] = _selected(_skill)
	if kind == "item":
		command["item_id"] = _selected(_item)
	var response: Dictionary = await app.call_server("battle.command", {"battle_id": battle_id, "round": round, "command": command})
	if not response.get("success", false):
		_log.append_text("命令被拒绝：%s\n" % app.error_text(response))
	else:
		_log.append_text("已提交第 %d 回合命令\n" % round)
