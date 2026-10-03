extends SceneTree

const ProtocolAdapter = preload("res://framework/network/protocol_adapter.gd")
const CATALOG_PATH := "res://../contracts/protocol/message_catalog.json"
const RESPONSE_SCHEMA_PATH := "res://../contracts/protocol/response.schema.json"
const TIMEOUT_SECONDS := 15.0

func _initialize() -> void:
	call_deferred("_run")

func _run() -> void:
	var url := OS.get_environment("TBCG_WS_URL")
	if not _is_loopback_wss_url(url):
		_abort("TBCG_WS_URL must be a loopback wss:// URL")
		return
	var session_token := OS.get_environment("TBCG_SESSION_TOKEN")
	if session_token.is_empty():
		_abort("TBCG_SESSION_TOKEN is required for the real Leaf probe")
		return
	var ca_path := OS.get_environment("TBCG_WS_CA_CERT")
	if ca_path.is_empty():
		_abort("TBCG_WS_CA_CERT is required for TLS verification")
		return

	var certificate := X509Certificate.new()
	if certificate.load(ca_path) != OK:
		_abort("TBCG_WS_CA_CERT could not be loaded as an X509Certificate")
		return
	var tls_options := TLSOptions.client(certificate)
	var peer := WebSocketPeer.new()
	var connect_error := peer.connect_to_url(url, tls_options)
	if connect_error != OK:
		_abort("WebSocket connection failed: %s" % error_string(connect_error))
		return

	var elapsed := 0.0
	while peer.get_ready_state() == WebSocketPeer.STATE_CONNECTING and elapsed < TIMEOUT_SECONDS:
		peer.poll()
		await create_timer(0.05).timeout
		elapsed += 0.05
	if peer.get_ready_state() != WebSocketPeer.STATE_OPEN:
		_abort("WebSocket did not open before timeout")
		return

	var catalog := _read_contract(CATALOG_PATH)
	var response_schema := _read_contract(RESPONSE_SCHEMA_PATH)
	if catalog.is_empty() or response_schema.is_empty():
		_abort("Trusted protocol contracts could not be loaded")
		return
	var adapter = ProtocolAdapter.new(peer, catalog, response_schema, TIMEOUT_SECONDS)
	var hello := {
		"game_id": _env_or("TBCG_GAME_ID", "demo"),
		"realm_id": _env_or("TBCG_REALM_ID", "local-test"),
		"client_build": _env_or("TBCG_CLIENT_BUILD", "0.0.1-test"),
		"release_id": _env_or("TBCG_RELEASE_ID", "demo-release-1"),
		"session_token": session_token,
		"capabilities": ["protocol.v1"]
	}
	if not adapter.start_handshake(hello):
		_abort("Hello could not be sent: %s" % str(adapter.last_error))
		return
	if adapter.is_ready():
		_abort("Client became ready before HelloAck")
		return

	elapsed = 0.0
	while not adapter.is_ready() and adapter.last_error.is_empty() and elapsed < TIMEOUT_SECONDS:
		adapter.poll(0.05)
		await create_timer(0.05).timeout
		elapsed += 0.05
	if not adapter.is_ready():
		_abort("HelloAck was not validated: %s" % str(adapter.last_error))
		return

	var nonce := "godot-probe-ping"
	if not adapter.send_business_message("system.ping", {"nonce": nonce}):
		_abort("Ping could not be sent after HelloAck")
		return
	elapsed = 0.0
	while elapsed < TIMEOUT_SECONDS:
		adapter.poll(0.05)
		if adapter.last_message.get("type", "") == "system.pong":
			var pong_data: Dictionary = adapter.last_message.get("data", {})
			if pong_data.get("nonce", "") != nonce:
				_abort("Pong nonce does not match Ping")
				return
			print("Leaf WebSocket HelloAck and Ping/Pong probe passed")
			peer.close()
			quit(0)
			return
		if not adapter.last_error.is_empty():
			_abort("Protocol probe failed: %s" % str(adapter.last_error))
			return
		await create_timer(0.05).timeout
		elapsed += 0.05
	_abort("Pong was not received before timeout")

func _is_loopback_wss_url(value: String) -> bool:
	var regex := RegEx.new()
	if regex.compile("^wss://(localhost|127\\.0\\.0\\.1|\\[::1\\])(?::([0-9]{1,5}))?(/[^?#]*)?$") != OK:
		return false
	var match := regex.search(value)
	if match == null:
		return false
	var port := match.get_string(2)
	return port.is_empty() or (int(port) >= 1 and int(port) <= 65535)

func _read_contract(path: String) -> Dictionary:
	var absolute_path := ProjectSettings.globalize_path(path)
	var parser := JSON.new()
	if parser.parse(FileAccess.get_file_as_string(absolute_path)) != OK or not parser.data is Dictionary:
		return {}
	return parser.data

func _env_or(name: String, default_value: String) -> String:
	var value := OS.get_environment(name)
	return default_value if value.is_empty() else value

func _abort(message: String) -> void:
	push_error(message)
	quit(1)
