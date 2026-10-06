extends SceneTree

const PublicBundle = preload("res://framework/content/public_bundle.gd")
const BUNDLE_PATH := "res://../tests/fixtures/content/demo-public-bundle.json"

var _failures := 0

func _initialize() -> void:
	call_deferred("_run")

func _run() -> void:
	var bytes := FileAccess.get_file_as_bytes(BUNDLE_PATH)
	var bundle = PublicBundle.new()
	_assert(bundle.load_bytes(bytes, ""), "demo public bundle loads: " + bundle.last_error)
	_assert(bundle.of_type("map").size() == 2, "bundle has two maps")
	_assert(bundle.of_type("npc").size() == 2, "bundle has two NPCs")
	_assert(not bundle.get_public("demo:npc/guide").has("nodes"), "NPC dialog graph (server field) is not public")
	_assert(not bundle.get_public("demo:monster/slime").has("loot"), "monster loot is not public")
	_assert(not bundle.get_public("demo:item/herb").has("drop_weight_basis_points"), "item drop weight is not public")
	_assert(not bundle.get_public("demo:dungeon/wolf-den").has("rewards"), "dungeon rewards are not public")
	_assert(not bundle.load_bytes(bytes, "00"), "sha256 mismatch is rejected")

	var text := bytes.get_string_from_utf8()
	_assert(text.find("\"server\"") == -1, "bundle bytes contain no server object")
	var tampered: Dictionary = JSON.parse_string(text)
	tampered["entries"][0]["public"]["server"] = {"secret": 1}
	_assert(not bundle.load_dictionary(tampered), "entry public with server field is rejected")
	var private_shape: Dictionary = JSON.parse_string(text)
	private_shape["entries"][0]["definition"] = {}
	_assert(not bundle.load_dictionary(private_shape), "private snapshot entry shape is rejected")
	var wrong_scope: Dictionary = JSON.parse_string(text)
	wrong_scope["scope"] = "private"
	_assert(not bundle.load_dictionary(wrong_scope), "private scope is rejected")
	_finish()

func _assert(condition: bool, message: String) -> void:
	if not condition:
		_failures += 1
		push_error("FAIL: " + message)
	else:
		print("PASS: " + message)

func _finish() -> void:
	print("public_bundle_test failures=%d" % _failures)
	quit(1 if _failures > 0 else 0)
