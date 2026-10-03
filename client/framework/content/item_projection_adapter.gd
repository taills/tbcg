extends RefCounted

const ItemProjection = preload("res://framework/content/item_projection.gd")

var _schema: Dictionary = {}

func _init(public_schema: Dictionary = {}) -> void:
	_schema = public_schema.duplicate(true)

func from_dictionary(source: Dictionary) -> Dictionary:
	if not _schema_is_valid():
		return _failure("/", "schema_violation", "A valid public item schema must be injected")

	var properties: Dictionary = _schema["properties"]
	var keys: Array = source.keys()
	keys.sort()
	for key in keys:
		if not properties.has(key):
			return _failure("/" + _escape_pointer(str(key)), "unknown_field", "Field is not part of the public projection")
	for field in _schema["required"]:
		if not source.has(field):
			return _failure("/" + str(field), "missing_field", "Required field is missing")

	for field in properties:
		if not source.has(field):
			continue
		var field_schema: Dictionary = properties[field]
		var value: Variant = source[field]
		if field_schema.get("type", "") == "string":
			if not _validate_string(field, value, field_schema):
				return _failure("/" + str(field), "invalid_value", "Field does not match the public item schema")

	if not _validate_integer(source["revision"], properties["revision"]):
		return _failure("/revision", "invalid_value", "Revision must be an integer within the schema range")
	if not _validate_integer(source["schema_version"], properties["schema_version"]):
		return _failure("/schema_version", "invalid_value", "Schema version does not match the public schema")
	if not properties["item_type"].get("enum", []).has(source["item_type"]):
		return _failure("/item_type", "invalid_value", "Item type is not supported")
	if not _validate_integer(source["stack_limit"], properties["stack_limit"]):
		return _failure("/stack_limit", "invalid_value", "Stack limit must be an integer within the schema range")

	var projection = ItemProjection.new()
	projection.game_id = source["game_id"]
	projection.content_id = source["content_id"]
	projection.revision = int(source["revision"])
	projection.schema_version = int(source["schema_version"])
	projection.item_type = source["item_type"]
	projection.display_name = source["display_name"]
	projection.description = source.get("description", "")
	projection.icon_id = source["icon_id"]
	projection.stack_limit = int(source["stack_limit"])
	projection.price_minor = source["price_minor"]
	return {"ok": true, "data": projection, "error": {}}

func _schema_is_valid() -> bool:
	if _schema.is_empty() or _schema.get("additionalProperties", true) != false:
		return false
	if not _schema.get("properties") is Dictionary or not _schema.get("required") is Array:
		return false
	var properties: Dictionary = _schema["properties"]
	var required: Array = _schema["required"]
	for field in properties:
		if not field is String or not properties[field] is Dictionary:
			return false
	for field in ["game_id", "content_id", "revision", "schema_version", "item_type", "display_name", "icon_id", "stack_limit", "price_minor"]:
		if not properties.has(field) or not properties[field] is Dictionary or not required.has(field):
			return false
	for field in required:
		if not field is String or not properties.has(field):
			return false
	if not properties["item_type"].get("enum", []) is Array or properties["item_type"].get("enum", []).is_empty():
		return false
	for field in ["game_id", "content_id", "display_name", "icon_id", "price_minor"]:
		if properties[field].get("type", "") != "string":
			return false
	for field in ["revision", "stack_limit"]:
		if properties[field].get("type", "") != "integer":
			return false
	if not properties["schema_version"].has("const"):
		return false
	if properties.has("description") and properties["description"].get("type", "") != "string":
		return false
	return true

func _validate_string(field: String, value: Variant, field_schema: Dictionary) -> bool:
	if not value is String:
		return false
	if value.length() < int(field_schema.get("minLength", 0)) or value.length() > int(field_schema.get("maxLength", 2147483647)):
		return false
	var pattern := str(field_schema.get("pattern", ""))
	return pattern.is_empty() or _matches_pattern(value, pattern)

func _validate_integer(value: Variant, field_schema: Dictionary) -> bool:
	if not value is int and not value is float:
		return false
	var number := float(value)
	if not is_finite(number) or floor(number) != number:
		return false
	if field_schema.has("const"):
		return number == float(field_schema["const"])
	return number >= float(field_schema.get("minimum", -INF)) and number <= float(field_schema.get("maximum", INF))

func _matches_pattern(value: String, pattern: String) -> bool:
	var regex := RegEx.new()
	return regex.compile(pattern) == OK and regex.search(value) != null

func _escape_pointer(value: String) -> String:
	return value.replace("~", "~0").replace("/", "~1")

func _failure(path: String, code: String, message: String) -> Dictionary:
	return {"ok": false, "data": null, "error": {"path": path, "code": code, "message": message}}
