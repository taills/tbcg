extends RefCounted
## 服务端权威状态的本地镜像：只合并服务端快照与推送，不在本地推进任务、背包或数值。

signal changed(section: String)

var character := {}
var state_version := ""
var inventory := {"items": {}, "balance": 0}
var quests: Array = []
var companions := {"pets": [], "partners": [], "mounts": []}
var party: Variant = null
var dungeon: Variant = null
var friends := {"friends": [], "incoming": [], "outgoing": []}
var guild := {}
var entities := {}
var map_id := ""

func apply_snapshot(snapshot: Dictionary) -> void:
	if snapshot.has("state"):
		character = snapshot.duplicate(true)
		state_version = str(snapshot.get("state_version", state_version))
		map_id = str(snapshot["state"].get("map_id", map_id))
		changed.emit("character")
	if snapshot.has("inventory"):
		inventory = snapshot["inventory"]
		changed.emit("inventory")
	if snapshot.has("quests"):
		quests = snapshot["quests"] if snapshot["quests"] is Array else []
		changed.emit("quests")
	if snapshot.has("companions"):
		companions = snapshot["companions"]
		changed.emit("companions")
	if snapshot.has("party"):
		party = snapshot["party"]
		changed.emit("party")
	if snapshot.has("dungeon"):
		dungeon = snapshot["dungeon"]
		changed.emit("dungeon")
	if snapshot.has("friends"):
		friends = snapshot["friends"]
		changed.emit("friends")
	if snapshot.has("guild"):
		guild = snapshot["guild"] if snapshot["guild"] is Dictionary else {}
		changed.emit("guild")
	if snapshot.has("map_entities"):
		set_entities(snapshot["map_entities"])

func set_entities(view: Dictionary) -> void:
	entities.clear()
	for player in view.get("players", []):
		entities[str(player["character_id"])] = player
	changed.emit("entities")

func position() -> Vector2i:
	var s: Dictionary = character.get("state", {})
	return Vector2i(int(s.get("x", 0)), int(s.get("y", 0)))

func character_id() -> String:
	return str(character.get("character_id", ""))

func apply_push(message_type: String, data: Dictionary) -> void:
	match message_type:
		"character.updated":
			apply_snapshot(data)
		"world.entity_joined":
			entities[str(data["character_id"])] = data
			changed.emit("entities")
		"world.entity_moved":
			var id := str(data.get("character_id", ""))
			if entities.has(id):
				for key in ["x", "y", "in_battle", "mount_id"]:
					if data.has(key):
						entities[id][key] = data[key]
				changed.emit("entities")
		"world.entity_left":
			entities.erase(str(data.get("character_id", "")))
			changed.emit("entities")
		"party.updated":
			party = null if data.get("left", false) else data
			changed.emit("party")
		"dungeon.updated":
			dungeon = data
			changed.emit("dungeon")
		"friend.updated":
			friends = data
			changed.emit("friends")
		"guild.updated":
			guild = data
			changed.emit("guild")
