#!/usr/bin/env python3
"""以临时证书、随机令牌和 loopback 服务验证真实 Leaf/Godot WSS 通信。"""

from __future__ import annotations

import argparse
import os
import secrets
import shutil
import socket
import ssl
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Mapping

ROOT = Path(__file__).resolve().parents[1]
TIMEOUT_SECONDS = 60


def run(
    command: list[str],
    *,
    env: Mapping[str, str] | None = None,
    cwd: Path = ROOT,
    expected_success: bool = True,
    timeout_seconds: int = TIMEOUT_SECONDS,
    required_output: str | None = None,
) -> str:
    result = subprocess.run(
        command,
        cwd=cwd,
        env=env,
        text=True,
        capture_output=True,
        timeout=timeout_seconds,
    )
    output = result.stdout + result.stderr
    if "SCRIPT ERROR:" in output or (result.returncode == 0) != expected_success:
        raise RuntimeError(f"验证命令失败：{command[0]}\n{output}")
    if required_output is not None and required_output not in output:
        raise RuntimeError(f"未观察到预期验证阶段 {required_output}：\n{output}")
    return output


def godot_binary(explicit: str | None) -> str:
    candidates = [
        explicit,
        os.environ.get("GODOT"),
        shutil.which("godot"),
        "/Applications/Godot.app/Contents/MacOS/Godot",
    ]
    for candidate in candidates:
        if candidate and Path(candidate).is_file():
            return str(Path(candidate).resolve())
    raise RuntimeError("找不到 Godot；请传入 --godot 或设置 GODOT")


def certificate_files(directory: Path) -> tuple[Path, Path]:
    config = directory / "certificate.cnf"
    config.write_text(
        "[req]\ndistinguished_name=dn\nx509_extensions=extensions\nprompt=no\n"
        "[dn]\nCN=localhost\n"
        "[extensions]\nsubjectAltName=DNS:localhost,IP:127.0.0.1\n"
        "basicConstraints=critical,CA:TRUE\n"
        "keyUsage=critical,digitalSignature,keyEncipherment,keyCertSign\n"
        "extendedKeyUsage=serverAuth\n",
        encoding="utf-8",
    )
    key, cert = directory / "server.key", directory / "server.crt"
    run(
        [
            "openssl",
            "req",
            "-x509",
            "-newkey",
            "rsa:2048",
            "-nodes",
            "-days",
            "1",
            "-config",
            str(config),
            "-keyout",
            str(key),
            "-out",
            str(cert),
        ]
    )
    key.chmod(0o600)
    return key, cert


def unused_port() -> int:
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def wait_for_server(process: subprocess.Popen, port: int, cert: Path) -> None:
    tls = ssl.create_default_context(cafile=str(cert))
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError("Leaf probe 在完成 TLS 就绪检查前退出")
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.5) as raw:
                with tls.wrap_socket(raw, server_hostname="localhost"):
                    return
        except (ConnectionRefusedError, TimeoutError):
            time.sleep(0.05)
    raise RuntimeError("Leaf probe 未在限定时间内启动")


def verify(godot: str) -> None:
    env = {**os.environ, "GOTOOLCHAIN": "go1.26.4"}
    version = run([godot, "--version"], env=env).strip()
    if not version.startswith("4.7.1."):
        raise RuntimeError(f"需要 Godot 4.7.1，实际为 {version}")
    with tempfile.TemporaryDirectory(prefix="tbcg-wss-") as temp:
        directory = Path(temp)
        key, cert = certificate_files(directory)
        untrusted_directory = directory / "untrusted"
        untrusted_directory.mkdir(mode=0o700)
        _, untrusted_cert = certificate_files(untrusted_directory)
        executable = directory / "protocol-probe"
        run(
            ["go", "build", "-o", str(executable), "./cmd/protocol-probe"],
            env=env,
            cwd=ROOT / "server",
            timeout_seconds=300,
        )
        port, token = unused_port(), secrets.token_urlsafe(32)
        env = {
            **env,
            "TBCG_PROTOCOL_PROBE_PORT": str(port),
            "TBCG_PROTOCOL_PROBE_TLS_CERT": str(cert),
            "TBCG_PROTOCOL_PROBE_TLS_KEY": str(key),
            "TBCG_PROTOCOL_PROBE_TOKEN": token,
            "TBCG_WS_URL": f"wss://127.0.0.1:{port}",
            "TBCG_WS_CA_CERT": str(cert),
            "TBCG_SESSION_TOKEN": token,
            "TBCG_PROTOCOL_PROBE_RELEASE_ID": "demo-release-1",
            "TBCG_GAME_ID": "demo",
            "TBCG_REALM_ID": "local-test",
            "TBCG_CLIENT_BUILD": "0.0.1-test",
            "TBCG_RELEASE_ID": "demo-release-1",
        }
        command = [
            godot,
            "--headless",
            "--path",
            str(ROOT / "client"),
            "--script",
            "res://tests/network/protocol_loopback_probe.gd",
        ]
        with (directory / "server.log").open("w", encoding="utf-8") as log:
            process = subprocess.Popen(
                [str(executable)], env=env, cwd=ROOT, stdout=log, stderr=log
            )
            try:
                wait_for_server(process, port, cert)
                output = run(command, env=env)
                if "Leaf WebSocket HelloAck and Ping/Pong probe passed" not in output:
                    raise RuntimeError("Godot 未产生成功联调标记")
                run(
                    command,
                    env={**env, "TBCG_SESSION_TOKEN": secrets.token_urlsafe(32)},
                    expected_success=False,
                    required_output="HelloAck was not validated",
                )
                run(
                    command,
                    env={**env, "TBCG_WS_CA_CERT": str(untrusted_cert)},
                    expected_success=False,
                    required_output="TLS handshake error",
                )
                if process.poll() is not None:
                    raise RuntimeError("负向校验期间 Leaf probe 意外退出")
                print(
                    "WSS 联调通过：可信证书握手、HelloAck、Ping/Pong；错误令牌与不受信证书均拒绝。"
                )
            finally:
                if process.poll() is None:
                    process.terminate()
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait(timeout=10)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--godot", help="Godot 4.7.1 可执行文件路径")
    args = parser.parse_args()
    try:
        verify(godot_binary(args.godot))
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        parser.exit(1, f"WSS 联调失败：{error}\n")


if __name__ == "__main__":
    main()
