@tool
extends EditorPlugin

const MENU_LABEL := "校验/导出当前地图"
const Exporter = preload("res://addons/tbcg_authoring/map_geometry_exporter.gd")
const FileWriter = preload("res://addons/tbcg_authoring/map_file_writer.gd")
const ContentDock = preload("res://addons/tbcg_authoring/content_dock.gd")

var _file_dialog: FileDialog
var _message_dialog: AcceptDialog
var _pending_json := ""
var _content_dock: Control

func _enter_tree() -> void:
	_file_dialog = FileDialog.new()
	_file_dialog.file_mode = FileDialog.FILE_MODE_SAVE_FILE
	_file_dialog.access = FileDialog.ACCESS_FILESYSTEM
	_file_dialog.add_filter("*.json", "JSON map geometry")
	_file_dialog.file_selected.connect(_on_file_selected)
	add_child(_file_dialog)
	_message_dialog = AcceptDialog.new()
	add_child(_message_dialog)
	add_tool_menu_item(MENU_LABEL, _on_export_menu)
	_content_dock = ContentDock.new()
	_content_dock.editor = get_editor_interface()
	add_control_to_dock(DOCK_SLOT_RIGHT_UL, _content_dock)

func _exit_tree() -> void:
	remove_tool_menu_item(MENU_LABEL)
	if is_instance_valid(_content_dock):
		remove_control_from_docks(_content_dock)
		_content_dock.queue_free()
	if is_instance_valid(_file_dialog):
		_file_dialog.queue_free()
	if is_instance_valid(_message_dialog):
		_message_dialog.queue_free()

func _on_export_menu() -> void:
	var editor_interface := get_editor_interface()
	var selected := editor_interface.get_selection().get_selected_nodes()
	var edited_scene_root := editor_interface.get_edited_scene_root()
	if selected.size() != 1 or selected[0] != edited_scene_root or not selected[0] is Node2D or selected[0].get_script() == null or selected[0].get_script().resource_path != "res://addons/tbcg_authoring/map_authoring_2d.gd":
		_show_message("请先在场景树中只选择地图根节点 MapAuthoring2D。")
		return
	var result: Dictionary = Exporter.export_scene(selected[0])
	if not result.get("ok", false):
		_show_message("地图校验失败：\n• " + "\n• ".join(result.get("errors", [])))
		return
	_pending_json = Exporter.canonical_json(result["data"]) + "\n"
	_file_dialog.current_file = "%s.json" % str(result["data"]["map_id"]).get_file()
	_file_dialog.popup_file_dialog()

func _on_file_selected(path: String) -> void:
	if _pending_json.is_empty():
		_show_message("没有待导出的地图数据，请重新执行导出。")
		return
	var result: Dictionary = FileWriter.write_file(path, _pending_json)
	if not result.get("ok", false):
		_show_message("导出失败：%s（%s）" % [path, result.get("error", "未知写入错误")])
		return
	_pending_json = ""
	_show_message("地图已导出到：%s" % path)

func _show_message(message: String) -> void:
	_message_dialog.dialog_text = message
	_message_dialog.popup_centered()
