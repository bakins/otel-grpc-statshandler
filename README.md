StatsHandler is now implemented in  https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc 

This project is no longer needed.
# otel-grpc-statshandler

[![PkgGoDev](https://pkg.go.dev/badge/github.com/bakins/otel-grpc-statshandler)](https://pkg.go.dev/github.com/bakins/otel-grpc-statshandler)

`otel-grpc-statshandler` implements [grpc.StatsHandler](https://pkg.go.dev/google.golang.org/grpc@v1.49.0/stats#Handler) for recording OpenTelemetry metrics and traces.

[otelgrpc](https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc) uses an interceptor based approach that can [miss some errors](https://github.com/open-telemetry/opentelemetry-go-contrib/issues/197) and also does not currently support metrics.

It records metrics and traces as decribed in:
- https://opentelemetry.io/docs/reference/specification/metrics/semantic_conventions/rpc/
- https://opentelemetry.io/docs/reference/specification/trace/semantic_conventions/rpc/


PRs and patches are welcomed.

## Example

```go
package main

import (
    "google.golang.org/grpc"
    statshandler "github.com/bakins/otel-grpc-statshandler"
)

func main() {
    handler, err := statshandler.NewServerHandler()
    if err != nil {
       // handle error
    }

    server := grpc.NewServer(grpc.StatsHandler(handler))
}
```


## LICENSE

See [LICENSE](./LICENSE)

## TODO
- more test cases
