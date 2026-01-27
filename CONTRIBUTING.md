# Contributing

This guide is for those who wish to contribute to the project.

## Development

Check that the code compiles:

```
go build ./...
```

Run the tests:

```
go test ./...
```


Test the examples:

```bash
go run ./examples/simple/main.go
go run ./examples/steps/main.go
```

### Run against echoserver

You can run the client against a local instance of `echoserver` to verify functionality of the command-line tool.

Start `echoserver` in one terminal:
```bash
go run github.com/tsaarni/echoserver@latest -log-level info
```

In another terminal, run the load test commands:
```bash
go run ./cmd/echoclient get
go run ./cmd/echoclient upload -size 10GB
```
