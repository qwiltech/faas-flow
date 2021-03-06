package main

import (
	"fmt"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"log"
	//"github.com/opentracing/opentracing-go/log"
	"github.com/uber/jaeger-client-go"
	"github.com/uber/jaeger-client-go/config"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var (
	closer            io.Closer
	reqSpan           opentracing.Span
	reqSpanCtx        opentracing.SpanContext
	tracerInitialized bool
	nodeSpans         map[string]opentracing.Span
)

// EnvHeadersCarrier satisfies both TextMapWriter and TextMapReader
type EnvHeadersCarrier struct {
	envMap map[string]string
}

// buildEnvHeadersCarrier builds a EnvHeadersCarrier from env
func buildEnvHeadersCarrier() *EnvHeadersCarrier {
	carrier := &EnvHeadersCarrier{}
	carrier.envMap = make(map[string]string)

	for _, e := range os.Environ() {
		if i := strings.Index(e, "="); i >= 0 {
			if e[:i] == "Http_Uber_Trace_Id" {
				key := "uber-trace-id"
				carrier.envMap[key] = e[i+1:]
				break
			}
		}
	}

	return carrier
}

// ForeachKey conforms to the TextMapReader interface
func (c *EnvHeadersCarrier) ForeachKey(handler func(key, val string) error) error {
	for key, value := range c.envMap {
		err := handler(key, value)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ForeachKey key %s value %s, error %v",
				key, value, err)
			return err
		}
	}
	return nil
}

// Set conforms to the TextMapWriter interface
func (c *EnvHeadersCarrier) Set(key, val string) {
	c.envMap[key] = val
}

// isTracingEnabled check if tracing enabled for the function
func isTracingEnabled() bool {
	tracing := os.Getenv("enable_tracing")
	if strings.ToUpper(tracing) == "TRUE" {
		return true
	}
	return false
}

// getTraceServer get the traceserver address
func getTraceServer() string {
	traceServer := os.Getenv("trace_server")
	if traceServer == "" {
		traceServer = "jaegertracing:5775"
	}
	return traceServer
}

// initGlobalTracer init global trace with configuration
func initGlobalTracer(flowName string) error {

	if !isTracingEnabled() {
		log.Printf("tracing is disabled")
		return nil
	}

	agentPort := getTraceServer()

	log.Printf("tracing is enabled, agent %s", agentPort)

	cfg := config.Configuration{
		ServiceName: flowName,
		Sampler: &config.SamplerConfig{
			Type:  "const",
			Param: 1,
		},
		Reporter: &config.ReporterConfig{
			LogSpans:            true,
			BufferFlushInterval: 1 * time.Second,
			LocalAgentHostPort:  agentPort,
		},
	}

	tracer, traceCloser, err := cfg.NewTracer(
		config.Logger(jaeger.StdLogger),
	)
	if err != nil {
		return fmt.Errorf("Failed to init tracer, error %v", err.Error())
	}

	closer = traceCloser

	opentracing.SetGlobalTracer(tracer)

	tracerInitialized = true

	nodeSpans = make(map[string]opentracing.Span)

	return nil
}

// startReqSpan starts a request span
func startReqSpan(reqId string) {
	if !isTracingEnabled() || !tracerInitialized {
		return
	}

	reqSpan = opentracing.GlobalTracer().StartSpan(reqId)
	reqSpan.SetTag("request", reqId)

	reqSpanCtx = reqSpan.Context()
}

// continueReqSpan continue request span
func continueReqSpan(reqId string) {
	var err error

	if !isTracingEnabled() || !tracerInitialized {
		return
	}

	carrier := buildEnvHeadersCarrier()
	reqSpanCtx, err = opentracing.GlobalTracer().Extract(
		opentracing.HTTPHeaders,
		carrier,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to continue req span tracing, error %v\n", err)
		return
	}

	reqSpan = nil
	// TODO: Its not Supported to get span from spanContext as of now
	//       https://github.com/opentracing/specification/issues/81
	//       it will support us to extend the request span for nodes
	//reqSpan = opentracing.SpanFromContext(reqSpanCtx)
}

// extendReqSpan extend req span over a request
// func extendReqSpan(url string, req *http.Request) {
func extendReqSpan(lastPhaseRef string, url string, req *http.Request) {
	if !isTracingEnabled() || !tracerInitialized {
		return
	}

	// TODO: as requestSpan can't be regenerated with the span context we
	//       forward the nodeSpan's SpanContext
	// span := reqSpan
	span := nodeSpans[lastPhaseRef]

	ext.SpanKindRPCClient.Set(span)
	ext.HTTPUrl.Set(span, url)
	ext.HTTPMethod.Set(span, "POST")
	span.Tracer().Inject(
		span.Context(),
		opentracing.HTTPHeaders,
		opentracing.HTTPHeadersCarrier(req.Header),
	)
}

// stopReqSpan terminate a request span
func stopReqSpan() {
	if !isTracingEnabled() || !tracerInitialized {
		return
	}

	if reqSpan == nil {
		return
	}

	reqSpan.Finish()
}

// startNodeSpan starts a node span
func startNodeSpan(node string, reqId string) {
	var nodeSpan opentracing.Span
	if !isTracingEnabled() || !tracerInitialized {
		return
	}

	nodeSpan = opentracing.GlobalTracer().StartSpan(
		node, ext.RPCServerOption(reqSpanCtx))
	nodeSpan.SetTag("async", "true")
	nodeSpan.SetTag("request", reqId)
	/*
		if reqSpan == nil {

		} else {
			nodeSpan = opentracing.GlobalTracer().StartSpan(
				node, opentracing.ChildOf(reqSpan.Context()))
		}
	*/
	nodeSpan.SetTag("node", node)
	nodeSpans[node] = nodeSpan
}

// stopNodeSpan terminates a node span
func stopNodeSpan(node string) {
	if !isTracingEnabled() || !tracerInitialized {
		return
	}

	nodeSpans[node].Finish()
}

// flushTracer flush all pending traces
func flushTracer() {
	if !isTracingEnabled() || !tracerInitialized {
		return
	}

	closer.Close()
}
