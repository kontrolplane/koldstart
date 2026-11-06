Set FontSize 20
Set Width 2000
Set Height 1000
Set BorderRadius 10
Set TypingSpeed 100ms

Output ./assets/cassette.gif

Hide
  Type@10ms "go build -o bin/koldstart ./cmd/koldstart && cd vhs/shop && clear && AWS_CONFIG_FILE=../aws-config ../../bin/koldstart --profile vhs ui --config koldstart.next.pkl"
  Enter
  Wait+Screen@60s /1 of 3/
  Type "q"
  Wait+Screen@10s /pr-128/
  Sleep 1s
Show

Sleep 2s

Screenshot ./assets/pages/stages/overview.png

Sleep 500ms
Enter
Wait+Screen@10s /[0-9]ms/
Sleep 2s

Screenshot ./assets/pages/stage/functions.png

Sleep 500ms
Tab
Sleep 2s
Down@600ms 2
Sleep 1s
Space
Sleep 2s

Screenshot ./assets/pages/stage/resources.png

Sleep 500ms
Tab
Sleep 2s
Down
Sleep 2s

Screenshot ./assets/pages/stage/outputs.png

Sleep 500ms
Enter
Wait+Screen@10s /value/
Sleep 2s

Screenshot ./assets/pages/output/details.png

Sleep 500ms
Type "q"
Sleep 1s
Tab
Wait+Screen@10s /~1/
Sleep 2s

Screenshot ./assets/pages/stage/history.png

Sleep 500ms
Enter
Wait+Screen@10s /changed/
Sleep 2s

Screenshot ./assets/pages/deploy/changes.png

Sleep 500ms
Type "q"
Sleep 1s
Tab
Sleep 1s
Enter
Wait+Screen@10s /successful/
Sleep 2s

Screenshot ./assets/pages/function/overview.png

Sleep 500ms
Tab
Wait+Screen@10s /following/
Sleep 2s
Type "s"
Sleep 1s
Type "s"
Wait+Screen@10s /level ≥ warn/
Sleep 2s

Screenshot ./assets/pages/function/logs.png

Sleep 500ms
Enter
Wait+Screen@10s /log line/
Sleep 2s

Screenshot ./assets/pages/line/details.png

Sleep 500ms
Type "o"
Wait+Screen@10s /memory/
Sleep 2s

Screenshot ./assets/pages/invocation/details.png

Sleep 500ms
Type "q"
Wait+Screen@10s /log line/
Sleep 1s
Type "q"
Wait+Screen@10s /following/
Sleep 1s
Tab
Wait+Screen@10s /cold start/
Sleep 2s
Down@600ms 2
Sleep 2s

Screenshot ./assets/pages/function/invocations.png

Sleep 500ms
Tab
Sleep 2s
Down@600ms 4
Sleep 1s
Type "i"
Wait+Screen@30s /returned/
Sleep 2s

Screenshot ./assets/pages/function/invoke.png

Sleep 500ms
Type "q"
Sleep 1s
Tab
Sleep 1s
Type "G"
Sleep 1s
Up@600ms 2
Sleep 1s
Enter
Wait+Screen@10s /applied with/
Sleep 2s
Down@400ms 3
Sleep 1s
Space
Sleep 1s
Type "/"
Sleep 500ms
Type "Key"
Sleep 1s
Enter
Sleep 1s
Type "n"
Sleep 2s

Screenshot ./assets/pages/resource/applied.png

Sleep 500ms
Type "q"
Sleep 1s
Type "d"
Wait+Screen@60s /planned at/
Sleep 2s

Screenshot ./assets/pages/plan/kinds.png

Sleep 500ms
Type "o"
Sleep 2s

Screenshot ./assets/pages/plan/actions.png

Sleep 500ms
Type "o"
Sleep 2s
Enter
Wait+Screen@10s /change 1 of/
Sleep 2s
Type "]"
Sleep 1s
Type "]"
Sleep 2s

Screenshot ./assets/pages/diff/changes.png

Sleep 500ms
Type "q"
Sleep 1s
Type "q"
Sleep 1s
Type "q"
Sleep 1s
Type "q"
Sleep 1s
