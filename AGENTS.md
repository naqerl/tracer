# Development

- To validate written code run `make vet`
- To test written code run `make test`
- Favor make target and DO NOT RUN `go vet`, `gofmt`, `go fmt` or any other raw `go` commands to
  - lint
  - format
  - build

# Unrelated changes

If any `make` target returns some issues from the code that you have not touched, DO NOT "nah, that's not me, i would not touch it". Instead fix it or interrupt and ask operator about how should it be fixed if not obvious. I do not care about who written the code that does not work, if it does not work for you - it's your responsibility to fix it.
