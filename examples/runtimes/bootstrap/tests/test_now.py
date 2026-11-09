"""Runs bin/now against a fake of lambda's runtime api, as lambda would run it."""

import json
import os
import queue
import subprocess
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

BOOTSTRAP = os.path.join(os.path.dirname(__file__), "..", "bin", "now")
PREFIX = "/2018-06-01/runtime/invocation/"


class FakeRuntime(ThreadingHTTPServer):
    """The runtime api: it hands out the events it holds, one for each request of the next, keeps
    what the bootstrap posts back, by the request's id, and notes when the bootstrap asks for
    more than it holds, done with the last."""

    daemon_threads = True

    def __init__(self, events: list[tuple[str, dict]]):
        super().__init__(("127.0.0.1", 0), RuntimeRequests)
        self.events = queue.Queue()
        for event in events:
            self.events.put(event)
        self.posted = queue.Queue()
        self.waiting = threading.Event()
        self.done = threading.Event()


class RuntimeRequests(BaseHTTPRequestHandler):
    """What the fake runtime api answers: the next event, or a note of a response or an error."""

    server: FakeRuntime

    def do_GET(self):
        if self.path != PREFIX + "next":
            self.send_error(404)
            return
        try:
            request_id, event = self.server.events.get_nowait()
        except queue.Empty:
            self.server.waiting.set()
            self.server.done.wait(10)
            self.send_error(500)
            return
        body = json.dumps(event).encode()
        self.send_response(200)
        self.send_header("Lambda-Runtime-Aws-Request-Id", request_id)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        request_id, _, kind = self.path.removeprefix(PREFIX).partition("/")
        body = self.rfile.read(int(self.headers["Content-Length"]))
        self.server.posted.put((request_id, kind, json.loads(body), self.headers.get("Lambda-Runtime-Function-Error-Type")))
        self.send_response(202)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def log_message(self, format, *arguments):
        pass


def function_url_request(method: str) -> dict:
    """A request of a function url, as lambda hands it to the function."""
    return {"version": "2.0", "rawPath": "/", "requestContext": {"http": {"method": method, "path": "/"}}}


class NowTest(unittest.TestCase):
    """The bootstrap, run with sh, as provided.al2023 runs it."""

    def run_bootstrap(self, events: list[tuple[str, dict]], log_level: str = "INFO") -> tuple[dict, str]:
        """Hands the events to bin/now, and returns what it posted back, by request id, and its log."""
        runtime = FakeRuntime(events)
        threading.Thread(target=runtime.serve_forever, daemon=True).start()
        environment = {"PATH": os.environ["PATH"], "AWS_LAMBDA_RUNTIME_API": f"127.0.0.1:{runtime.server_address[1]}", "AWS_LAMBDA_LOG_LEVEL": log_level}
        process = subprocess.Popen(["sh", BOOTSTRAP], env=environment, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        posted = {}
        try:
            for _ in events:
                request_id, kind, body, error_type = runtime.posted.get(timeout=10)
                posted[request_id] = (kind, body, error_type)
            runtime.waiting.wait(10)
        finally:
            runtime.done.set()
            process.terminate()
            log, _ = process.communicate(timeout=10)
            runtime.shutdown()
            runtime.server_close()
        return posted, log

    def test_answers(self):
        examples = [
            ("a GET gets the time", "GET", 200, "application/json"),
            ("a HEAD gets the time", "HEAD", 200, "application/json"),
            ("a POST is not allowed", "POST", 405, "application/problem+json"),
        ]
        for name, method, status, content_type in examples:
            with self.subTest(name):
                posted, _ = self.run_bootstrap([("request-1", function_url_request(method))])
                kind, response, _ = posted["request-1"]
                self.assertEqual(kind, "response")
                self.assertEqual(response["statusCode"], status)
                self.assertEqual(response["headers"]["content-type"], content_type)
                self.assertEqual(response["headers"]["x-request-id"], "request-1")
                body = json.loads(response["body"])
                if status == 200:
                    self.assertRegex(body["time"], r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$")
                    self.assertIsInstance(body["epochSeconds"], int)
                else:
                    self.assertEqual(response["headers"]["allow"], "GET, HEAD")
                    self.assertEqual(body["status"], 405)

    def test_an_event_that_is_not_a_request_fails(self):
        posted, log = self.run_bootstrap([("request-1", {"source": "aws.events"})])
        kind, error, error_type = posted["request-1"]
        self.assertEqual(kind, "error")
        self.assertEqual(error_type, "Unhandled")
        self.assertEqual(error["errorType"], "NotARequest")
        self.assertIn('"level":"ERROR"', log)

    def test_one_instance_answers_one_invocation_after_another(self):
        posted, _ = self.run_bootstrap([("request-1", function_url_request("GET")), ("request-2", function_url_request("POST"))])
        self.assertEqual(posted["request-1"][1]["statusCode"], 200)
        self.assertEqual(posted["request-2"][1]["statusCode"], 405)

    def test_the_log_keeps_the_levels_lambda_asks_for(self):
        examples = [("INFO", True), ("WARN", False)]
        for level, logged in examples:
            with self.subTest(level):
                _, log = self.run_bootstrap([("request-1", function_url_request("GET"))], log_level=level)
                lines = [json.loads(line) for line in log.splitlines() if line.startswith("{")]
                self.assertEqual(any(line["message"] == "answered a request" for line in lines), logged)
                for line in lines:
                    self.assertEqual(line["requestId"], "request-1")


if __name__ == "__main__":
    unittest.main()
