extends VBoxContainer
## 任务日志：阶段描述来自公开包，进度与状态只来自服务端。

const UI = preload("res://framework/presentation/hud/ui.gd")
const STATUS := {"active": "进行中", "objectives_done": "可交付", "completed": "已完成"}
var app
var _body: VBoxContainer

func setup(owner_app) -> void:
	app = owner_app
	name = "任务"
	_body = VBoxContainer.new()
	add_child(_body)
	app.state.changed.connect(func(section): if section == "quests": refresh())
	refresh()

func refresh() -> void:
	UI.clear(_body)
	if app.state.quests.is_empty():
		_body.add_child(UI.label("暂无任务。与 NPC 交谈以接取任务。"))
	for quest in app.state.quests:
		var definition: Dictionary = app.bundle.get_public(str(quest["quest_id"]))
		var stages: Array = definition.get("stage_descriptions", [])
		var index := int(quest["stage_index"])
		var stage_text: String = stages[index] if index < stages.size() else "全部目标已完成"
		_body.add_child(UI.label("【%s】%s — %s（进度 %d）" % [STATUS.get(quest["status"], quest["status"]), str(definition.get("display_name", quest["quest_id"])), stage_text, int(quest["stage_progress"])]))
		if quest["status"] != "completed":
			_body.add_child(UI.button("放弃", func(): app.send_and_log("quest.abandon", {"quest_id": quest["quest_id"]})))
