extends PanelContainer
## NPC 对话、商店与制作弹窗：选项由服务端过滤，动作结果全部由服务端返回。

const UI = preload("res://framework/presentation/hud/ui.gd")
var app
var _body: VBoxContainer
var _dialog := {}

func setup(owner_app) -> void:
	app = owner_app
	custom_minimum_size = Vector2(420, 200)
	_body = VBoxContainer.new()
	add_child(_body)
	hide()

func show_dialog(dialog: Dictionary) -> void:
	_dialog = dialog
	UI.clear(_body)
	_body.add_child(UI.label("%s：%s" % [app.bundle.display_name(str(dialog["npc_id"])), dialog["text"]]))
	for option in dialog.get("options", []):
		var option_id := str(option["option_id"])
		_body.add_child(UI.button(str(option["text"]), func(): _choose(option_id)))
	show()

func _choose(option_id: String) -> void:
	var response: Dictionary = await app.call_server("npc.choose", {"npc_id": _dialog["npc_id"], "node_id": _dialog["node_id"], "option_id": option_id, "idempotency_key": app.new_key()})
	if not response.get("success", false):
		app.log_line(app.error_text(response))
		hide()
		return
	var data: Dictionary = response["data"]
	if data.get("dialog") is Dictionary:
		show_dialog(data["dialog"])
	else:
		hide()
	if data.has("shop"):
		show_shop(data["shop"])
	if data.has("crafting"):
		show_crafting(data["crafting"])
	if data.has("result"):
		app.log_line("完成：" + JSON.stringify(data["result"]))
	if data.has("dungeon"):
		app.state.apply_push("dungeon.updated", data["dungeon"])

func show_shop(shop: Dictionary) -> void:
	UI.clear(_body)
	_body.add_child(UI.label("%s（货币 %d）" % [shop["display_name"], int(app.state.inventory.get("balance", 0))]))
	for good in shop.get("goods", []):
		var item_id := str(good["item_id"])
		var limit := int(good.get("purchase_limit", 0))
		var text := "%s 价格 %d" % [app.bundle.display_name(item_id), int(good["price"])]
		if limit > 0:
			text += " 限购 %d/%d" % [int(good.get("purchased", 0)), limit]
		if good.has("stock_remaining"):
			text += " 库存 %d" % int(good["stock_remaining"])
		_body.add_child(UI.row([UI.label(text), UI.button("购买", func(): app.send_and_log("shop.buy", {"shop_id": shop["shop_id"], "item_id": item_id, "quantity": 1, "idempotency_key": app.new_key()}))]))
	_body.add_child(UI.button("关闭", hide))
	show()

func show_crafting(crafting: Dictionary) -> void:
	UI.clear(_body)
	for recipe_id in crafting.get("recipe_ids", []):
		var recipe: Dictionary = app.bundle.get_public(recipe_id)
		var materials := []
		for stack in recipe.get("materials", []):
			var have := int(app.state.inventory.get("items", {}).get(stack["item_id"], 0))
			materials.append("%s %d/%d" % [app.bundle.display_name(stack["item_id"]), have, int(stack["quantity"])])
		_body.add_child(UI.label("%s：%s" % [recipe.get("display_name", recipe_id), ", ".join(materials)]))
		_body.add_child(UI.button("制作", func(): app.send_and_log("craft.make", {"recipe_id": recipe_id, "idempotency_key": app.new_key()})))
	_body.add_child(UI.button("关闭", hide))
	show()
