extends VBoxContainer
## 跨玩家交易：报价、锁定、双确认；资产转移只在服务端事务中发生。

const UI = preload("res://framework/presentation/hud/ui.gd")
var app
var trade := {}
var _body: VBoxContainer

func setup(owner_app) -> void:
	app = owner_app
	name = "交易"
	_body = VBoxContainer.new()
	add_child(_body)
	refresh()

func on_push(message_type: String, data: Dictionary) -> void:
	if message_type == "trade.invited":
		trade = {"trade_id": data["trade_id"], "status": "invited", "from_name": data["from_name"], "party_b": app.state.character_id()}
	elif message_type == "trade.updated":
		trade = {} if data.get("status", "") in ["done", "cancelled"] else data
		if data.has("reason"):
			app.log_line("交易%s：%s" % ["完成" if data["status"] == "done" else "取消", data["reason"]])
	refresh()

func refresh() -> void:
	UI.clear(_body)
	if trade.is_empty():
		var target := UI.line_edit("角色名")
		_body.add_child(UI.row([target, UI.button("发起交易", func(): app.send_and_log("trade.invite", {"character_name": target.text}))]))
		return
	var trade_id := str(trade["trade_id"])
	if trade.get("status", "") == "invited" and str(trade.get("party_b", "")) == app.state.character_id():
		_body.add_child(UI.row([UI.label("收到交易邀请"), UI.button("接受", func(): app.send_and_log("trade.respond", {"trade_id": trade_id, "accept": true})),
			UI.button("拒绝", func(): app.send_and_log("trade.respond", {"trade_id": trade_id, "accept": false}))]))
		return
	_body.add_child(UI.label("交易状态：%s" % trade.get("status", "")))
	var offers: Dictionary = trade.get("offers", {})
	for owner in offers:
		var offer: Dictionary = offers[owner]
		var parts := []
		for stack in offer.get("items", []):
			parts.append("%s×%d" % [app.bundle.display_name(stack["item_id"]), int(stack["quantity"])])
		_body.add_child(UI.label("%s 报价：%s 货币 %d %s %s" % [app.name_of(owner), ", ".join(parts), int(offer.get("currency", 0)),
			"已锁定" if trade.get("locked", {}).get(owner, false) else "", "已确认" if trade.get("confirmed", {}).get(owner, false) else ""]))
	var item := OptionButton.new()
	var items: Dictionary = app.state.inventory.get("items", {})
	for item_id in items:
		item.add_item("%s ×%d" % [app.bundle.display_name(item_id), int(items[item_id])])
		item.set_item_metadata(item.item_count - 1, item_id)
	var quantity := SpinBox.new()
	quantity.min_value = 1
	quantity.max_value = 99
	var currency := SpinBox.new()
	currency.max_value = 1000000
	_body.add_child(UI.row([item, quantity, UI.label("货币"), currency]))
	_body.add_child(UI.row([UI.button("提交报价", _submit_offer.bind(trade_id, item, quantity, currency)),
		UI.button("锁定", func(): app.send_and_log("trade.lock", {"trade_id": trade_id, "offer_version": int(trade.get("offer_version", 0))})),
		UI.button("确认", func(): app.send_and_log("trade.confirm", {"trade_id": trade_id, "offer_version": int(trade.get("offer_version", 0))})),
		UI.button("取消", func(): app.send_and_log("trade.cancel", {"trade_id": trade_id}))]))

func _submit_offer(trade_id: String, item: OptionButton, quantity: SpinBox, currency: SpinBox) -> void:
	var stacks := []
	if item.selected >= 0:
		stacks.append({"item_id": item.get_item_metadata(item.selected), "quantity": int(quantity.value)})
	app.send_and_log("trade.offer", {"trade_id": trade_id, "items": stacks, "currency": int(currency.value)})
