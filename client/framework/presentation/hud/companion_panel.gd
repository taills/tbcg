extends VBoxContainer
## 宠物、伙伴与坐骑。

const UI = preload("res://framework/presentation/hud/ui.gd")
var app
var _body: VBoxContainer

func setup(owner_app) -> void:
	app = owner_app
	name = "伙伴"
	_body = VBoxContainer.new()
	add_child(_body)
	app.state.changed.connect(func(section): if section == "companions": refresh())
	refresh()

func refresh() -> void:
	UI.clear(_body)
	var c: Dictionary = app.state.companions
	_body.add_child(UI.label("宠物："))
	for pet in c.get("pets", []):
		var uid := str(pet["pet_uid"])
		var rename := UI.line_edit("新名字")
		_body.add_child(UI.row([UI.label("%s Lv.%d %s" % [pet["name"], int(pet["level"]), "【出战】" if pet["active"] else ""]),
			UI.button("出战", func(): app.send_and_log("pet.activate", {"pet_uid": uid})), rename,
			UI.button("改名", func(): app.send_and_log("pet.rename", {"pet_uid": uid, "name": rename.text}))]))
	_body.add_child(UI.button("召回出战宠物", func(): app.send_and_log("pet.rest", {})))
	_body.add_child(UI.label("伙伴："))
	for partner in c.get("partners", []):
		var id := str(partner["partner_id"])
		var on: bool = partner["in_formation"]
		_body.add_child(UI.row([UI.label("%s %s" % [app.bundle.display_name(id), "【助战】" if on else ""]),
			UI.button("离队" if on else "助战", func(): app.send_and_log("partner.formation", {"partner_id": id, "in_formation": not on}))]))
	_body.add_child(UI.label("坐骑："))
	for mount in c.get("mounts", []):
		var id := str(mount["mount_id"])
		_body.add_child(UI.row([UI.label("%s %s" % [app.bundle.display_name(id), "【骑乘中】" if mount["equipped"] else ""]),
			UI.button("骑乘", func(): app.send_and_log("mount.equip", {"mount_id": id}))]))
	_body.add_child(UI.button("下马", func(): app.send_and_log("mount.unequip", {})))
