extends Node
## 玩家账号与公开包的 HTTP 访问；只下载公开白名单包，私有包没有任何客户端路径。

var tls_options: TLSOptions = null

func request_json(method: int, url: String, body: Variant = null) -> Dictionary:
	var http := HTTPRequest.new()
	http.timeout = 15.0
	if tls_options != null:
		http.set_tls_options(tls_options)
	add_child(http)
	var headers := PackedStringArray(["Content-Type: application/json", "Accept: application/json"])
	var payload := "" if body == null else JSON.stringify(body)
	var error := http.request(url, headers, method, payload)
	if error != OK:
		http.queue_free()
		return {"success": false, "error": {"code": "network", "message": "请求失败"}}
	var result: Array = await http.request_completed
	http.queue_free()
	if int(result[0]) != HTTPRequest.RESULT_SUCCESS:
		return {"success": false, "error": {"code": "network", "message": "网络错误"}}
	var parsed: Variant = JSON.parse_string((result[3] as PackedByteArray).get_string_from_utf8())
	if not parsed is Dictionary:
		return {"success": false, "error": {"code": "invalid_response", "message": "服务器响应无效"}}
	return parsed

func get_bytes(url: String) -> Dictionary:
	var http := HTTPRequest.new()
	http.timeout = 30.0
	http.body_size_limit = 16 * 1024 * 1024
	if tls_options != null:
		http.set_tls_options(tls_options)
	add_child(http)
	var error := http.request(url)
	if error != OK:
		http.queue_free()
		return {"ok": false}
	var result: Array = await http.request_completed
	http.queue_free()
	return {"ok": int(result[0]) == HTTPRequest.RESULT_SUCCESS and int(result[1]) == 200, "status": int(result[1]), "body": result[3]}
