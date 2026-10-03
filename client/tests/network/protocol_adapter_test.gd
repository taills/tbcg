extends SceneTree

const ProtocolAdapter = preload("res://framework/network/protocol_adapter.gd")
const FIXTURE_DIR := "res://../tests/fixtures/protocol/"
const CATALOG_PATH := "res://../contracts/protocol/message_catalog.json"
const RESPONSE_SCHEMA_PATH := "res://../contracts/protocol/response.schema.json"

var _failures := 0

class FakePeer:
	extends RefCounted
	var status := WebSocketPeer.STATE_OPEN
	var sent_packets: Array[PackedByteArray] = []
	var incoming: Array[PackedByteArray] = []
	var packet_error := OK
	var string_packet := false
	var received_packet_count := 0
	var close_count := 0

	func was_string_packet() -> bool:
		return string_packet

	func get_ready_state() -> int:
		return status

	func send(packet: PackedByteArray) -> Error:
		sent_packets.append(packet)
		return OK

	func get_packet() -> PackedByteArray:
		received_packet_count += 1
		return incoming.pop_front()

	func get_packet_error() -> Error:
		return packet_error

	func get_available_packet_count() -> int:
		return incoming.size()

	func close() -> void:
		close_count += 1
		status = WebSocketPeer.STATE_CLOSED

func _initialize() -> void:
	call_deferred("_run")

func _run() -> void:
	var hello: Dictionary = _read_fixture("hello.request.json")
	var ack: Dictionary = _read_fixture("hello.response.json")
	var ping: Dictionary = _read_fixture("ping.request.json")
	var pong: Dictionary = _read_fixture("pong.response.json")
	var premature_peer := FakePeer.new()
	var premature_client = _new_client(premature_peer)
	_assert(not premature_client.send_business_message("system.ping", {"nonce": "early"}), "business message before HelloAck")
	_assert(premature_peer.close_count == 1, "protocol failures close the owned peer")
	_assert(not premature_client.is_ready(), "failed client is never ready")

	var no_contract_peer := FakePeer.new()
	var no_contract_client = ProtocolAdapter.new(no_contract_peer)
	_assert(_error_code(no_contract_client) == "schema_violation", "missing injected protocol schema fails closed")
	var malformed_contract_peer := FakePeer.new()
	var malformed_response_schema := _read_json(RESPONSE_SCHEMA_PATH)
	malformed_response_schema["properties"].erase("request_id")
	var malformed_contract_client = ProtocolAdapter.new(malformed_contract_peer, _read_json(CATALOG_PATH), malformed_response_schema)
	_assert(_error_code(malformed_contract_client) == "schema_violation", "malformed response schema fails closed")

	var peer := FakePeer.new()
	var client = _new_client(peer)
	_assert(not client.is_ready(), "client must start not ready")
	_assert(client.start_handshake(hello["payload"]), "hello send")
	_assert(not client.is_ready(), "Hello does not make client ready")
	var sent_hello: Dictionary = JSON.parse_string(peer.sent_packets[0].get_string_from_utf8())
	ack["request_id"] = sent_hello["request_id"]
	peer.incoming.append(JSON.stringify(ack).to_utf8_buffer())
	client.poll(0.1)
	_assert(client.is_ready(), "valid HelloAck makes client ready")
	_assert(client.send_business_message("system.ping", ping["payload"]), "ready client can send ping")
	_assert(peer.sent_packets.size() == 2, "hello and ping are sent as binary packets")
	var sent_ping: Dictionary = JSON.parse_string(peer.sent_packets[1].get_string_from_utf8())
	_assert(sent_ping["type"] == "system.ping", "ping JSON uses binary transport")
	pong["request_id"] = sent_ping["request_id"]
	peer.incoming.append(JSON.stringify(pong).to_utf8_buffer())
	client.poll(0.1)
	_assert(client.last_message.get("type", "") == "system.pong", "pong is decoded")

	var unknown_peer := FakePeer.new()
	var unknown_client = _new_client(unknown_peer)
	unknown_client.start_handshake(hello["payload"])
	unknown_peer.incoming.append(JSON.stringify(_read_fixture("unknown-type.request.json")).to_utf8_buffer())
	unknown_client.poll(0.1)
	_assert(_error_path(unknown_client) == "/type", "unknown protocol type returns a field error")

	var malformed_peer := FakePeer.new()
	var malformed_client = _new_client(malformed_peer)
	malformed_client.start_handshake(hello["payload"])
	malformed_peer.incoming.append(PackedByteArray([123, 0, 125]))
	malformed_client.poll(0.1)
	_assert(_error_path(malformed_client) == "/", "malformed JSON returns a field error")

	var text_peer := FakePeer.new()
	var text_client = _new_client(text_peer)
	text_client.start_handshake(hello["payload"])
	var text_hello: Dictionary = JSON.parse_string(text_peer.sent_packets[0].get_string_from_utf8())
	var text_ack: Dictionary = ack.duplicate(true)
	text_ack["request_id"] = text_hello["request_id"]
	text_peer.string_packet = true
	text_peer.incoming.append(JSON.stringify(text_ack).to_utf8_buffer())
	text_client.poll(0.1)
	_assert(_error_code(text_client) == "frame_type", "text WebSocket frames are rejected")

	var timeout_peer := FakePeer.new()
	var timeout_client = _new_client(timeout_peer, 1.0)
	timeout_client.start_handshake(hello["payload"])
	timeout_client.poll(1.1)
	_assert(_error_code(timeout_client) == "timeout", "handshake timeout enters failure state")
	_assert(timeout_peer.close_count == 1, "timeout closes the owned peer")

	var disconnect_peer := FakePeer.new()
	var disconnected_client = _new_client(disconnect_peer)
	disconnected_client.start_handshake(hello["payload"])
	disconnect_peer.status = WebSocketPeer.STATE_CLOSED
	disconnected_client.poll(0.1)
	_assert(_error_code(disconnected_client) == "disconnected", "disconnect enters failure state")
	_assert(disconnect_peer.close_count == 1, "disconnect closes the owned peer")

	var buffered_peer := FakePeer.new()
	var buffered_client = _new_client(buffered_peer)
	buffered_client.start_handshake(hello["payload"])
	var buffered_hello: Dictionary = JSON.parse_string(buffered_peer.sent_packets[0].get_string_from_utf8())
	var buffered_ack: Dictionary = ack.duplicate(true)
	buffered_ack["request_id"] = buffered_hello["request_id"]
	buffered_peer.incoming.append(JSON.stringify(buffered_ack).to_utf8_buffer())
	buffered_peer.status = WebSocketPeer.STATE_CLOSED
	buffered_client.poll(0.1)
	_assert(buffered_peer.received_packet_count == 1, "queued packet is consumed before closed-state handling")
	_assert(_error_code(buffered_client) == "disconnected", "closed peer remains failed after buffered response")
	_assert(not buffered_client.is_ready(), "closed peer cannot stay ready")

	quit(1 if _failures > 0 else 0)

func _new_client(peer: FakePeer, timeout_seconds: float = 10.0) -> Object:
	return ProtocolAdapter.new(peer, _read_json(CATALOG_PATH), _read_json(RESPONSE_SCHEMA_PATH), timeout_seconds)

func _read_fixture(file_name: String) -> Dictionary:
	return _read_json(FIXTURE_DIR + file_name)

func _read_json(path: String) -> Dictionary:
	var file_path := ProjectSettings.globalize_path(path)
	var parsed: Variant = JSON.parse_string(FileAccess.get_file_as_string(file_path))
	return parsed as Dictionary if parsed is Dictionary else {}

func _error_code(adapter: Object) -> String:
	return str(adapter.last_error.get("code", ""))

func _error_path(adapter: Object) -> String:
	return str(adapter.last_error.get("path", ""))

func _assert(condition: bool, message: String) -> void:
	if not condition:
		_failures += 1
		push_error(message)
