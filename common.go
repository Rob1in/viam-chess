package viamchess

import (
	"context"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"

	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
	"go.viam.com/utils/trace"
)

var family = resource.ModelNamespace("erh").WithFamily("viam-chess")

func enableTracing(logger logging.Logger) {
	exporter, err := otlptracegrpc.New(context.Background())
	if err != nil {
		logger.Warnf("can't enable tracing: %v", err)
	} else {
		trace.AddExporters(exporter)
	}
}
