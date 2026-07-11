from __future__ import annotations

from chic.app import create_app
from fastapi.testclient import TestClient


def test_healthz_ok() -> None:
    client = TestClient(create_app(wire=False))
    resp = client.get("/healthz")
    assert resp.status_code == 200
    body = resp.json()
    assert body["status"] == "ok"
    assert "version" in body["build"]
    assert "python" in body["build"]
