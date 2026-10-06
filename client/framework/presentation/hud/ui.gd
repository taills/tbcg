extends RefCounted
## 程序化 UI 的小工具。

static func button(text: String, action: Callable) -> Button:
	var b := Button.new()
	b.text = text
	b.pressed.connect(action)
	return b

static func label(text: String) -> Label:
	var l := Label.new()
	l.text = text
	l.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
	return l

static func line_edit(placeholder: String) -> LineEdit:
	var e := LineEdit.new()
	e.placeholder_text = placeholder
	e.custom_minimum_size = Vector2(140, 0)
	return e

static func row(children: Array) -> HBoxContainer:
	var r := HBoxContainer.new()
	for child in children:
		r.add_child(child)
	return r

static func clear(container: Node) -> void:
	for child in container.get_children():
		child.queue_free()
