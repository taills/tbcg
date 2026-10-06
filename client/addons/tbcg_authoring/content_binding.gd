@tool
extends RefCounted
## 把地图几何与公开内容投影关联：列出绑定到每个锚点的 NPC/传送/区域，并定位断开的锚点引用。
## 只读：不产生第二套玩法字段真源，编辑仍在管理平台完成。

static func analyze(geometry: Dictionary, bundle: Dictionary) -> Dictionary:
	var map_id := str(geometry.get("map_id", ""))
	var anchors := {}
	for anchor in geometry.get("anchors", []):
		anchors[str(anchor["placement_id"])] = str(anchor["kind"])
	var bindings := []
	var broken := []
	var used := {}
	for entry in bundle.get("entries", []):
		var public: Dictionary = entry.get("public", {})
		var content_id := str(entry.get("content_id", ""))
		match str(entry.get("content_type", "")):
			"npc":
				if public.get("map_id", "") == map_id:
					_bind(content_id, "npc", str(public.get("anchor_id", "")), "npc", anchors, bindings, broken, used)
			"map":
				if content_id == map_id:
					for portal in public.get("portals", []):
						_bind(content_id, "portal", str(portal.get("anchor_id", "")), "portal", anchors, bindings, broken, used)
					for zone in public.get("zones", []):
						_bind(content_id, "zone", str(zone.get("anchor_id", "")), "", anchors, bindings, broken, used)
				else:
					for portal in public.get("portals", []):
						if portal.get("target_map_id", "") == map_id:
							_bind(content_id, "portal_target", str(portal.get("target_anchor_id", "")), "", anchors, bindings, broken, used)
			"quest":
				pass
	var unused := []
	for anchor_id in anchors:
		if not used.has(anchor_id) and anchors[anchor_id] != "spawn":
			unused.append(anchor_id)
	unused.sort()
	return {"map_id": map_id, "bindings": bindings, "broken": broken, "unused_anchors": unused}

static func _bind(content_id: String, role: String, anchor_id: String, required_kind: String, anchors: Dictionary, bindings: Array, broken: Array, used: Dictionary) -> void:
	if not anchors.has(anchor_id):
		broken.append({"content_id": content_id, "role": role, "anchor_id": anchor_id, "reason": "锚点不存在"})
		return
	if not required_kind.is_empty() and anchors[anchor_id] != required_kind:
		broken.append({"content_id": content_id, "role": role, "anchor_id": anchor_id, "reason": "锚点类型应为 %s，实际为 %s" % [required_kind, anchors[anchor_id]]})
		return
	used[anchor_id] = true
	bindings.append({"content_id": content_id, "role": role, "anchor_id": anchor_id})
