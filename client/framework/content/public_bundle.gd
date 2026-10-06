extends RefCounted
## 公开白名单包：校验 SHA-256、格式与条目形状；任何包含 definition/server 的私有形状一律拒绝。

const ENTRY_KEYS := ["content_id", "content_type", "public", "revision"]
const FORBIDDEN_KEYS := ["server", "definition", "body_hash"]

var game_id := ""
var release_id := ""
var release_version := 0
var sha256 := ""
var last_error := ""
var _by_type := {}
var _by_id := {}

func load_bytes(bytes: PackedByteArray, expected_sha256: String) -> bool:
	var hashing := HashingContext.new()
	hashing.start(HashingContext.HASH_SHA256)
	hashing.update(bytes)
	sha256 = hashing.finish().hex_encode()
	if not expected_sha256.is_empty() and sha256 != expected_sha256:
		return _fail("公开包哈希与服务器声明不一致")
	var parsed: Variant = JSON.parse_string(bytes.get_string_from_utf8())
	if not parsed is Dictionary:
		return _fail("公开包不是 JSON 对象")
	return load_dictionary(parsed)

func load_dictionary(bundle: Dictionary) -> bool:
	if bundle.get("scope", "") != "public" or int(bundle.get("format_version", 0)) != 1 or not bundle.get("entries") is Array:
		return _fail("公开包格式或 scope 无效")
	game_id = str(bundle.get("game_id", ""))
	release_id = str(bundle.get("release_id", ""))
	release_version = int(bundle.get("release_version", 0))
	_by_type.clear()
	_by_id.clear()
	for entry in bundle["entries"]:
		if not entry is Dictionary:
			return _fail("公开包条目无效")
		var keys: Array = entry.keys()
		keys.sort()
		if keys != ENTRY_KEYS:
			return _fail("公开包条目包含非白名单字段")
		var public_value: Variant = entry["public"]
		if not public_value is Dictionary:
			return _fail("公开投影必须是对象")
		for forbidden in FORBIDDEN_KEYS:
			if public_value.has(forbidden):
				return _fail("公开投影包含私有字段 %s" % forbidden)
		var content_type := str(entry["content_type"])
		var content_id := str(entry["content_id"])
		if public_value.get("content_id", "") != content_id or public_value.get("game_id", "") != game_id:
			return _fail("公开投影身份不一致")
		if not _by_type.has(content_type):
			_by_type[content_type] = {}
		_by_type[content_type][content_id] = public_value
		_by_id[content_id] = public_value
	return true

func of_type(content_type: String) -> Dictionary:
	return _by_type.get(content_type, {})

func get_public(content_id: String) -> Dictionary:
	return _by_id.get(content_id, {})

func display_name(content_id: String) -> String:
	var value: Dictionary = get_public(content_id)
	return str(value.get("display_name", content_id))

func geometry(map_id: String) -> Dictionary:
	return get_public(map_id).get("geometry", {})

func npcs_on_map(map_id: String) -> Array:
	var out := []
	for npc in of_type("npc").values():
		if npc.get("map_id", "") == map_id:
			out.append(npc)
	return out

func _fail(message: String) -> bool:
	last_error = message
	return false
