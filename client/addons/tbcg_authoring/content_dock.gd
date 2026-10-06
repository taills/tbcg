@tool
extends VBoxContainer
## 编辑器 Dock：载入公开内容包，显示当前地图锚点的只读内容投影与断开引用。

const Exporter = preload("res://addons/tbcg_authoring/map_geometry_exporter.gd")
const ContentBinding = preload("res://addons/tbcg_authoring/content_binding.gd")

var editor: EditorInterface
var _path: LineEdit
var _output: RichTextLabel

func _ready() -> void:
	name = "TBCG 内容"
	_path = LineEdit.new()
	_path.text = ProjectSettings.globalize_path("res://../tests/fixtures/content/demo-public-bundle.json")
	add_child(_path)
	var analyze := Button.new()
	analyze.text = "分析当前地图"
	analyze.pressed.connect(_analyze)
	add_child(analyze)
	_output = RichTextLabel.new()
	_output.custom_minimum_size = Vector2(260, 360)
	_output.size_flags_vertical = Control.SIZE_EXPAND_FILL
	add_child(_output)

func _analyze() -> void:
	_output.clear()
	var root := editor.get_edited_scene_root() if editor != null else null
	if root == null:
		_output.append_text("没有打开的地图场景。\n")
		return
	var exported: Dictionary = Exporter.export_scene(root)
	if not exported.get("ok", false):
		_output.append_text("地图校验失败：\n" + "\n".join(exported.get("errors", [])) + "\n")
		return
	var bundle: Variant = JSON.parse_string(FileAccess.get_file_as_string(_path.text))
	if not bundle is Dictionary or bundle.get("scope", "") != "public":
		_output.append_text("无法读取公开内容包（只接受 scope=public 的白名单包）。\n")
		return
	var report: Dictionary = ContentBinding.analyze(exported["data"], bundle)
	_output.append_text("地图 %s\n" % report["map_id"])
	for binding in report["bindings"]:
		_output.append_text("✔ %s → %s（%s）\n" % [binding["anchor_id"], binding["content_id"], binding["role"]])
	for item in report["broken"]:
		_output.append_text("✘ %s 的 %s 引用 %s：%s\n" % [item["content_id"], item["role"], item["anchor_id"], item["reason"]])
	for anchor_id in report["unused_anchors"]:
		_output.append_text("· 未被内容引用的锚点：%s\n" % anchor_id)
