extends SceneTree

const FileWriter = preload("res://addons/tbcg_authoring/map_file_writer.gd")
var _failures := 0

func _initialize() -> void:
	call_deferred("_run")

func _run() -> void:
	var directory := OS.get_temp_dir().path_join("tbcg-map-writer-%d-%d" % [OS.get_process_id(), Time.get_ticks_usec()])
	if DirAccess.dir_exists_absolute(directory) or DirAccess.make_dir_absolute(directory) != OK:
		push_error("无法创建独立的文件写入测试目录")
		quit(1)
		return
	var output_path := directory.path_join("output.json")
	var content := "{\"ok\":true}\n"
	var success: Dictionary = FileWriter.write_file(output_path, content)
	_assert(success.get("ok", false), "valid file write reports success")
	if success.get("ok", false):
		_assert(FileAccess.get_file_as_string(output_path) == content, "successful write flushes exact content")
	var failure: Dictionary = FileWriter.write_file(directory.path_join("missing/output.json"), content)
	_assert(not failure.get("ok", true) and not str(failure.get("error", "")).is_empty(), "open failure reports an actionable error")
	if FileAccess.file_exists(output_path):
		_assert(DirAccess.remove_absolute(output_path) == OK, "测试输出清理失败")
	_assert(DirAccess.remove_absolute(directory) == OK, "测试目录清理失败")
	quit(1 if _failures > 0 else 0)

func _assert(condition: bool, message: String) -> void:
	if not condition:
		_failures += 1
		push_error(message)
