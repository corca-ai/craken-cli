# Testing

Run the full local test suite with:

```sh
go test ./...
```

CI also verifies `go mod tidy`, runs the race detector, checks coverage, scans vulnerabilities, and lints the Linux target.

Coverage is enforced through:

```sh
./scripts/check_coverage.sh 35
```
