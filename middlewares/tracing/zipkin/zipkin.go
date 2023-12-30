package zipkin

import (
	"io"

	"github.com/opentracing/opentracing-go"
	zipkinot "github.com/openzipkin-contrib/zipkin-go-opentracing"
	"github.com/openzipkin/zipkin-go"
	zipkinhttp "github.com/openzipkin/zipkin-go/reporter/http"

	"github.com/pteich/traefik/log"
)

// Name sets the name of this tracer
const Name = "zipkin"

// Config provides configuration settings for a zipkin tracer
type Config struct {
	HTTPEndpoint string `description:"HTTP Endpoint to report traces to." export:"false"`
	SameSpan     bool   `description:"Use ZipKin SameSpan RPC style traces." export:"true"`
	ID128Bit     bool   `description:"Use ZipKin 128 bit root span IDs." export:"true"`
	Debug        bool   `description:"Enable Zipkin debug." export:"true"`
}

// Setup sets up the tracer
func (c *Config) Setup(serviceName string) (opentracing.Tracer, io.Closer, error) {
	reporter := zipkinhttp.NewReporter(c.HTTPEndpoint)

	endpoint, err := zipkin.NewEndpoint(serviceName, "0.0.0.0:0")
	if err != nil {
		return nil, nil, err
	}

	nativeTracer, err := zipkin.NewTracer(reporter,
		zipkin.WithLocalEndpoint(endpoint),
		zipkin.WithSharedSpans(c.SameSpan),
		zipkin.WithTraceID128Bit(c.ID128Bit),
	)
	if err != nil {
		return nil, nil, err
	}

	tracer := zipkinot.Wrap(nativeTracer)
	// Without this, child spans are getting the NOOP tracer
	opentracing.SetGlobalTracer(tracer)

	log.Debug("Zipkin tracer configured")

	return tracer, reporter, nil
}
