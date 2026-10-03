extends SceneTree

const Exporter = preload("res://addons/tbcg_authoring/map_geometry_exporter.gd")
const FileWriter = preload("res://addons/tbcg_authoring/map_file_writer.gd")
const SCENE := "res://games/demo/maps/training-ground.tscn"

func _initialize() -> void:
	call_deferred("_run")

func _run() -> void:
	var arguments := OS.get_cmdline_user_args()
	var output_path := ""
	for index in arguments.size():
		if arguments[index] == "--output" and index + 1 < arguments.size():
			output_path = arguments[index + 1]
			break
	if not output_path.is_absolute_path():
		push_error("usage: --output <absolute-path>")
		quit(2)
		return
	var packed: PackedScene = load(SCENE)
	if packed == null:
		push_error("failed to load trusted training scene")
		quit(1)
		return
	var root := packed.instantiate()
	var result: Dictionary = Exporter.export_scene(root)
	root.free()
	if not result.get("ok", false):
		push_error("map export rejected: %s" % [result.get("errors", [])])
		quit(1)
		return
	var write_result: Dictionary = FileWriter.write_file(output_path, Exporter.canonical_json(result.data) + "\n")
	if not write_result.get("ok", false):
		push_error("cannot write requested output path: %s" % write_result.get("error", "unknown write error"))
		quit(1)
		return
	quit(0)
