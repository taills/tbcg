"""联调工具的退出码、工具选择和资源清理测试；真实通信另由联调命令验证。"""

from __future__ import annotations

import importlib.util
import subprocess
from pathlib import Path

import pytest

SCRIPT = Path(__file__).resolve().parents[1] / "verify_protocol.py"
SPEC = importlib.util.spec_from_file_location("verify_protocol", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
probe = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(probe)


def test_run_respects_expected_exit_code(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(
        probe.subprocess,
        "run",
        lambda *args, **kwargs: subprocess.CompletedProcess(args, 0, "ok", ""),
    )
    assert probe.run(["unused"]) == "ok"
    with pytest.raises(RuntimeError):
        probe.run(["unused"], expected_success=False)
    monkeypatch.setattr(
        probe.subprocess,
        "run",
        lambda *args, **kwargs: subprocess.CompletedProcess(args, 1, "", "rejected"),
    )
    assert (
        probe.run(["unused"], expected_success=False, required_output="rejected")
        == "rejected"
    )
    with pytest.raises(RuntimeError):
        probe.run(
            ["unused"], expected_success=False, required_output="TLS handshake error"
        )
    with pytest.raises(RuntimeError):
        probe.run(["unused"])


def test_script_error_fails_even_with_zero_exit(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        probe.subprocess,
        "run",
        lambda *args, **kwargs: subprocess.CompletedProcess(
            args, 0, "SCRIPT ERROR: invalid schema", ""
        ),
    )
    with pytest.raises(RuntimeError):
        probe.run(["godot"])


def test_timeout_preserves_script_diagnostics(monkeypatch: pytest.MonkeyPatch) -> None:
    def timed_out(*args: object, **kwargs: object) -> None:
        raise subprocess.TimeoutExpired(
            "godot", 1, output=b"SCRIPT ERROR: missing helper", stderr=b"details"
        )

    monkeypatch.setattr(probe.subprocess, "run", timed_out)
    with pytest.raises(RuntimeError, match="SCRIPT ERROR: missing helper"):
        probe.run(["godot"])


def test_explicit_godot_path(tmp_path: Path) -> None:
    executable = tmp_path / "godot"
    executable.write_text("placeholder", encoding="utf-8")
    assert probe.godot_binary(str(executable)) == str(executable)


def test_certificate_uses_loopback_and_private_key_permissions(tmp_path: Path) -> None:
    key, cert = probe.certificate_files(tmp_path)
    assert key.stat().st_mode & 0o777 == 0o600
    assert "BEGIN CERTIFICATE" in cert.read_text()
    config = (tmp_path / "certificate.cnf").read_text()
    assert "IP:127.0.0.1" in config
    assert "DNS:localhost" in config


def test_bad_godot_version_stops_before_server(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(probe, "run", lambda *args, **kwargs: "4.6.0.stable")
    with pytest.raises(RuntimeError, match="4.7.1"):
        probe.verify("unused")


@pytest.mark.parametrize("fail_probe", [False, True])
def test_verify_always_stops_owned_process(
    monkeypatch: pytest.MonkeyPatch,
    fail_probe: bool,
) -> None:
    events: list[str] = []
    temporary_paths: list[Path] = []

    class Process:
        def poll(self) -> None:
            return None

        def terminate(self) -> None:
            events.append("terminate")

        def wait(self, timeout: int) -> int:
            events.append("wait")
            return 0

    def fake_run(command: list[str], **kwargs: object) -> str:
        if "--version" in command:
            return "4.7.1.stable"
        if "--script" in command:
            events.append("probe")
            if fail_probe:
                raise RuntimeError("injected failure")
            return "Leaf WebSocket HelloAck and Ping/Pong probe passed"
        return ""

    def fake_certs(directory: Path) -> tuple[Path, Path]:
        temporary_paths.append(directory)
        return directory / "server.key", directory / "server.crt"

    monkeypatch.setattr(probe, "run", fake_run)
    monkeypatch.setattr(probe, "certificate_files", fake_certs)
    monkeypatch.setattr(probe, "unused_port", lambda: 49152)
    monkeypatch.setattr(probe, "wait_for_server", lambda *args: None)
    monkeypatch.setattr(probe.subprocess, "Popen", lambda *args, **kwargs: Process())
    if fail_probe:
        with pytest.raises(RuntimeError, match="injected failure"):
            probe.verify("unused")
    else:
        probe.verify("unused")
        assert events.count("probe") == 3
    assert events[-2:] == ["terminate", "wait"]
    assert temporary_paths and all(not path.exists() for path in temporary_paths)
