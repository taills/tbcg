@tool
extends RefCounted

static func write_file(path: String, contents: String) -> Dictionary:
	var file := FileAccess.open(path, FileAccess.WRITE)
	if file == null:
		return _failure("无法打开输出文件（错误码 %d）" % FileAccess.get_open_error())
	file.store_string(contents)
	var write_error := file.get_error()
	if write_error == OK:
		file.flush()
		write_error = file.get_error()
	file.close()
	if write_error != OK:
		return _failure("写入或刷新输出文件失败（错误码 %d）" % write_error)
	return {"ok": true, "error": ""}

static func _failure(message: String) -> Dictionary:
	return {"ok": false, "error": message}
