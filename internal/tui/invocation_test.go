package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// ivdFixture is the log of one invocation as a runtime writes it, the newest of the logs it is
// in.
type ivdFixture struct {
	name string
	id   string
	// lines are its lines, from START, each a millisecond apart unless it says its own offset.
	lines []ivdLine
}

// ivdLine is a line of a fixture, at after from the first.
type ivdLine struct {
	after time.Duration
	msg   string
}

// ivdEvents are the lines of a fixture from at, on one log stream.
func ivdEvents(fx ivdFixture, at time.Time) []cli.LogEvent {
	out := make([]cli.LogEvent, len(fx.lines))
	for i, l := range fx.lines {
		out[i] = cli.LogEvent{Time: at.Add(l.after), Stream: "2026/11/05/[$LATEST]" + fx.name, Message: l.msg}
	}
	return out
}

// ivdReport is the REPORT line lambda writes at the end of a request, with what it adds after the
// memory used: an init duration, a status.
func ivdReport(id, duration, billed string, used int, more string) string {
	return "REPORT RequestId: " + id + "\tDuration: " + duration + " ms\tBilled Duration: " + billed + " ms\tMemory Size: 1024 MB\tMax Memory Used: " + formatInt(used) + " MB\t" + more
}

// ivdNode is a line node's console writes in lambda, its newlines carriage returns, as the
// runtime writes them so a message stays one event.
func ivdNode(id, level, msg string) string {
	return "2026-11-05T10:00:00.012Z\t" + id + "\t" + level + "\t" + strings.ReplaceAll(msg, "\n", "\r")
}

// ivdFixtures are an invocation of each kind of function and log lambda writes: node, python and
// go, json logged by powertools, pino and zap, a stack trace, a timeout, a cold start, an error
// and a line of 50 kb.
func ivdFixtures() []ivdFixture {
	const (
		node     = "aaaaaaaa-0000-4000-8000-000000000001"
		nodeErr  = "aaaaaaaa-0000-4000-8000-000000000002"
		python   = "aaaaaaaa-0000-4000-8000-000000000003"
		goID     = "aaaaaaaa-0000-4000-8000-000000000004"
		power    = "aaaaaaaa-0000-4000-8000-000000000005"
		pino     = "aaaaaaaa-0000-4000-8000-000000000006"
		zap      = "aaaaaaaa-0000-4000-8000-000000000007"
		stack    = "aaaaaaaa-0000-4000-8000-000000000008"
		timeout  = "aaaaaaaa-0000-4000-8000-000000000009"
		cold     = "aaaaaaaa-0000-4000-8000-00000000000a"
		big      = "aaaaaaaa-0000-4000-8000-00000000000b"
		goPanic  = "aaaaaaaa-0000-4000-8000-00000000000c"
		pyCrash  = "aaaaaaaa-0000-4000-8000-00000000000d"
		ms       = time.Millisecond
		start    = "START RequestId: "
		version  = " Version: $LATEST"
		end      = "END RequestId: "
		nodeInit = "INIT_START Runtime Version: nodejs:22.v35\tRuntime Version ARN: arn:aws:lambda:eu-west-1::runtime:0123456789abcdef"
	)
	return []ivdFixture{
		{name: "node", id: node, lines: []ivdLine{
			{0, start + node + version},
			{12 * ms, ivdNode(node, "INFO", "order placed { orderId: 9, items: 3 }")},
			{20 * ms, ivdNode(node, "WARN", "stock is low for sku-41")},
			{25 * ms, end + node},
			{25 * ms, ivdReport(node, "25.31", "26", 88, "")},
		}},
		{name: "node error", id: nodeErr, lines: []ivdLine{
			{0, start + nodeErr + version},
			{3 * ms, ivdNode(nodeErr, "INFO", "reading order undefined")},
			{4 * ms, ivdNode(nodeErr, "ERROR", `Invoke Error 	{"errorType":"TypeError","errorMessage":"Cannot read properties of undefined (reading 'id')","stack":["TypeError: Cannot read properties of undefined (reading 'id')","    at handler (file:///var/task/index.mjs:4:22)","    at Runtime.handleOnceNonStreaming (file:///var/runtime/index.mjs:1173:29)"]}`)},
			{6 * ms, end + nodeErr},
			{6 * ms, ivdReport(nodeErr, "5.62", "6", 90, "")},
		}},
		{name: "python", id: python, lines: []ivdLine{
			{0, start + python + version},
			{4 * ms, "[INFO]\t2026-11-05T10:00:00.004Z\t" + python + "\treading order 9"},
			{9 * ms, "[WARNING]\t2026-11-05T10:00:00.009Z\t" + python + "\tretrying the read"},
			{40 * ms, end + python},
			{40 * ms, ivdReport(python, "40.10", "41", 71, "")},
		}},
		{name: "python crash", id: pyCrash, lines: []ivdLine{
			{0, start + pyCrash + version},
			{2 * ms, "[ERROR] KeyError: 'id'\rTraceback (most recent call last):\r  File \"/var/task/app.py\", line 8, in handler\r    return orders[event[\"id\"]]\r  File \"/var/lang/lib/python3.12/site-packages/orders/lookup.py\", line 3, in get\r    raise KeyError(key)"},
			{3 * ms, end + pyCrash},
			{3 * ms, ivdReport(pyCrash, "2.88", "3", 70, "")},
		}},
		{name: "go", id: goID, lines: []ivdLine{
			{0, start + goID + version},
			{1 * ms, "2026/11/05 10:00:00 reading order 9 from orders-dev"},
			{2 * ms, "2026/11/05 10:00:00 read order 9 in 1.2ms"},
			{3 * ms, end + goID},
			{3 * ms, ivdReport(goID, "2.71", "3", 21, "")},
		}},
		{name: "go panic", id: goPanic, lines: []ivdLine{
			{0, start + goPanic + version},
			{1 * ms, "panic: assignment to entry in nil map"},
			{1 * ms, ""},
			{1 * ms, "goroutine 1 [running]:"},
			{1 * ms, "main.handler({0x1d2e3f0, 0x4000124000})"},
			{1 * ms, "\t/build/main.go:23 +0x1d"},
			{1 * ms, "github.com/aws/aws-lambda-go/lambda.NewHandler.func1({0x1d2e3f0, 0x4000124000})"},
			{1 * ms, "\t/go/pkg/mod/github.com/aws/aws-lambda-go@v1.47.0/lambda/handler.go:200 +0x84"},
			{1 * ms, "runtime.goexit()"},
			{1 * ms, "\t/usr/local/go/src/runtime/asm_arm64.s:1223 +0x4"},
			{2 * ms, "RequestId: " + goPanic + " Error: Runtime exited with error: exit status 2\nRuntime.ExitError"},
			{2 * ms, end + goPanic},
			{2 * ms, ivdReport(goPanic, "1.80", "2", 20, "Status: error\tError Type: Runtime.ExitError")},
		}},
		{name: "powertools", id: power, lines: []ivdLine{
			{0, start + power + version},
			{12 * ms, `{"level":"INFO","location":"handler:12","message":"order placed","timestamp":"2026-11-05 10:00:00,012+0000","service":"orders","cold_start":false,"function_name":"orders-dev-api","function_memory_size":"1024","function_arn":"arn:aws:lambda:eu-west-1:123456789012:function:orders-dev-api","function_request_id":"` + power + `","xray_trace_id":"1-6729e0a0-3b1c9f2d8e7a6b5c4d3e2f1a","order_id":9}`},
			{30 * ms, `{"level":"ERROR","location":"handler:20","message":"could not store the order","timestamp":"2026-11-05 10:00:00,030+0000","service":"orders","cold_start":false,"function_request_id":"` + power + `","exception":"Traceback (most recent call last):\n  File \"/var/task/app.py\", line 18, in handler\n    table.put_item(Item=order)\n  File \"/var/lang/lib/python3.12/site-packages/boto3/resources/factory.py\", line 581, in do_action\n    response = action(self, *args, **kwargs)\nbotocore.exceptions.ClientError: An error occurred (ValidationException) when calling the PutItem operation","exception_name":"ClientError"}`},
			{31 * ms, end + power},
			{31 * ms, ivdReport(power, "31.00", "31", 96, "")},
		}},
		{name: "pino", id: pino, lines: []ivdLine{
			{0, start + pino + version},
			{5 * ms, `{"level":30,"time":1762336800005,"pid":8,"hostname":"169.254.12.1","reqId":"` + pino + `","msg":"order placed","orderId":9}`},
			{9 * ms, `{"level":50,"time":1762336800009,"pid":8,"hostname":"169.254.12.1","err":{"type":"Error","message":"connection reset","stack":"Error: connection reset\n    at Socket.onEnd (/var/task/node_modules/pg/lib/connection.js:118:15)\n    at store (/var/task/src/store.js:18:9)\n    at node:internal/process/task_queues:95:5"},"msg":"could not store the order"}`},
			{10 * ms, end + pino},
			{10 * ms, ivdReport(pino, "10.40", "11", 92, "")},
		}},
		{name: "zap", id: zap, lines: []ivdLine{
			{0, start + zap + version},
			{1 * ms, `{"level":"info","ts":1762336800.001,"caller":"orders/main.go:42","msg":"order read","order":9,"took":0.0012}`},
			{2 * ms, `{"level":"error","ts":1762336800.002,"caller":"orders/main.go:51","msg":"store failed","error":"ConditionalCheckFailedException: The conditional request failed","stacktrace":"main.store\n\t/build/main.go:51\nmain.handler\n\t/build/main.go:30\ngithub.com/aws/aws-lambda-go/lambda.NewHandler.func1\n\t/go/pkg/mod/github.com/aws/aws-lambda-go@v1.47.0/lambda/handler.go:200"}`},
			{3 * ms, end + zap},
			{3 * ms, ivdReport(zap, "2.90", "3", 22, "")},
		}},
		{name: "stack trace", id: stack, lines: []ivdLine{
			{0, start + stack + version},
			{7 * ms, ivdNode(stack, "ERROR", "Error: connect ECONNREFUSED 127.0.0.1:5432\n    at TCPConnectWrap.afterConnect [as oncomplete] (node:net:1607:16)\n    at Client._connect (/var/task/node_modules/pg/lib/client.js:132:11)\n    at store (/var/task/src/store.js:18:9)\n    at async Runtime.handler (/var/task/src/index.js:9:3)")},
			{8 * ms, end + stack},
			{8 * ms, ivdReport(stack, "8.12", "9", 93, "")},
		}},
		{name: "timeout", id: timeout, lines: []ivdLine{
			{0, start + timeout + version},
			{2 * ms, ivdNode(timeout, "INFO", "calling the payments api")},
			{6010 * ms, "2026-11-05T10:00:06.010Z " + timeout + " Task timed out after 6.01 seconds"},
			{6010 * ms, end + timeout},
			{6010 * ms, ivdReport(timeout, "6006.11", "6000", 98, "Status: timeout")},
		}},
		{name: "cold start", id: cold, lines: []ivdLine{
			{-190 * ms, nodeInit},
			{-120 * ms, "2026-11-05T09:59:59.880Z\tundefined\tINFO\tconnected to orders-dev"},
			{-1 * ms, "INIT_REPORT Init Duration: 188.40 ms\tPhase: init\tStatus: success"},
			{0, start + cold + version},
			{14 * ms, ivdNode(cold, "INFO", "order placed")},
			{15 * ms, end + cold},
			{15 * ms, ivdReport(cold, "14.80", "203", 140, "Init Duration: 188.40 ms")},
		}},
		{name: "50 kb line", id: big, lines: []ivdLine{
			{0, start + big + version},
			{1 * ms, `{"level":"info","msg":"the order","order":"` + strings.Repeat("sku-41 ", 7300) + `"}`},
			{2 * ms, ivdNode(big, "INFO", strings.Repeat("a long line of 50 kb ", 2500))},
			{3 * ms, end + big},
			{3 * ms, ivdReport(big, "3.00", "3", 120, "")},
		}},
	}
}

// ivdLogs are the lines of every fixture, each a second after the one before, the last starting
// half a minute ago.
func ivdLogs() []cli.LogEvent {
	fixtures := ivdFixtures()
	at := time.Now().Add(-30*time.Second - time.Duration(len(fixtures))*time.Second)
	out := make([]cli.LogEvent, 0, len(fixtures)*8)
	for i, fx := range fixtures {
		out = append(out, ivdEvents(fx, at.Add(time.Duration(i)*time.Second))...)
	}
	return out
}

// ivdOpen is the invocations tab of a function that logged every fixture, at a size.
func ivdOpen(t *testing.T, w, h int) model {
	t.Helper()
	f := newFake(t)
	f.logs = ivdLogs()
	m := newModel(t.Context(), f)
	m, _ = update(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	m = settle(t, m, m.Init())
	return pressKeys(t, m, "enter", "tab", "tab")
}

// ivdOpenOne opens the invocation of request id from the invocations tab, as a person does.
func ivdOpenOne(t *testing.T, m model, id string) model {
	t.Helper()
	m = pressKeys(t, m, "g")
	for _, r := range m.state.function.requests {
		if r.id == id {
			return pressKeys(t, m, "enter")
		}
		m = pressKeys(t, m, "j")
	}
	t.Fatalf("no invocation %s in the table", id)
	return m
}

// ivdPage is all of the invocation page, without its styles, however far it scrolls.
func ivdPage(m model) string {
	l := m.invocationLines()
	return ansi.Strip(strings.Join(l.window(0, l.len()), "\n"))
}

// TestInvocationFixtures opens the invocation of each fixture and checks its head says how it
// ended and what with, its lines show what they say without lambda's own lines, the time and the
// request id the head shows, and its stack traces and json read as they should.
func TestInvocationFixtures(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   []string
		absent []string
	}{
		{name: "node", status: "✓ ok", want: []string{"+12ms  info   order placed { orderId: 9, items: 3 }", "+20ms  warn   stock is low for sku-41", "duration  25ms · billed 26ms", "memory  88 of 1,024 mb"},
			absent: []string{"START RequestId", "END RequestId", "REPORT RequestId", "2026-11-05T10:00:00.012Z", "cold start"}},
		{name: "node error", status: "✗ failed", want: []string{"error  TypeError: Cannot read properties of undefined (reading 'id')", "error  Invoke Error · TypeError: Cannot read", "      at handler (file:///var/task/index.mjs:4:22)"},
			absent: []string{`"errorType"`, "[\"TypeError"}},
		{name: "python", status: "✓ ok", want: []string{"+4ms  info   reading order 9", "+9ms  warn   retrying the read"}, absent: []string{"[INFO]", "2026-11-05T10:00:00.004Z"}},
		{name: "python crash", status: "✗ failed", want: []string{"error  KeyError: 'id'", "Traceback (most recent call last):", `  File "/var/task/app.py", line 8, in handler`, `    return orders[event["id"]]`},
			absent: []string{`\x0d`, "[ERROR]"}},
		{name: "go", status: "✓ ok", want: []string{"+1ms  reading order 9 from orders-dev", "+2ms  read order 9 in 1.2ms"}, absent: []string{"2026/11/05"}},
		{name: "go panic", status: "✗ failed", want: []string{"error  panic: assignment to entry in nil map", "goroutine 1 [running]:", "  main.handler({0x1d2e3f0, 0x4000124000})", "    /build/main.go:23 +0x1d", "Error: Runtime exited with error: exit status 2"},
			absent: []string{"RequestId: aaaaaaaa"}},
		{name: "powertools", status: "✗ failed", want: []string{"error  ClientError: could not store the order", "info   order placed  location=handler:12  service=orders", "Traceback (most recent call last):", "botocore.exceptions.ClientError"},
			absent: []string{"function_arn", "cold_start", "timestamp=", "function_request_id", `"level"`}},
		{name: "pino", status: "✗ failed", want: []string{"error  Error: could not store the order: connection reset", `error  could not store the order  err="Error: connection reset"`, "at store (/var/task/src/store.js:18:9)"},
			absent: []string{"time=", "reqId=", "level=", "msg="}},
		{name: "zap", status: "✗ failed", want: []string{"info   order read  caller=orders/main.go:42  order=9  took=0.0012", `error="ConditionalCheckFailedException: The co…"`, "main.store", "  /build/main.go:51"},
			absent: []string{"ts=", "stacktrace="}},
		{name: "stack trace", status: "✗ failed", want: []string{"error  Error: connect ECONNREFUSED 127.0.0.1:5432", "  at store (/var/task/src/store.js:18:9)", "  at async Runtime.handler (/var/task/src/index.js:9:3)"},
			absent: []string{`\x0d`}},
		{name: "timeout", status: "⏱ timed out", want: []string{"error  Task timed out after 6.01 seconds", "+6s         Task timed out after 6.01 seconds", "duration  6s · billed 6s"}},
		{name: "cold start", status: "✓ ok", want: []string{"cold start  188ms · nodejs:22.v35", "-120ms  info   connected to orders-dev", "+14ms  info   order placed", "billed 203ms"},
			absent: []string{"INIT_START", "INIT_REPORT", "undefined"}},
		{name: "50 kb line", status: "✓ ok", want: []string{`info   the order  order="sku-41 sku-41`, "more lines of this one: the logs tab shows it whole"}},
	}
	m := ivdOpen(t, 120, 40)
	for _, fx := range ivdFixtures() {
		var tt struct {
			name   string
			status string
			want   []string
			absent []string
		}
		for _, c := range tests {
			if c.name == fx.name {
				tt = c
			}
		}
		t.Run(fx.name, func(t *testing.T) {
			if tt.name == "" {
				t.Fatal("no test for the fixture")
			}
			v := ivdOpenOne(t, m, fx.id)
			page := ivdPage(v)
			if v.page != invocationPage || !strings.Contains(page, "status  "+tt.status) {
				t.Fatalf("the status is not %s:\n%s", tt.status, page)
			}
			if n := strings.Count(page, fx.id); n != 1 {
				t.Errorf("the request id is on the page %d times, want once, in the head:\n%s", n, page)
			}
			for _, want := range tt.want {
				if !strings.Contains(page, want) {
					t.Errorf("no %q in:\n%s", want, page)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(page, absent) {
					t.Errorf("%q is on the page:\n%s", absent, page)
				}
			}
		})
	}
}

// TestInvocationTones checks the tones of a line of the invocation page: the message in the text
// ink, its level by how bad it is, the other fields faint, and a frame of the function's own code
// in the body ink beside the faint frames of its dependencies and the runtime.
func TestInvocationTones(t *testing.T) {
	g := gutter{offset: 5, levels: true}
	pino := lineRows(readLogLine(ivdFixtures()[7].lines[2].msg, ""), g, 160)
	python := lineRows(readLogLine(ivdFixtures()[3].lines[1].msg, ""), g, 160)
	zap := lineRows(readLogLine(ivdFixtures()[8].lines[2].msg, ""), g, 160)
	tests := []struct {
		name string
		c    cell
		part string
		want styles.Tone
	}{
		{name: "the message", c: pino[0], part: "could not store the order", want: styles.ToneText},
		{name: "an error level", c: pino[0], part: "error", want: styles.ToneDanger},
		{name: "a key", c: pino[0], part: "pid=", want: styles.ToneFaint},
		{name: "a value", c: pino[0], part: "169.254.12.1", want: styles.ToneMuted},
		{name: "the error", c: pino[0], part: "Error: connection reset", want: styles.ToneBody},
		{name: "a frame of a dependency", c: pino[1], part: "node_modules", want: styles.ToneFaint},
		{name: "a frame of its own", c: pino[2], part: "at store", want: styles.ToneBody},
		{name: "a frame of node", c: pino[3], part: "node:internal", want: styles.ToneFaint},
		{name: "a traceback's head", c: python[1], part: "Traceback", want: styles.ToneFaint},
		{name: "its own python frame", c: python[2], part: "/var/task/app.py", want: styles.ToneBody},
		{name: "its own python source", c: python[3], part: "return orders", want: styles.ToneBody},
		{name: "a python package", c: python[4], part: "site-packages", want: styles.ToneFaint},
		{name: "its own go call", c: zap[1], part: "main.store", want: styles.ToneBody},
		{name: "aws-lambda-go", c: zap[6], part: "aws-lambda-go", want: styles.ToneFaint},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tone, ok := toneOf(tt.c, tt.part)
			if !ok {
				t.Fatalf("no %q in %q", tt.part, plainText(tt.c))
			}
			if tone != tt.want {
				t.Errorf("%q is in tone %v, want %v", tt.part, tone, tt.want)
			}
		})
	}
}

// TestReadLogLine checks a line of json reads as its level, its message and then its other
// fields in the order of the line, without the time and the request id, each value cut to
// fieldWidth, and a line a runtime writes without its time and request id.
func TestReadLogLine(t *testing.T) {
	id := "aaaaaaaa-0000-4000-8000-000000000001"
	tests := []struct {
		name  string
		line  string
		level string
		body  string
		more  int
	}{
		{name: "fields after the message", line: `{"b":2,"msg":"hi","level":"warn","a":1}`, level: "warn", body: "hi  b=2  a=1"},
		{name: "the time and request id dropped", line: `{"time":"x","requestId":"` + id + `","level":"info","message":"hi","user":"levi"}`, level: "info", body: "hi  user=levi"},
		{name: "another request's id kept", line: `{"requestId":"other","msg":"hi"}`, body: "hi  requestId=other"},
		{name: "a long value cut", line: `{"msg":"hi","q":"` + strings.Repeat("x", 100) + `"}`, body: "hi  q=" + strings.Repeat("x", fieldWidth-1) + "…"},
		{name: "a value with a space quoted", line: `{"msg":"hi","who":"levi van noort"}`, body: `hi  who="levi van noort"`},
		{name: "an object compact", line: `{"msg":"hi","order":{ "id": 9 }}`, body: `hi  order={"id":9}`},
		{name: "pino's level", line: `{"level":40,"msg":"slow"}`, level: "warn", body: "slow"},
		{name: "no message", line: `{"level":"info","a":1}`, level: "info", body: "a=1"},
		{name: "a message of two lines", line: `{"msg":"one\ntwo"}`, body: "one", more: 1},
		{name: "a node line", line: "2026-11-05T10:00:00.012Z\t" + id + "\tWARN\tslow", level: "warn", body: "slow"},
		{name: "a python line", line: "[CRITICAL]\t2026-11-05T10:00:00.012Z\t" + id + "\tdown", level: "crit", body: "down"},
		{name: "a timeout", line: "2026-11-05T10:00:06.010Z " + id + " Task timed out after 6.01 seconds", body: "Task timed out after 6.01 seconds"},
		{name: "broken json as it is", line: `{"msg":"cut`, body: `{"msg":"cut`},
		{name: "a lambda error after words", line: `Invoke Error 	{"errorType":"Error","errorMessage":"boom","stack":["Error: boom","    at x (/var/task/a.js:1:1)"]}`, body: "Invoke Error · Error: boom", more: 1},
		{name: "go's frames from aws-lambda-go", line: `{"errorMessage":"boom","errorType":"runtimeError","stackTrace":[{"path":"/build/main.go","line":23,"label":"handler"}]}`, body: "runtimeError: boom", more: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := readLogLine(tt.line, id)
			if l.level != tt.level || plainText(l.body) != tt.body || len(l.more) != tt.more {
				t.Errorf("level %q, body %q, %d lines under it; want %q, %q, %d", l.level, plainText(l.body), len(l.more), tt.level, tt.body, tt.more)
			}
		})
	}
}

// TestFailureCause checks what each kind of failure says it failed with: a timeout, the error a
// runtime reports, a panic, and a line logged at the error level.
func TestFailureCause(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		rank    int
		kind    string
		message string
	}{
		{name: "a timeout", line: "2026-11-05T10:00:06.010Z 1 Task timed out after 6.01 seconds", rank: 3, message: "Task timed out after 6.01 seconds"},
		{name: "node's invoke error", line: "2026-11-05T10:00:00.012Z\t1\tERROR\tInvoke Error \t{\"errorType\":\"TypeError\",\"errorMessage\":\"boom\"}", rank: 2, kind: "TypeError", message: "boom"},
		{name: "go's error after its log date", line: `2026/11/05 10:00:00 {"errorMessage":"boom","errorType":"runtimeError"}`, rank: 2, kind: "runtimeError", message: "boom"},
		{name: "a python crash", line: "[ERROR] KeyError: 'id'\rTraceback (most recent call last):", rank: 2, kind: "KeyError", message: "'id'"},
		{name: "a go panic", line: "panic: boom", rank: 2, kind: "panic", message: "boom"},
		{name: "a runtime that exited", line: "RequestId: 1 Error: Runtime exited with error: exit status 2\nRuntime.ExitError", rank: 2, kind: "Runtime.ExitError", message: "Runtime exited with error: exit status 2"},
		{name: "a report's error type", line: "REPORT RequestId: 1\tDuration: 1.00 ms\tStatus: error\tError Type: Runtime.Unknown", rank: 1, kind: "Runtime.Unknown"},
		{name: "a node error logged", line: "2026-11-05T10:00:00.012Z\t1\tERROR\tError: connect ECONNREFUSED", rank: 1, kind: "Error", message: "connect ECONNREFUSED"},
		{name: "a python error logged", line: "[ERROR]\t2026-11-05T10:00:00.012Z\t1\tcould not store", rank: 1, message: "could not store"},
		{name: "zap", line: `{"level":"error","msg":"store failed","error":"conditional check failed"}`, rank: 1, message: "store failed: conditional check failed"},
		{name: "pino", line: `{"level":50,"err":{"type":"Error","message":"reset"},"msg":"could not store"}`, rank: 1, kind: "Error", message: "could not store: reset"},
		{name: "powertools", line: `{"level":"ERROR","message":"could not store","exception_name":"ClientError"}`, rank: 1, kind: "ClientError", message: "could not store"},
		{name: "logfmt", line: `level=error msg=boom`, rank: 1, message: "level=error msg=boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := causeOf(tt.line, "1")
			if c.rank != tt.rank || c.kind != tt.kind || c.message != tt.message {
				t.Errorf("got %+v, want rank %d, %q, %q", c, tt.rank, tt.kind, tt.message)
			}
		})
	}
}

// TestRequestsColdStart checks the lines of the init of an instance go with the first request it
// runs, with the runtime it loaded and an init that did not succeed, and not with the next.
func TestRequestsColdStart(t *testing.T) {
	at := time.Now().Add(-time.Minute)
	line := func(d time.Duration, msg string) cli.LogEvent {
		return cli.LogEvent{Time: at.Add(d), Stream: "a", Message: msg}
	}
	got := requests([]cli.LogEvent{
		line(0, "INIT_START Runtime Version: python:3.12.v40\tRuntime Version ARN: arn"),
		line(time.Millisecond, "connecting"),
		line(2*time.Millisecond, "INIT_REPORT Init Duration: 900.00 ms\tPhase: init\tStatus: timeout"),
		line(3*time.Millisecond, "START RequestId: r1 Version: $LATEST"),
		line(4*time.Millisecond, "REPORT RequestId: r1\tDuration: 2.00 ms\tBilled Duration: 2 ms\tMemory Size: 128 MB\tMax Memory Used: 60 MB"),
		line(5*time.Millisecond, "START RequestId: r2 Version: $LATEST"),
		line(6*time.Millisecond, "REPORT RequestId: r2\tDuration: 2.00 ms\tBilled Duration: 2 ms\tMemory Size: 128 MB\tMax Memory Used: 60 MB"),
	})
	tests := []struct {
		name    string
		r       request
		lines   int
		init    float64
		runtime string
		status  string
	}{
		{name: "the cold start", r: got[1], lines: 5, init: 900, runtime: "python:3.12.v40", status: "timeout"},
		{name: "the warm one", r: got[0], lines: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.r
			if len(r.lines) != tt.lines || r.init != tt.init || r.runtime != tt.runtime || r.initStatus != tt.status || r.failed {
				t.Errorf("%d lines, init %v, runtime %q, init %q, failed %v", len(r.lines), r.init, r.runtime, r.initStatus, r.failed)
			}
		})
	}
}

// TestInvocationsSummary checks the line above the invocations counts them, the failed and the
// cold starts, and gives the median and p99 of their durations.
func TestInvocationsSummary(t *testing.T) {
	durations := func(ms ...float64) []request {
		out := make([]request, 0, len(ms))
		for i, d := range ms {
			out = append(out, request{reported: true, duration: d, failed: i == 0, init: float64(i % 2)})
		}
		return out
	}
	tests := []struct {
		name string
		rs   []request
		want string
	}{
		{name: "one", rs: durations(12), want: "1 invocation · 1 failed · p50 12ms · p99 12ms · 0 cold starts"},
		{name: "many", rs: durations(1, 2, 3, 4, 5, 6, 7, 8, 9, 1000), want: "10 invocations · 1 failed · p50 5ms · p99 1s · 5 cold starts"},
		{name: "none reported", rs: []request{{}}, want: "1 invocation · 0 failed · 0 cold starts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := plainText(invocationsSummary(tt.rs)); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestInvocationsTable checks the invocations tab sums them up above the table, newest first,
// each with its status, a cold start marked, its durations with their units lined up, and what a
// failed one failed with.
func TestInvocationsTable(t *testing.T) {
	m := ivdOpen(t, 120, 40)
	v := screen(m)
	rows := strings.Split(v, "\n")
	find := func(part string) string {
		for _, r := range rows {
			if strings.Contains(r, part) {
				return r
			}
		}
		return ""
	}
	tests := []struct {
		name string
		row  string
		want []string
	}{
		{name: "the summary", row: "invocations ·", want: []string{"13 invocations · 8 failed · p50 8ms · p99 6s · 1 cold start"}},
		{name: "the columns", row: "duration", want: []string{"time", "request", "billed", "memory", "cold start", "error"}},
		{name: "a timeout", row: "⏱ timed out", want: []string{"6s ", "Task timed out after 6.01 seconds"}},
		{name: "a cold start", row: "188ms", want: []string{"✓ ok", "15ms", "203ms", "140/1,024 mb"}},
		{name: "an error", row: "TypeError", want: []string{"✗ failed", "aaaaaaaa", "Cannot read"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := find(tt.row)
			for _, want := range tt.want {
				if !strings.Contains(row, want) {
					t.Errorf("no %q in %q", want, row)
				}
			}
		})
	}
	newest := m.state.function.requests[0].id
	if newest != ivdFixtures()[len(ivdFixtures())-1].id {
		t.Errorf("the first row is %s, want the newest", newest)
	}
	if a, b := strings.Index(find("⏱ timed out"), "6s "), strings.Index(find("188ms"), "15ms"); a+1 != b+2 {
		t.Errorf("the durations do not line up:\n%s\n%s", find("⏱ timed out"), find("188ms"))
	}
}

// TestInvocationStep checks n and p show the next and the previous invocation without going
// back, the table's cursor following, and say so at either end.
func TestInvocationStep(t *testing.T) {
	m := ivdOpen(t, 120, 40)
	rs := m.state.function.requests
	tests := []struct {
		name   string
		keys   []string
		want   string
		status string
	}{
		{name: "next", keys: []string{"n"}, want: rs[1].id},
		{name: "next and back", keys: []string{"n", "n", "p"}, want: rs[1].id},
		{name: "before the newest", keys: []string{"p"}, want: rs[0].id, status: "this is the newest invocation"},
		{name: "after the oldest", keys: []string{"G", "enter", "n"}, want: rs[len(rs)-1].id, status: "this is the oldest invocation in the logs read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := m
			if tt.keys[0] != "G" {
				v = pressKeys(t, v, "enter")
			}
			v = pressKeys(t, v, tt.keys...)
			if v.page != invocationPage || v.state.invocation.req.id != tt.want || !strings.Contains(screen(v), tt.want) {
				t.Fatalf("on %v, invocation %s, want %s", v.page, v.state.invocation.req.id, tt.want)
			}
			if !strings.HasPrefix(v.status, tt.status) {
				t.Errorf("status %q, want %q", v.status, tt.status)
			}
			v = pressKeys(t, v, "q")
			if f := v.state.function; v.page != functionPage || f.requests[f.invocations.cursor].id != tt.want {
				t.Errorf("back on %v with the cursor on %s, want the function with it on %s", v.page, f.requests[f.invocations.cursor].id, tt.want)
			}
		})
	}
}

// FuzzInvocationLine checks a line of a log of anything is drawn on the invocation page with
// nothing the terminal would act on, no wider than the page, in the gutter of a request whose
// lines do and do not name their level. A width is at least 20, the room of the gutter and a
// wide rune.
func FuzzInvocationLine(f *testing.F) {
	for _, fx := range ivdFixtures() {
		for _, l := range fx.lines {
			if len(l.msg) < 2000 {
				f.Add(l.msg, fx.id, 80)
			}
		}
	}
	f.Add("{\"level\":\"\\u001b[2J\",\"msg\":\"x\u202ey\",\"err\":{\"stack\":\"a\\n\\t/x.go:1\"},\"k\\u0007\":\"v\"}", "1", 30)
	f.Add("2026-11-05T10:00:00.001Z\t1\t\x1b[31mERROR\t漢字\r    at x (/var/task/\x9b.js:1:1)", "1", 21)
	f.Fuzz(func(t *testing.T, line, id string, width int) {
		width = 20 + fuzzWidth(width, 160)
		for _, g := range []gutter{{offset: 6, levels: true}, {offset: 6}} {
			for _, c := range lineRows(readLogLine(line, id), g, width) {
				drawn := renderCell(append(offsetCell("+12ms", g), c...), column{width: width}, nil)
				fuzzDrawn(t, "a line of an invocation", drawn, width)
				if w := ansi.StringWidth(plainText(c)) + g.offset + 2; w > width {
					t.Fatalf("a line of an invocation %d wide, more than %d: %q", w, width, plainText(c))
				}
			}
		}
		if failed, _ := failure(line); failed {
			fuzzPlain(t, "what an invocation failed with", plainText(causeCell(causeOf(line, id), styles.ToneBody)))
		}
	})
}

// TestInvocationLayout draws the invocation of each fixture and the invocations tab at terminal
// sizes from the minimum to large, scrolled to the top and the bottom, and checks no line is wider
// than the terminal, there are no more lines than it is high, and nothing drawn holds an escape.
func TestInvocationLayout(t *testing.T) {
	sizes := [][2]int{{78, 22}, {80, 24}, {100, 30}, {120, 40}, {200, 60}}
	for _, size := range sizes {
		w, h := size[0], size[1]
		m := ivdOpen(t, w, h)
		pages := map[string]model{"invocations": m}
		for _, fx := range ivdFixtures() {
			v := ivdOpenOne(t, m, fx.id)
			pages[fx.name] = v
			pages[fx.name+" at the bottom"] = pressKeys(t, v, "G")
		}
		for name, v := range pages {
			drawn := v.render()
			fuzzDrawn(t, name, drawn, w)
			if n := len(strings.Split(drawn, "\n")); n > h {
				t.Errorf("%dx%d %s: %d lines", w, h, name, n)
			}
		}
	}
}

// TestInvocationCauseWraps checks the error an invocation failed with wraps in the column its
// value is drawn in, so no line of it is cut short at a width where it would only just have fit.
func TestInvocationCauseWraps(t *testing.T) {
	tests := []struct {
		name  string
		cause cause
	}{
		{name: "a type and a message", cause: cause{kind: "TypeError", message: "Cannot read properties of undefined (reading 'id')"}},
		{name: "a message alone", cause: cause{message: "Task timed out after 6.01 seconds"}},
	}
	m := newTestModel(t, newFake(t))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := request{id: "r-1", failed: true, cause: tt.cause}
			want := strings.TrimPrefix(tt.cause.kind+": "+tt.cause.message, ": ")
			for width := 40; width <= 120; width++ {
				var parts []string
				for _, line := range m.invocationHead(r, width) {
					plain := ansi.Strip(line)
					if ansi.StringWidth(plain) > width {
						t.Fatalf("width %d: %q is wider", width, plain)
					}
					label := strings.TrimSpace(ansi.Truncate(plain, rowInset+labelWidth, ""))
					if label == errorLabel || label == "" && len(parts) > 0 {
						parts = append(parts, strings.TrimSpace(ansi.TruncateLeft(plain, rowInset+labelWidth, "")))
					}
				}
				if got := strings.Join(parts, " "); got != want {
					t.Fatalf("width %d: the error reads %q, want %q", width, got, want)
				}
			}
		})
	}
}

// TestInvocationNothingLogged checks the note of an invocation that logged nothing of its own
// starts where the values of the rows above it do.
func TestInvocationNothingLogged(t *testing.T) {
	tests := []struct {
		name  string
		lines []cli.LogEvent
	}{
		{name: "lambda's own lines", lines: []cli.LogEvent{{Message: "START RequestId: r-1 Version: $LATEST"}, {Message: "END RequestId: r-1"}}},
		{name: "blank lines", lines: []cli.LogEvent{{Message: "  "}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel(t, newFake(t))
			m.state.invocation = invocationState{req: request{id: "r-1", lines: tt.lines}, cache: &invocationCache{}}
			if v := ivdPage(m); !strings.Contains(v, "\n"+strings.Repeat(" ", rowInset+labelWidth)+"nothing logged but lambda's own lines") {
				t.Errorf("the note does not start where the values do:\n%s", v)
			}
		})
	}
}
