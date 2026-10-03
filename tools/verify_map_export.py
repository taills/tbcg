#!/usr/bin/env python3
"""从真实 Godot 场景导出两次，并由 Go 校验器验证确定性地图快照。"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import tempfile
from pathlib import Path

from verify_protocol import ROOT, godot_binary, run


def verify_map(godot: str) -> None:
    env = {**os.environ, "GOTOOLCHAIN": "go1.26.4"}
    version = run([godot, "--version"], env=env).strip()
    if not version.startswith("4.7.1."):
        raise RuntimeError(f"需要 Godot 4.7.1，实际为 {version}")
    expected = json.loads(
        (ROOT / "tests/fixtures/maps/training-ground.json").read_text()
    )
    with tempfile.TemporaryDirectory(prefix="tbcg-map-") as temporary:
        exports = [Path(temporary) / "first.json", Path(temporary) / "second.json"]
        for output in exports:
            run(
                [
                    godot,
                    "--headless",
                    "--path",
                    str(ROOT / "client"),
                    "--script",
                    "res://tests/maps/export_probe.gd",
                    "--",
                    "--output",
                    str(output),
                ],
                env=env,
            )
            if json.loads(output.read_text()) != expected:
                raise RuntimeError("Godot 导出的空间语义与共享 golden fixture 不一致")
        if exports[0].read_bytes() != exports[1].read_bytes():
            raise RuntimeError("同一场景重复导出结果不确定")
        run(
            [
                "go",
                "run",
                "./cmd/map-validate",
                "--contracts",
                str(ROOT / "contracts"),
                "--input",
                str(exports[0]),
            ],
            env=env,
            cwd=ROOT / "server",
            timeout_seconds=300,
        )
    print("地图跨端验证通过：真实场景、确定性导出、共享 golden 与 Go 校验一致。")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--godot", help="Godot 4.7.1 可执行文件路径")
    args = parser.parse_args()
    try:
        verify_map(godot_binary(args.godot))
    except (OSError, RuntimeError, ValueError, subprocess.SubprocessError) as error:
        parser.exit(1, f"地图跨端验证失败：{error}\n")


if __name__ == "__main__":
    main()
