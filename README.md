Minimal Go HTTP server, exposing a remote code execution endpoint, accepting commands: `exec`, `file_read`, `file_write`, `dir_create`. No auth.

## Build

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o rce-http .
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o rce-http .
```

## Run

Home directory defaults to the working directory where the server is started.
Can be overwritted with `-home` flag, or per request `?home` query param.
Relative paths resolve against the home dir; absolute paths are used as-is.


```bash
./rce-http -route /invocations -addr :8080 -home /workspace -route
```

- `GET /ping` health check
- `POST /invocations[?home=/abs/path]` with `{"cmd": "...", ...}`, returns `{"output": {...}}`

```bash
curl -X POST 'localhost:8080/invocations' -d '{"cmd":"exec","command":"ls"}'
```
