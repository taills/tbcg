"""验证地图跨端工具会检查语义、字节确定性，并清理临时产物。"""

from __future__ import annotations

import importlib.util
import json
from pathlib import Path
from types import ModuleType

import pytest


@pytest.fixture
def map_probe(monkeypatch: pytest.MonkeyPatch) -> ModuleType:
    tools = Path(__file__).resolve().parents[1]
    monkeypatch.syspath_prepend(str(tools))
    spec = importlib.util.spec_from_file_location(
        "verify_map_export", tools / "verify_map_export.py"
    )
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


@pytest.mark.parametrize("fault", ["none", "wrong_data", "nondeterministic"])
def test_export_contract_and_cleanup(
    map_probe: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    fault: str,
) -> None:
    exports: list[Path] = []
    validated: list[list[str]] = []
    golden = json.loads(
        (map_probe.ROOT / "tests/fixtures/maps/training-ground.json").read_text()
    )

    def fake_run(command: list[str], **kwargs: object) -> str:
        if "--version" in command:
            return "4.7.1.stable"
        if "--output" in command:
            output = Path(command[command.index("--output") + 1])
            payload = {**golden, "map_id": "wrong"} if fault == "wrong_data" else golden
            suffix = "\n" if fault == "nondeterministic" and exports else ""
            output.write_text(json.dumps(payload, sort_keys=True) + suffix)
            exports.append(output)
        else:
            validated.append(command)
        return ""

    monkeypatch.setattr(map_probe, "run", fake_run)
    if fault == "none":
        map_probe.verify_map("unused")
        assert len(exports) == 2
        assert len(validated) == 1 and "./cmd/map-validate" in validated[0]
    else:
        with pytest.raises(RuntimeError):
            map_probe.verify_map("unused")
        assert not validated
    assert exports and all(not path.exists() for path in exports)
