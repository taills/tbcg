extends RefCounted

# 适配器支持的信封 ABI；注入 Schema 不能删除处理代码依赖的字段。
const RESPONSE_FIELDS := ["protocol_version", "request_id", "type", "success", "server_seq", "state_version", "data", "error", "meta"]
const RESPONSE_FIELD_TYPES := {
	"request_id": "string", "type": "string", "success": "boolean",
	"server_seq": "string", "data": "object", "meta": "object"
}

var _peer: Object
var _timeout_seconds: float
var _elapsed := 0.0
var _catalog: Dictionary = {}
var _response_schema: Dictionary = {}
var _request_types: Dictionary = {}
var _server_types: Dictionary = {}
var _response_types: Dictionary = {}
var _request_payload_fields: Dictionary = {}
var _hello_request_id := ""
var _requested_release_id := ""
var _pending_ping_id := ""
var _pending_ping_nonce := ""
var _next_request_number := 1
var _state := "disconnected"
var _max_message_bytes := 0
var _protocol_version := ""
var last_error: Dictionary = {}
var last_message: Dictionary = {}

func _init(peer: Object, catalog: Dictionary = {}, response_schema: Dictionary = {}, timeout_seconds: float = 10.0) -> void:
	_peer = peer
	_timeout_seconds = timeout_seconds
	_load_contracts(catalog, response_schema)

func is_ready() -> bool:
	return _state == "ready"

func start_handshake(hello_payload: Dictionary) -> bool:
	if not last_error.is_empty() or _state != "disconnected":
		return false
	if not _payload_has_fields("system.hello", hello_payload):
		_fail("/payload", "missing_field", "Hello payload does not match the message catalog")
		return false
	var request_id := "godot-hello-%d" % _next_request_number
	_next_request_number += 1
	var envelope := {
		"protocol_version": _protocol_version,
		"request_id": request_id,
		"type": "system.hello",
		"payload": hello_payload.duplicate(true)
	}
	if not _send_envelope(envelope):
		return false
	_hello_request_id = request_id
	_requested_release_id = str(hello_payload["release_id"])
	_elapsed = 0.0
	_state = "handshaking"
	return true

func send_business_message(message_type: String, payload: Dictionary) -> bool:
	if not is_ready():
		_fail("/state", "invalid_value", "Business messages require a validated HelloAck")
		return false
	if not _request_types.has(message_type):
		_fail("/type", "unknown_field", "Message type is not in the protocol catalog")
		return false
	if not _payload_has_fields(message_type, payload):
		_fail("/payload", "missing_field", "Message payload does not match the message catalog")
		return false
	if message_type == "system.ping":
		if not payload["nonce"] is String or payload["nonce"].is_empty():
			_fail("/payload/nonce", "invalid_value", "Ping nonce must be a non-empty string")
			return false
		if not _pending_ping_id.is_empty():
			_fail("/state", "invalid_value", "A Ping request is already pending")
			return false
	var request_id := "godot-request-%d" % _next_request_number
	_next_request_number += 1
	var envelope := {
		"protocol_version": _protocol_version,
		"request_id": request_id,
		"type": message_type,
		"payload": payload.duplicate(true)
	}
	if not _send_envelope(envelope):
		return false
	if message_type == "system.ping":
		_pending_ping_id = request_id
		_pending_ping_nonce = str(payload["nonce"])
		_elapsed = 0.0
	return true

func poll(delta: float) -> void:
	if _state == "failed" or _state == "disconnected":
		return
	if _peer is WebSocketPeer:
		_peer.poll()
	while _peer.get_available_packet_count() > 0:
		var packet: PackedByteArray = _peer.get_packet()
		if _peer.get_packet_error() != OK:
			_fail("/connection", "invalid_value", "WebSocket packet could not be read")
			return
		if _peer.has_method("was_string_packet") and _peer.was_string_packet():
			_fail("/", "frame_type", "Protocol messages must use binary WebSocket frames")
			return
		if packet.size() > _max_message_bytes:
			_fail("/", "invalid_value", "Protocol message exceeds the catalog limit")
			return
		if not _consume_packet(packet):
			return
	if _peer.get_ready_state() == WebSocketPeer.STATE_CLOSED:
		_fail("/connection", "disconnected", "WebSocket connection closed")
		return
	if _state == "handshaking" or not _pending_ping_id.is_empty():
		_elapsed += maxf(delta, 0.0)
		if _elapsed >= _timeout_seconds:
			_fail("/connection", "timeout", "Protocol response timed out")

func _consume_packet(packet: PackedByteArray) -> bool:
	var payload_text := packet.get_string_from_utf8()
	if payload_text.to_utf8_buffer() != packet:
		_fail("/", "invalid_value", "Protocol payload is not valid UTF-8")
		return false
	var parser := JSON.new()
	if parser.parse(payload_text) != OK:
		_fail("/", "invalid_value", "Protocol payload is not valid JSON")
		return false
	if not parser.data is Dictionary:
		_fail("/", "invalid_value", "Protocol payload must be a JSON object")
		return false
	var response: Dictionary = parser.data
	var properties: Dictionary = _response_schema.get("properties", {})
	if not response.has("type") or not _server_types.has(str(response.get("type", ""))) or not _response_types.has(str(response.get("type", ""))):
		_fail("/type", "unknown_field", "Message type is not in the protocol catalog")
		return false
	for field in _response_schema.get("required", []):
		if not response.has(field):
			_fail("/" + str(field), "missing_field", "Response envelope field is missing")
			return false
	for key in response.keys():
		if not properties.has(key):
			_fail("/" + _escape_pointer(str(key)), "unknown_field", "Response envelope contains an unknown field")
			return false
	if response.get("protocol_version") != _protocol_version:
		_fail("/protocol_version", "invalid_value", "Unsupported protocol version")
		return false
	if not _validate_response_fields(response, properties):
		return false
	var message_type := str(response["type"])
	if message_type == "system.hello_ack":
		return _accept_hello_ack(response)
	if not is_ready():
		_fail("/state", "invalid_value", "Server sent a business response before HelloAck")
		return false
	if message_type == "system.pong":
		if not response["success"]:
			_fail("/success", "invalid_value", "Server rejected the Ping request")
			return false
		if response["request_id"] != _pending_ping_id or _pending_ping_id.is_empty():
			_fail("/request_id", "invalid_value", "Pong does not match a pending Ping")
			return false
		if not response["data"].has("nonce") or not response["data"].has("server_time"):
			_fail("/data", "missing_field", "Pong data does not match the message catalog")
			return false
		if response["data"]["nonce"] != _pending_ping_nonce:
			_fail("/data/nonce", "invalid_value", "Pong does not match the pending Ping")
			return false
		if not response["data"]["server_time"] is String:
			_fail("/data/server_time", "invalid_value", "Server time must be a string")
			return false
		_pending_ping_id = ""
		_pending_ping_nonce = ""
	last_message = response.duplicate(true)
	return true

func _validate_response_fields(response: Dictionary, properties: Dictionary) -> bool:
	for field in response:
		var definition: Dictionary = properties[field]
		var value: Variant = response[field]
		if definition.has("const") and value != definition["const"]:
			_fail("/" + str(field), "invalid_value", "Response value does not match the schema")
			return false
		if field == "request_id":
			if not value is String or value.is_empty() or value.length() > int(definition.get("maxLength", 128)):
				_fail("/request_id", "invalid_value", "Response request ID is invalid")
				return false
		if field == "server_seq" and not _matches_pattern(value, str(definition.get("pattern", ""))):
			_fail("/server_seq", "invalid_value", "Server sequence must be an unsigned decimal string")
			return false
		if field == "state_version" and value != null and not _matches_pattern(value, "^(0|[1-9][0-9]*)$"):
			_fail("/state_version", "invalid_value", "State version must be null or an unsigned decimal string")
			return false
	if not response.get("success") is bool or not response.get("data") is Dictionary or not response.get("meta") is Dictionary:
		_fail("/", "invalid_value", "Response envelope fields have invalid types")
		return false
	if response.get("error") != null and not response["error"] is Dictionary:
		_fail("/error", "invalid_value", "Response error must be an object or null")
		return false
	return true

func _accept_hello_ack(response: Dictionary) -> bool:
	if _state != "handshaking" or response["request_id"] != _hello_request_id:
		_fail("/request_id", "invalid_value", "HelloAck does not match the active handshake")
		return false
	if not response["success"]:
		_fail("/success", "invalid_value", "Server rejected the protocol handshake")
		return false
	var data: Dictionary = response["data"]
	if not data.has("release_id") or not data.has("capabilities") or not data["release_id"] is String or not data["capabilities"] is Array:
		_fail("/data", "missing_field", "HelloAck data does not match the message catalog")
		return false
	if data["release_id"] != _requested_release_id:
		_fail("/data/release_id", "invalid_value", "Server release does not match the requested release")
		return false
	if not data["capabilities"].has("protocol.v1"):
		_fail("/data/capabilities", "invalid_value", "Server does not support protocol.v1")
		return false
	_state = "ready"
	last_message = response.duplicate(true)
	_elapsed = 0.0
	return true

func _load_contracts(catalog: Dictionary, response_schema: Dictionary) -> void:
	if catalog.is_empty() or response_schema.is_empty():
		_fail("/", "schema_violation", "Protocol catalog and response schema must be injected")
		return
	_catalog = catalog.duplicate(true)
	_response_schema = response_schema.duplicate(true)
	var transport: Variant = _catalog.get("transport")
	var messages: Variant = _catalog.get("messages")
	var properties: Variant = _response_schema.get("properties")
	var required: Variant = _response_schema.get("required")
	if not transport is Dictionary or not messages is Array or messages.is_empty() or not properties is Dictionary or not required is Array or required.is_empty():
		_fail("/", "schema_violation", "Injected protocol contracts have invalid shapes")
		return
	if _response_schema.get("additionalProperties", true) != false:
		_fail("/", "schema_violation", "Response schema must reject unknown properties")
		return
	for field in properties:
		if not field is String or not properties[field] is Dictionary:
			_fail("/properties", "schema_violation", "Response schema properties are invalid")
			return
	if not _validate_response_contract(properties, required):
		return
	_protocol_version = str(_catalog.get("protocol_version", ""))
	_max_message_bytes = int(transport.get("max_message_bytes", 0))
	var protocol_definition: Dictionary = properties["protocol_version"]
	var type_definition: Dictionary = properties["type"]
	var response_type_values: Variant = type_definition.get("enum")
	if _protocol_version.is_empty() or _max_message_bytes < 1 or protocol_definition.get("const", "") != _protocol_version or not response_type_values is Array or response_type_values.is_empty():
		_fail("/", "schema_violation", "Injected protocol contracts are incomplete or inconsistent")
		return
	for field in required:
		if not field is String or not properties.has(field):
			_fail("/required", "schema_violation", "Response schema required fields are invalid")
			return
	var catalog_types := {}
	for entry in messages:
		if not entry is Dictionary:
			_fail("/messages", "schema_violation", "Protocol catalog contains an invalid message")
			return
		var message_type := str(entry.get("type", ""))
		var payload_fields: Variant = entry.get("required_payload_fields", [])
		if message_type.is_empty() or not payload_fields is Array:
			_fail("/messages", "schema_violation", "Protocol catalog contains an invalid message")
			return
		catalog_types[message_type] = true
		_request_payload_fields[message_type] = payload_fields.duplicate()
		if entry.get("direction", "") == "server_to_client":
			_server_types[message_type] = true
		elif entry.get("direction", "") == "client_to_server":
			_request_types[message_type] = true
		else:
			_fail("/messages", "schema_violation", "Protocol catalog contains an invalid direction")
			return
	for response_type in response_type_values:
		if not _server_types.has(response_type) or not catalog_types.has(response_type):
			_fail("/properties/type/enum", "schema_violation", "Protocol catalog and response schema disagree")
			return
		_response_types[response_type] = true
	if not catalog_types.has("system.hello") or not catalog_types.has("system.hello_ack"):
		_fail("/messages", "schema_violation", "Protocol catalog is missing its handshake messages")
		return
	if transport.get("protocol", "") != "wss" or transport.get("frame_type", "") != "binary" or transport.get("encoding", "") != "utf-8":
		_fail("/transport", "schema_violation", "Protocol transport does not match WSS binary UTF-8")

func _validate_response_contract(properties: Dictionary, required: Array) -> bool:
	if _response_schema.get("type") != "object":
		_fail("/type", "schema_violation", "响应 Schema 必须声明对象信封")
		return false
	for field in RESPONSE_FIELDS:
		if not properties.has(field) or not required.has(field):
			_fail("/required", "schema_violation", "响应 Schema 缺少必填信封字段：" + field)
			return false
	for field in RESPONSE_FIELD_TYPES:
		if properties[field].get("type") != RESPONSE_FIELD_TYPES[field]:
			_fail("/properties/" + field, "schema_violation", "响应字段类型与适配器契约不兼容")
			return false
	for field in {"state_version": "string", "error": "object"}:
		var expected_type := "string" if field == "state_version" else "object"
		if not _has_nullable_type(properties[field], expected_type):
			_fail("/properties/" + field, "schema_violation", "响应可空字段类型与适配器契约不兼容")
			return false
	return true

func _has_nullable_type(definition: Dictionary, expected_type: String) -> bool:
	var branches: Variant = definition.get("oneOf")
	if not branches is Array or branches.size() != 2:
		return false
	var types := []
	for branch in branches:
		if not branch is Dictionary:
			return false
		types.append(branch.get("type"))
	return types.has("null") and types.has(expected_type)

func _payload_has_fields(message_type: String, payload: Dictionary) -> bool:
	for field in _request_payload_fields.get(message_type, []):
		if not payload.has(field):
			return false
	return true

func _send_envelope(envelope: Dictionary) -> bool:
	var packet := JSON.stringify(envelope).to_utf8_buffer()
	if packet.size() > _max_message_bytes:
		_fail("/", "invalid_value", "Protocol message exceeds the catalog limit")
		return false
	var send_error: int
	if _peer is WebSocketPeer:
		send_error = _peer.send(packet, WebSocketPeer.WRITE_MODE_BINARY)
	else:
		send_error = _peer.send(packet)
	if send_error != OK:
		_fail("/connection", "invalid_value", "WebSocket packet could not be sent")
		return false
	return true

func _matches_pattern(value: Variant, pattern: String) -> bool:
	if not value is String or pattern.is_empty():
		return false
	var regex := RegEx.new()
	return regex.compile(pattern) == OK and regex.search(value) != null

func _escape_pointer(value: String) -> String:
	return value.replace("~", "~0").replace("/", "~1")

func _fail(path: String, code: String, message: String) -> void:
	if _state == "failed":
		return
	_state = "failed"
	last_error = {"path": path, "code": code, "message": message}
	if _peer != null and _peer.has_method("close"):
		_peer.close()
