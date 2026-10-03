extends SceneTree

const ItemProjectionAdapter = preload("res://framework/content/item_projection_adapter.gd")
const FIXTURE_DIR := "res://../tests/fixtures/content/"
const SCHEMA_PATH := "res://../contracts/content/item-public.schema.json"

var _failures := 0

func _initialize() -> void:
	call_deferred("_run")

func _run() -> void:
	var adapter = ItemProjectionAdapter.new(_read_json(SCHEMA_PATH))
	_expect_fixture(adapter, "item.public.valid.json", true, "", "")
	_expect_fixture(adapter, "item.public.invalid-bigint-number.json", false, "/price_minor", "invalid_value")
	_expect_fixture(adapter, "item.invalid-enum.json", false, "/item_type", "invalid_value")
	_expect_fixture(adapter, "item.invalid-null-optional.json", false, "/description", "invalid_value")

	var public_item := _read_fixture("item.public.valid.json")
	public_item.erase("description")
	var omitted_description: Dictionary = adapter.from_dictionary(public_item)
	_assert(omitted_description.get("ok", false), "optional description may be omitted")
	if omitted_description.get("ok", false):
		var resource: Resource = omitted_description["data"]
		_assert(resource is Resource, "valid projection is returned as a Resource")
		_assert(resource.get("description").is_empty(), "omitted description defaults to empty text")
		_assert(not _has_property(resource, "server"), "Resource has no server-owned field")

	var private_item := _read_fixture("item.valid.json")
	var rejected_private: Dictionary = adapter.from_dictionary(private_item)
	_assert(not rejected_private.get("ok", true), "private definition is rejected")
	_assert(_error_code(rejected_private) == "unknown_field", "private definition uses the shared unknown-field code")

	var server_fields := _read_fixture("item.public.valid.json")
	server_fields["server"] = {"reward_value_minor": "250"}
	var rejected_server: Dictionary = adapter.from_dictionary(server_fields)
	_assert(not rejected_server.get("ok", true), "server fields are rejected")
	_assert(_error_path(rejected_server) == "/server", "server error path")
	_assert(_error_code(rejected_server) == "unknown_field", "server field uses the shared unknown-field code")

	var missing_required := _read_fixture("item.public.valid.json")
	missing_required.erase("stack_limit")
	var rejected_missing: Dictionary = adapter.from_dictionary(missing_required)
	_assert(_error_path(rejected_missing) == "/stack_limit", "missing field error path")
	_assert(_error_code(rejected_missing) == "missing_field", "missing field uses the shared code")

	var fractional_integer := _read_fixture("item.public.valid.json")
	fractional_integer["stack_limit"] = 20.5
	var rejected_fraction: Dictionary = adapter.from_dictionary(fractional_integer)
	_assert(_error_path(rejected_fraction) == "/stack_limit", "integer error path")
	_assert(_error_code(rejected_fraction) == "invalid_value", "fractional integer uses the shared code")

	var null_description := _read_fixture("item.public.valid.json")
	null_description["description"] = null
	var rejected_null: Dictionary = adapter.from_dictionary(null_description)
	_assert(_error_path(rejected_null) == "/description", "description error path")
	_assert(_error_code(rejected_null) == "invalid_value", "null optional value uses the shared code")

	var no_schema_adapter = ItemProjectionAdapter.new()
	var rejected_without_schema: Dictionary = no_schema_adapter.from_dictionary(public_item)
	_assert(_error_code(rejected_without_schema) == "schema_violation", "missing injected schema fails closed")
	var malformed_schema := _read_json(SCHEMA_PATH)
	malformed_schema["properties"].erase("revision")
	var malformed_adapter = ItemProjectionAdapter.new(malformed_schema)
	var rejected_malformed_schema: Dictionary = malformed_adapter.from_dictionary(public_item)
	_assert(_error_code(rejected_malformed_schema) == "schema_violation", "malformed injected schema fails closed")

	var incomplete_adapter = ItemProjectionAdapter.new({"additionalProperties": false})
	var incomplete_result: Dictionary = incomplete_adapter.from_dictionary(public_item)
	_assert(_error_code(incomplete_result) == "schema_violation", "incomplete schema must fail closed")
	var wrong_optional_schema := _read_json(SCHEMA_PATH)
	wrong_optional_schema["properties"]["description"] = 42
	var wrong_optional_adapter = ItemProjectionAdapter.new(wrong_optional_schema)
	var wrong_optional_result: Dictionary = wrong_optional_adapter.from_dictionary(public_item)
	_assert(_error_code(wrong_optional_result) == "schema_violation", "invalid optional schema must fail closed")

	quit(1 if _failures > 0 else 0)

func _expect_fixture(adapter: Object, file_name: String, expected_ok: bool, expected_path: String, expected_code: String) -> void:
	var fixture := _read_fixture(file_name)
	var projection: Dictionary = fixture.get("public", fixture)
	var result: Dictionary = adapter.from_dictionary(projection)
	_assert(result.get("ok", false) == expected_ok, "%s validity" % file_name)
	if not expected_path.is_empty():
		_assert(_error_path(result) == expected_path, "%s path" % file_name)
		_assert(_error_code(result) == expected_code, "%s code" % file_name)

func _read_fixture(file_name: String) -> Dictionary:
	return _read_json(FIXTURE_DIR + file_name)

func _read_json(path: String) -> Dictionary:
	var file_path := ProjectSettings.globalize_path(path)
	var parsed: Variant = JSON.parse_string(FileAccess.get_file_as_string(file_path))
	return parsed as Dictionary if parsed is Dictionary else {}

func _has_property(resource: Resource, property_name: String) -> bool:
	for property in resource.get_property_list():
		if property["name"] == property_name:
			return true
	return false

func _error_path(result: Dictionary) -> String:
	return str(result.get("error", {}).get("path", ""))

func _error_code(result: Dictionary) -> String:
	return str(result.get("error", {}).get("code", ""))

func _assert(condition: bool, message: String) -> void:
	if not condition:
		_failures += 1
		push_error(message)
