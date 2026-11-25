"""Tests of the zone hook of the bundled gateway (config/gateway/koodam_zone.py)."""
import asyncio
import contextlib
import importlib.util
import io
import json
import os
import pathlib
import sys
import types
import unittest
from unittest import mock

HOOK = pathlib.Path(__file__).resolve().parent.parent / "config" / "gateway" / "koodam_zone.py"
CHART_COPY = HOOK.parent.parent.parent / "charts" / "koodam" / "files" / "litellm-zone-hook.py"


class HTTPError(Exception):
    def __init__(self, status_code, detail):
        super().__init__(detail)
        self.status_code, self.detail = status_code, detail


def load(env):
    """Load the hook with stand-ins for the 2 modules that only the gateway image has."""
    fastapi = types.ModuleType("fastapi")
    fastapi.HTTPException = HTTPError
    logger = types.ModuleType("litellm.integrations.custom_logger")
    logger.CustomLogger = type("CustomLogger", (), {})
    modules = {"fastapi": fastapi, "litellm": types.ModuleType("litellm"),
               "litellm.integrations": types.ModuleType("litellm.integrations"),
               "litellm.integrations.custom_logger": logger}
    spec = importlib.util.spec_from_file_location("koodam_zone_under_test", HOOK)
    module = importlib.util.module_from_spec(spec)
    with mock.patch.dict(sys.modules, modules), mock.patch.dict(os.environ, env, clear=True):
        spec.loader.exec_module(module)
    return module


def call(module, data):
    out = io.StringIO()
    with contextlib.redirect_stdout(out):
        try:
            result = asyncio.run(module.handler.async_pre_call_hook(None, None, data, "completion"))
        except HTTPError as err:
            return err, out.getvalue()
    return result, out.getvalue()


class ZoneHookTest(unittest.TestCase):
    ENV = {"KOODAM_ZONE": "zone-a", "KOODAM_OUT_OF_ZONE_MODELS": '["model-b", "a,b"]'}

    def test_refuses_a_route_of_a_different_zone(self):
        module = load(self.ENV)
        for name in ("model-b", "a,b"):
            err, out = call(module, {"model": name, "messages": [{"role": "user", "content": "secret text"}]})
            self.assertIsInstance(err, HTTPError)
            self.assertEqual(err.status_code, 403)
            self.assertTrue(err.detail["error"].startswith("RouteOutOfZone: "))
            self.assertIn("zone-a", err.detail["error"])
            self.assertEqual(json.loads(out),
                             {"kind": "gateway-zone", "outcome": "RouteOutOfZone", "zone": "zone-a", "model": name})
            self.assertNotIn("secret text", out)

    def test_passes_each_other_call(self):
        module = load(self.ENV)
        for data in ({"model": "model-a"}, {"model": "a"}, {"model": ["model-b"]}, {"model": None}, {}, "text"):
            result, out = call(module, data)
            self.assertIs(result, data)
            self.assertEqual(out, "")

    def test_no_list_refuses_nothing(self):
        for env in ({}, {"KOODAM_ZONE": "zone-a"}, {"KOODAM_OUT_OF_ZONE_MODELS": "[]"}):
            data = {"model": "model-b"}
            self.assertIs(call(load(env), data)[0], data)

    def test_a_bad_list_stops_the_gateway(self):
        for text in ('"model-b"', '{"model-b": 1}', "[1]", "model-b"):
            with self.assertRaises(ValueError):
                load({"KOODAM_OUT_OF_ZONE_MODELS": text})

    def test_chart_copy_is_the_source(self):
        self.assertEqual(CHART_COPY.read_bytes(), HOOK.read_bytes())


if __name__ == "__main__":
    unittest.main()
