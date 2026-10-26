# Contributing

Thanks for your interest in contributing!

## Development

1. Fork the repository and create your branch from `master`.
2. Make your changes.
3. Run the checks locally:

```bash
go build ./...
go vet ./...
go test -race ./...
golangci-lint run ./...
```

4. Open a pull request with a clear description of the change.

## Code style

- Go code is formatted with `gofmt`/`goimports`.
- Every feature or bug fix should come with tests.
- Keep the public API documented (godoc comments).

## Reporting bugs

Open an issue with a minimal reproduction, the Go version and the Loki version you are pushing to.
