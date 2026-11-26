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
"""The fake upstream model of the guard image check. It runs in the gateway container.

It writes the body of each request as one JSON line to the file in argv[2], and it answers chat
completions, Responses and Anthropic Messages calls with a fixed text, with or without a stream.
It calls nothing.
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TEXT = "mock answer"
USAGE = {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}


def response(status, text):
    content = [{"type": "output_text", "text": text, "annotations": []}] if text else []
    return {"id": "resp_check", "object": "response", "created_at": 1, "status": status, "model": "check",
            "output": [{"id": "msg_check", "type": "message", "role": "assistant", "status": status,
                        "content": content}],
            "parallel_tool_calls": False, "tool_choice": "auto", "tools": [],
            "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}


def chat_events():
    chunk = {"id": "c1", "object": "chat.completion.chunk", "created": 1, "model": "check"}
    yield dict(chunk, choices=[{"index": 0, "delta": {"role": "assistant", "content": TEXT}, "finish_reason": None}])
    yield dict(chunk, choices=[{"index": 0, "delta": {}, "finish_reason": "stop"}], usage=USAGE)


def response_events():
    done = {"type": "output_text", "text": TEXT, "annotations": []}
    yield {"type": "response.created", "response": response("in_progress", "")}
    yield {"type": "response.output_text.delta", "item_id": "msg_check", "output_index": 0, "content_index": 0,
           "delta": TEXT}
    yield {"type": "response.content_part.done", "item_id": "msg_check", "output_index": 0, "content_index": 0,
           "part": done}
    yield {"type": "response.completed", "response": response("completed", TEXT)}


def message_events():
    message = {"id": "msg_check", "type": "message", "role": "assistant", "model": "check", "content": [],
               "stop_reason": None, "usage": {"input_tokens": 1, "output_tokens": 0}}
    yield {"type": "message_start", "message": message}
    yield {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}
    yield {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": TEXT}}
    yield {"type": "content_block_stop", "index": 0}
    yield {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"output_tokens": 1}}
    yield {"type": "message_stop"}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        with open(sys.argv[2], "ab") as out:
            out.write(json.dumps(json.loads(raw or b"{}"), separators=(",", ":")).encode() + b"\n")
        body = json.loads(raw or b"{}")
        path = self.path.split("?")[0].rstrip("/")
        responses, anthropic = path.endswith("/responses"), path.endswith("/messages")
        if not body.get("stream") and anthropic:
            self.send_json({"id": "msg_check", "type": "message", "role": "assistant", "model": "check",
                            "content": [{"type": "text", "text": TEXT}], "stop_reason": "end_turn",
                            "usage": {"input_tokens": 1, "output_tokens": 1}})
            return
        if not body.get("stream"):
            self.send_json(response("completed", TEXT) if responses else {
                "id": "c1", "object": "chat.completion", "created": 1, "model": "check", "usage": USAGE,
                "choices": [{"index": 0, "message": {"role": "assistant", "content": TEXT}, "finish_reason": "stop"}]})
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Connection", "close")
        self.end_headers()
        events = response_events() if responses else message_events() if anthropic else chat_events()
        for event in events:
            prefix = "event: %s\n" % event["type"] if responses or anthropic else ""
            self.wfile.write((prefix + "data: " + json.dumps(event) + "\n\n").encode())
        if not responses and not anthropic:
            self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()
        self.close_connection = True

    def send_json(self, obj):
        data = json.dumps(obj).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *args):
        pass


ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
