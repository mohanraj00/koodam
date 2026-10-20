# A fake upstream model for the e2e tests. It answers POST /v1/responses with a fixed text, as
# a stream of server-sent events or as one JSON object. The gateway sends the Codex calls to it,
# because the LiteLLM mock_response cannot stream the Responses API. It reads no key and calls
# no real model.
import json
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TEXT = "mock answer"


def response(model, status, output):
    return {
        "id": "resp_fake", "object": "response", "created_at": int(time.time()),
        "status": status, "model": model, "output": output, "parallel_tool_calls": False,
        "tool_choice": "auto", "tools": [], "temperature": 1.0, "top_p": 1.0,
        "usage": {"input_tokens": 10, "output_tokens": 2, "total_tokens": 12,
                  "input_tokens_details": {"cached_tokens": 0},
                  "output_tokens_details": {"reasoning_tokens": 0}},
    }


def message(status, text):
    return {"id": "msg_fake", "type": "message", "role": "assistant", "status": status,
            "content": [{"type": "output_text", "text": text, "annotations": []}] if text else []}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        self.send_json({"status": "ok"})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
        model = body.get("model", "fake")
        if not self.path.endswith("/responses"):
            self.send_json({"error": {"message": "not found"}}, 404)
            return
        if not body.get("stream"):
            self.send_json(response(model, "completed", [message("completed", TEXT)]))
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Connection", "close")
        self.end_headers()
        part = {"type": "output_text", "text": "", "annotations": []}
        done = dict(part, text=TEXT)
        events = [
            {"type": "response.created", "response": response(model, "in_progress", [])},
            {"type": "response.output_item.added", "output_index": 0, "item": message("in_progress", "")},
            {"type": "response.content_part.added", "item_id": "msg_fake", "output_index": 0,
             "content_index": 0, "part": part},
            {"type": "response.output_text.delta", "item_id": "msg_fake", "output_index": 0,
             "content_index": 0, "delta": TEXT},
            {"type": "response.output_text.done", "item_id": "msg_fake", "output_index": 0,
             "content_index": 0, "text": TEXT},
            {"type": "response.content_part.done", "item_id": "msg_fake", "output_index": 0,
             "content_index": 0, "part": done},
            {"type": "response.output_item.done", "output_index": 0, "item": message("completed", TEXT)},
            {"type": "response.completed",
             "response": response(model, "completed", [message("completed", TEXT)])},
        ]
        for n, e in enumerate(events):
            e["sequence_number"] = n
            self.wfile.write(("event: %s\ndata: %s\n\n" % (e["type"], json.dumps(e))).encode())
        self.wfile.flush()
        self.close_connection = True

    def send_json(self, obj, code=200):
        b = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)


ThreadingHTTPServer(("", 8080), Handler).serve_forever()
