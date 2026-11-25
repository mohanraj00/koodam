# Copyright 2026 Mohan Raj.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
"""The zone hook of the bundled LiteLLM gateway.

The chart removes each model route of a different zone from the route list. This hook gives a
call for the model name of a removed route the status 403 and the typed reason RouteOutOfZone.
The chart mounts this file adjacent to the LiteLLM configuration and sets the 2 variables.
docs/zones.md has the rules.
"""
import json
import os
import sys

from fastapi import HTTPException
from litellm.integrations.custom_logger import CustomLogger

REASON = "RouteOutOfZone"


def load_models(text):
    """Give back the set of the model names in a JSON list of strings."""
    names = json.loads(text or "[]")
    if not isinstance(names, list) or not all(isinstance(n, str) for n in names):
        raise ValueError("KOODAM_OUT_OF_ZONE_MODELS is not a JSON list of strings")
    return frozenset(names)


ZONE = os.environ.get("KOODAM_ZONE", "")
OUT_OF_ZONE = load_models(os.environ.get("KOODAM_OUT_OF_ZONE_MODELS", ""))


class ZoneHook(CustomLogger):
    """Refuse a call for a model route of a different zone before LiteLLM routes it."""

    async def async_pre_call_hook(self, user_api_key_dict, cache, data, call_type):
        model = data.get("model") if isinstance(data, dict) else None
        if isinstance(model, str) and model in OUT_OF_ZONE:
            # The line has no key and no request data. The model name is a name of the chart
            # values, because it is in OUT_OF_ZONE.
            line = {"kind": "gateway-zone", "outcome": REASON, "zone": ZONE, "model": model}
            print(json.dumps(line), file=sys.stdout, flush=True)
            raise HTTPException(
                status_code=403,
                detail={"error": REASON + ": the model route is not in the zone " + ZONE + "."},
            )
        return data


handler = ZoneHook()
