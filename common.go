package viamchess

import (
	"context"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"

	"go.viam.com/rdk/resource"
	"go.viam.com/utils/trace"
)

var family = resource.ModelNamespace("erh").WithFamily("viam-chess")

func init() {
	exporter, err := otlptracegrpc.New(context.Background())
	if err == nil {
		trace.AddExporters(exporter)
	}
}
