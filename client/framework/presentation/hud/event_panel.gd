extends VBoxContainer
## 活动日历：开放状态以服务端时钟为准。

const UI = preload("res://framework/presentation/hud/ui.gd")
var app
var _body: VBoxContainer

func setup(owner_app) -> void:
	app = owner_app
	name = "活动"
	_body = VBoxContainer.new()
	add_child(_body)
	add_child(UI.button("刷新活动", refresh))

func refresh() -> void:
	var response: Dictionary = await app.call_server("event.list", {})
	UI.clear(_body)
	if not response.get("success", false):
		_body.add_child(UI.label(app.error_text(response)))
		return
	for event in response["data"].get("events", []):
		var id := str(event["event_id"])
		var definition: Dictionary = app.bundle.get_public(id)
		var objective: Dictionary = event["objective"]
		_body.add_child(UI.label("%s【%s】%s 进度 %d/%d 已领 %d" % [definition.get("display_name", id), "开放中" if event["open"] else "未开放",
			definition.get("description", ""), int(event.get("progress", 0)), int(objective["count"]), int(event.get("claims", 0))]))
		_body.add_child(UI.row([UI.button("领奖", func(): app.send_and_log("event.claim", {"event_id": id, "idempotency_key": app.new_key()})),
			UI.button("排行榜", func(): _leaderboard(id))]))

func _leaderboard(event_id: String) -> void:
	var response: Dictionary = await app.call_server("event.leaderboard", {"event_id": event_id})
	if response.get("success", false):
		var lines := []
		for entry in response["data"].get("entries", []):
			lines.append("%d. %s %d" % [int(entry["rank"]), entry["name"], int(entry["score"])])
		app.log_line("排行榜：" + ("；".join(lines) if not lines.is_empty() else "暂无"))
	else:
		app.log_line(app.error_text(response))
