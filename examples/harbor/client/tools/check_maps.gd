extends SceneTree
## 无界面校验：用框架导出器导出 res://games/harbor/maps 下的每个地图场景，与内容中的几何逐项比对；
## 传入公开包时再检查 NPC、传送点与区域对锚点的绑定（与编辑器「TBCG 内容」Dock 相同的规则）。
##
##   godot --headless --path examples/harbor/client --script res://tools/check_maps.gd [-- --bundle <public-bundle.json>]

const Exporter = preload("res://addons/tbcg_authoring/map_geometry_exporter.gd")
const ContentBinding = preload("res://addons/tbcg_authoring/content_binding.gd")
const MAP_DIR := "res://games/harbor/maps"
const CONTENT_DIR := "res://../content/map"

func _initialize() -> void:
	call_deferred("_run")

func _run() -> void:
	var bundle := _load_bundle()
	var failures := 0
	var scenes := DirAccess.get_files_at(MAP_DIR)
	for file in scenes:
		if file.get_extension() == "tscn":
			failures += _check(file.get_basename(), bundle)
	print("checked %d map scenes, %d failure(s)" % [scenes.size(), failures])
	quit(1 if failures > 0 else 0)

func _check(slug: String, bundle: Dictionary) -> int:
	var root: Node = load("%s/%s.tscn" % [MAP_DIR, slug]).instantiate()
	var exported: Dictionary = Exporter.export_scene(root)
	root.free()
	if not exported.get("ok", false):
		printerr("%s: export rejected %s" % [slug, exported.get("errors", [])])
		return 1
	var definition: Variant = JSON.parse_string(FileAccess.get_file_as_string(ProjectSettings.globalize_path("%s/%s.json" % [CONTENT_DIR, slug])))
	if not definition is Dictionary:
		printerr("%s: content definition missing" % slug)
		return 1
	# 经同一 JSON 解析后比较，避免整数/浮点表示差异。
	if JSON.parse_string(Exporter.canonical_json(exported.data)) != definition["public"]["geometry"]:
		printerr("%s: scene geometry differs from content/map/%s.json" % [slug, slug])
		return 1
	var failures := 0
	if not bundle.is_empty():
		var report: Dictionary = ContentBinding.analyze(exported.data, bundle)
		for item in report["broken"]:
			printerr("%s: %s %s -> %s: %s" % [slug, item["content_id"], item["role"], item["anchor_id"], item["reason"]])
			failures += 1
		print("%s: %d binding(s), %d unused anchor(s)" % [slug, report["bindings"].size(), report["unused_anchors"].size()])
	print("%s: OK" % slug)
	return failures

func _load_bundle() -> Dictionary:
	var arguments := OS.get_cmdline_user_args()
	var index := arguments.find("--bundle")
	if index < 0 or index + 1 >= arguments.size():
		return {}
	var bundle: Variant = JSON.parse_string(FileAccess.get_file_as_string(arguments[index + 1]))
	if not bundle is Dictionary or bundle.get("scope", "") != "public":
		printerr("not a public bundle: %s" % arguments[index + 1])
		quit(2)
		return {}
	return bundle
