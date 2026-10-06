extends SceneTree

const ContentBinding = preload("res://addons/tbcg_authoring/content_binding.gd")

var _failures := 0

func _initialize() -> void:
	call_deferred("_run")

func _run() -> void:
	var geometry: Dictionary = JSON.parse_string(FileAccess.get_file_as_string("res://../tests/fixtures/maps/training-ground.json"))
	var bundle: Dictionary = JSON.parse_string(FileAccess.get_file_as_string("res://../tests/fixtures/content/demo-public-bundle.json"))
	var report: Dictionary = ContentBinding.analyze(geometry, bundle)
	_assert(report["broken"].is_empty(), "demo map has no broken anchor references")
	var roles: Array = report["bindings"].map(func(b): return "%s:%s" % [b["role"], b["anchor_id"]])
	_assert(roles.has("npc:guide") and roles.has("portal:portal") and roles.has("zone:training-area"), "guide/portal/zone bindings found")
	var broken_geometry: Dictionary = geometry.duplicate(true)
	broken_geometry["anchors"] = broken_geometry["anchors"].filter(func(a): return a["placement_id"] != "guide")
	var broken: Dictionary = ContentBinding.analyze(broken_geometry, bundle)
	_assert(broken["broken"].size() == 1 and broken["broken"][0]["content_id"] == "demo:npc/guide", "removed anchor is located as a broken NPC reference")
	print("content_binding_test failures=%d" % _failures)
	quit(1 if _failures > 0 else 0)

func _assert(condition: bool, message: String) -> void:
	if not condition:
		_failures += 1
		push_error("FAIL: " + message)
