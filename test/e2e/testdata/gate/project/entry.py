"""The entry of the gate project of the kind test.

Nookku starts this program for each test of the gate, and the program speaks the agent contract
on stdin and stdout. It sends each message to the app of the AppVersion. KOODAM_GATE_TARGET has
the host and the port of the Service of the version.
"""

import os
import urllib.request

from nookku.agent import serve


def reply(message: str, history: list) -> str:
    url = "http://" + os.environ["KOODAM_GATE_TARGET"] + "/chat"
    request = urllib.request.Request(url, data=message.encode("utf-8"), method="POST")
    with urllib.request.urlopen(request, timeout=20) as response:
        return response.read().decode("utf-8")


serve(reply)
