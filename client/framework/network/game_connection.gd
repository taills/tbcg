extends Node
## 游戏 WSS 连接：按协议目录校验响应信封，按 request_id 匹配响应，push- 前缀的消息作为服务端推送分发。
## 客户端只发送意图；所有权威结果来自服务端响应与推送。

signal pushed(message_type: String, data: Dictionary)
signal disconnected(reason: String)

const RESPONSE_FIELDS := ["protocol_version", "request_id", "type", "success", "server_seq", "state_version", "data", "error", "meta"]
const REQUEST_TIMEOUT := 15.0

class Pending extends RefCounted:
	signal done(response: Dictionary)
	var elapsed := 0.0

var _peer := WebSocketPeer.new()
var _pending := {}
var _next := 1
var _protocol_version := "1"
var _max_bytes := 65536
var _server_types := {}
var _response_types := {}
var _open := false
var last_error := ""

func load_catalog(catalog: Dictionary) -> bool:
	if not catalog.get("messages") is Array:
		last_error = "协议目录无效"
		return false
	_protocol_version = str(catalog.get("protocol_version", "1"))
	_max_bytes = int(catalog.get("transport", {}).get("max_message_bytes", 65536))
	for entry in catalog["messages"]:
		if entry.get("direction", "") == "server_to_client":
			_server_types[entry["type"]] = true
		elif entry.get("direction", "") == "client_to_server":
			_response_types[entry["type"]] = str(entry.get("response_type", ""))
	return true

func is_open() -> bool:
	return _open and _peer.get_ready_state() == WebSocketPeer.STATE_OPEN

func open(url: String, tls_options: TLSOptions = null) -> bool:
	last_error = ""
	var error := _peer.connect_to_url(url, tls_options)
	if error != OK:
		last_error = "无法连接 %s" % url
		return false
	var waited := 0.0
	while waited < 10.0:
		_peer.poll()
		var state := _peer.get_ready_state()
		if state == WebSocketPeer.STATE_OPEN:
			_open = true
			return true
		if state == WebSocketPeer.STATE_CLOSED:
			break
		await get_tree().process_frame
		waited += get_process_delta_time()
	last_error = "连接超时或被拒绝"
	return false

func close() -> void:
	_peer.close()
	_open = false

func request(message_type: String, payload: Dictionary) -> Dictionary:
	if not is_open():
		return _local_failure("disconnected", "未连接服务器")
	if not _response_types.has(message_type):
		return _local_failure("unknown_type", "消息类型不在协议目录中")
	var request_id := "godot-%d" % _next
	_next += 1
	var envelope := {"protocol_version": _protocol_version, "request_id": request_id, "type": message_type, "payload": payload}
	var packet := JSON.stringify(envelope).to_utf8_buffer()
	if packet.size() > _max_bytes:
		return _local_failure("too_large", "请求超过协议大小限制")
	var pending := Pending.new()
	_pending[request_id] = pending
	if _peer.send(packet, WebSocketPeer.WRITE_MODE_BINARY) != OK:
		_pending.erase(request_id)
		return _local_failure("send_failed", "发送失败")
	var response: Dictionary = await pending.done
	return response

func _local_failure(code: String, message: String) -> Dictionary:
	return {"success": false, "data": {}, "error": {"code": code, "message": message}, "state_version": null}

func _process(delta: float) -> void:
	if not _open:
		return
	_peer.poll()
	while _peer.get_available_packet_count() > 0:
		var packet := _peer.get_packet()
		if _peer.was_string_packet():
			_fail_connection("服务器发送了非二进制帧")
			return
		_consume(packet)
	for request_id in _pending.keys():
		var pending: Pending = _pending[request_id]
		pending.elapsed += delta
		if pending.elapsed > REQUEST_TIMEOUT:
			_pending.erase(request_id)
			pending.done.emit(_local_failure("timeout", "请求超时，结果未知；请使用同一幂等键重试"))
	if _peer.get_ready_state() == WebSocketPeer.STATE_CLOSED:
		_fail_connection("连接已关闭")

func _fail_connection(reason: String) -> void:
	_open = false
	for request_id in _pending.keys():
		_pending[request_id].done.emit(_local_failure("disconnected", reason))
	_pending.clear()
	disconnected.emit(reason)

func _consume(packet: PackedByteArray) -> void:
	if packet.size() > _max_bytes:
		_fail_connection("消息超过大小限制")
		return
	var parsed: Variant = JSON.parse_string(packet.get_string_from_utf8())
	if not parsed is Dictionary:
		_fail_connection("服务器消息不是 JSON 对象")
		return
	var message: Dictionary = parsed
	for field in RESPONSE_FIELDS:
		if not message.has(field):
			_fail_connection("响应缺少字段 %s" % field)
			return
	if str(message["protocol_version"]) != _protocol_version or not _server_types.has(str(message["type"])):
		_fail_connection("响应类型或协议版本不受支持")
		return
	if not message["data"] is Dictionary and message["data"] != null:
		_fail_connection("响应 data 必须是对象")
		return
	var request_id := str(message["request_id"])
	if _pending.has(request_id):
		var pending: Pending = _pending[request_id]
		_pending.erase(request_id)
		pending.done.emit(message)
	elif request_id.begins_with("push-"):
		var data: Dictionary = message["data"] if message["data"] is Dictionary else {}
		pushed.emit(str(message["type"]), data)
