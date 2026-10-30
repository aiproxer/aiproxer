package conformance

import (
	"context"
	"fmt"
	"io"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type memStream struct {
	ctx    context.Context
	inbox  []backendplugin.ClientFrame
	outbox []backendplugin.ServerFrame
	ri     int
}

func (m *memStream) Context() context.Context { return m.ctx }

func (m *memStream) Recv() (backendplugin.ClientFrame, error) {
	if m.ri >= len(m.inbox) {
		return backendplugin.ClientFrame{}, io.EOF
	}
	f := m.inbox[m.ri]
	m.ri++
	return f, nil
}

func (m *memStream) Send(frame backendplugin.ServerFrame) error {
	if err := frame.ValidateShape(); err != nil {
		return err
	}
	if err := backendplugin.ValidateServerFrameBounds(frame); err != nil {
		return err
	}
	m.outbox = append(m.outbox, frame)
	return nil
}

func sampleInvocation(model string) backendplugin.Invocation {
	if model == "" {
		model = "fake-model"
	}
	text := "hi"
	return backendplugin.Invocation{
		RequestID: "r1", AttemptID: "a1", ALegID: "aleg", BLegID: "bleg",
		CanonicalModelID: model,
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
}

func configure(ctx context.Context, svc backendplugin.Service, id string, opts Options) (backendplugin.ConfiguredInstance, backendplugin.PluginDescriptor, error) {
	desc, err := svc.Describe(ctx)
	if err != nil {
		return nil, backendplugin.PluginDescriptor{}, err
	}
	if err := desc.Validate(); err != nil {
		return nil, desc, err
	}
	host := backendplugin.ProtocolOffer{Major: 1, Minor: 0, DisableTransportRetries: true, Features: desc.Features}
	plugin := backendplugin.ProtocolOffer{Major: 1, Minor: 0, DisableTransportRetries: true, Features: desc.Features}
	neg, err := backendplugin.Negotiate(host, plugin)
	if err != nil {
		return nil, desc, err
	}
	cfgYAML := opts.ConfigYAML
	if len(cfgYAML) == 0 {
		cfgYAML = []byte("kind: fake\n")
	}
	kind := opts.FactoryKind
	if kind == "" {
		kind = desc.Factories[0].Kind
	}
	inst, err := svc.Configure(ctx, backendplugin.ConfigureRequest{
		InstanceID:  id,
		FactoryKind: kind,
		ConfigYAML:  cfgYAML,
		Negotiation: neg,
		RuntimePolicy: backendplugin.RuntimePolicy{
			DisableTransportRetries: true,
			MaxRequestBytes:         backendplugin.DefaultMaxMessageBytes,
			MaxStreamFrameBytes:     backendplugin.DefaultMaxStreamFrameBytes,
		},
	})
	return inst, desc, err
}

func pass(name string) Result { return Result{Name: name, Passed: true} }

func fail(name, detail, stable string) Result {
	return Result{Name: name, Passed: false, Detail: detail, Stable: stable}
}

func require(cond bool, name, detail, stable string) Result {
	if cond {
		return pass(name)
	}
	return fail(name, detail, stable)
}

// runExecute threads the runner ctx into the stream (memStream must carry a ctx to
// satisfy ExecuteStream.Context); context.Background is never substituted here.
func runExecute(ctx context.Context, inst backendplugin.ConfiguredInstance, inbox ...backendplugin.ClientFrame) ([]backendplugin.ServerFrame, error) {
	ms := &memStream{ctx: ctx, inbox: inbox}
	err := inst.Execute(ms)
	return ms.outbox, err
}

func validateServerStream(frames []backendplugin.ServerFrame) error {
	var v backendplugin.StreamValidator
	for _, f := range frames {
		if err := v.Push(f); err != nil {
			return err
		}
	}
	return nil
}

func advertisedFactory(desc backendplugin.PluginDescriptor) (backendplugin.FactoryDescriptor, error) {
	return advertisedFactoryKind(desc, "")
}

func advertisedFactoryKind(desc backendplugin.PluginDescriptor, kind string) (backendplugin.FactoryDescriptor, error) {
	if len(desc.Factories) == 0 {
		return backendplugin.FactoryDescriptor{}, fmt.Errorf("no factories")
	}
	if kind == "" {
		return desc.Factories[0], nil
	}
	for _, f := range desc.Factories {
		if f.Kind == kind {
			return f, nil
		}
	}
	return backendplugin.FactoryDescriptor{}, fmt.Errorf("factory kind %q not advertised", kind)
}
