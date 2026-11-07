# The runtime client koldstart runs python handlers with locally: it asks the runtime api for the
# next event, calls the handler, and posts what it returns or raises.
import importlib
import json
import os
import sys
import time
import traceback
import urllib.request

API = "http://" + os.environ["AWS_LAMBDA_RUNTIME_API"] + "/2018-06-01/runtime"


def request(method, path, body=None, headers=None):
    req = urllib.request.Request(API + path, data=body, method=method, headers=headers or {})
    with urllib.request.urlopen(req, timeout=None) as res:
        return res.headers, res.read()


def error(e):
    return json.dumps({
        "errorType": type(e).__name__,
        "errorMessage": str(e),
        "stackTrace": traceback.format_tb(e.__traceback__),
    }).encode()


class Context:
    def __init__(self, headers):
        self.aws_request_id = headers["Lambda-Runtime-Aws-Request-Id"]
        self.invoked_function_arn = headers.get("Lambda-Runtime-Invoked-Function-Arn", "")
        self.function_name = os.environ.get("AWS_LAMBDA_FUNCTION_NAME", "")
        self.function_version = os.environ.get("AWS_LAMBDA_FUNCTION_VERSION", "")
        self.memory_limit_in_mb = os.environ.get("AWS_LAMBDA_FUNCTION_MEMORY_SIZE", "")
        self.log_group_name = os.environ.get("AWS_LAMBDA_LOG_GROUP_NAME", "")
        self.log_stream_name = os.environ.get("AWS_LAMBDA_LOG_STREAM_NAME", "")
        self._deadline = int(headers.get("Lambda-Runtime-Deadline-Ms", "0"))

    def get_remaining_time_in_millis(self):
        return max(0, self._deadline - int(time.time() * 1000))


def main():
    module_name, function_name = sys.argv[1], sys.argv[2]
    sys.path.insert(0, os.environ["LAMBDA_TASK_ROOT"])
    try:
        handler = getattr(importlib.import_module(module_name.replace("/", ".")), function_name)
    except Exception as e:
        request("POST", "/init/error", error(e), {"Lambda-Runtime-Function-Error-Type": "Runtime.ImportModuleError"})
        sys.exit(1)
    while True:
        headers, body = request("GET", "/invocation/next")
        context = Context(headers)
        os.environ["_X_AMZN_TRACE_ID"] = headers.get("Lambda-Runtime-Trace-Id", "")
        try:
            result = handler(json.loads(body), context)
            request("POST", "/invocation/" + context.aws_request_id + "/response", json.dumps(result).encode())
        except Exception as e:
            request("POST", "/invocation/" + context.aws_request_id + "/error", error(e), {"Lambda-Runtime-Function-Error-Type": "Unhandled"})
        sys.stdout.flush()
        sys.stderr.flush()


main()
