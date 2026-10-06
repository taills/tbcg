extends RefCounted
## 战斗回放模型：严格按 event_seq 顺序应用服务端事件，HP 只取事件中的 hp_after，
## 不在客户端计算伤害、暴击或胜负；顺序或 hp_before 不一致即标记失步。

var units := {}
var applied_seq := 0
var desynced := false
var outcome := ""
var lines: Array[String] = []

func load_units(list: Array) -> void:
	units.clear()
	for unit in list:
		units[str(unit["id"])] = {"name": str(unit.get("name", "")), "side": int(unit.get("side", 0)), "hp": int(unit.get("hp", 0)),
			"mp": int(unit.get("mp", 0)), "max_hp": int(unit.get("stats", {}).get("max_hp", 1)), "kind": str(unit.get("kind", ""))}

func apply_events(events: Array) -> bool:
	for event in events:
		if not apply_event(event):
			return false
	return true

func apply_event(event: Dictionary) -> bool:
	var seq := int(event.get("event_seq", 0))
	if seq != applied_seq + 1:
		desynced = true
		return false
	applied_seq = seq
	var kind := str(event.get("kind", ""))
	var target := str(event.get("target_id", ""))
	match kind:
		"damage", "heal", "item":
			if not units.has(target):
				desynced = true
				return false
			if int(units[target]["hp"]) != int(event.get("hp_before", -1)):
				desynced = true
			units[target]["hp"] = int(event.get("hp_after", 0))
			var amount := int(event.get("damage", event.get("heal", 0)))
			var crit := " 暴击!" if event.get("crit", false) else ""
			lines.append("%s → %s %s %d%s" % [_name(str(event.get("actor_id", ""))), _name(target), "伤害" if kind == "damage" else "恢复", amount, crit])
		"death":
			if units.has(target):
				units[target]["hp"] = 0
			lines.append("%s 倒下" % _name(target))
		"buff":
			lines.append("%s 获得 %s %+d（%d 回合）" % [_name(target), str(event.get("stat", "")), int(event.get("amount", 0)), int(event.get("turns", 0))])
		"battle_end":
			outcome = str(event.get("outcome", ""))
			lines.append("战斗结束：%s" % outcome)
		_:
			lines.append("%s %s" % [_name(str(event.get("actor_id", ""))), kind])
	return true

func _name(unit_id: String) -> String:
	return str(units.get(unit_id, {}).get("name", unit_id))
