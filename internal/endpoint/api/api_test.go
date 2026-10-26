package api

import (
	"net"
	"testing"

	"github.com/foxcpp/maddy/framework/config"
	"github.com/foxcpp/maddy/framework/container"
	"github.com/foxcpp/maddy/framework/log"
	"github.com/foxcpp/maddy/framework/module/modules"
)

func TestEndpointRegistered(t *testing.T) {
	factory := modules.GetEndpoint("api")
	if factory == nil {
		t.Fatal("api endpoint is not registered")
	}

	c := container.New()
	c.DefaultLogger = &log.DefaultLogger
	endp, err := factory(c, "api", []string{"tcp://127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if endp.Name() != "api" {
		t.Fatalf("unexpected module name: %s", endp.Name())
	}
	if endp.InstanceName() != "" {
		t.Fatalf("endpoint instances must be unnamed, got: %s", endp.InstanceName())
	}
}

func TestEndpointServes(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().(*net.TCPAddr).String()
	l.Close()

	endp, err := newTestEndpoint(addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := endp.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := endp.Stop(); err != nil {
			t.Fatal(err)
		}
	}()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal("nothing is listening after Start:", err)
	}
	conn.Close()
}

func newTestEndpoint(addr string) (*Endpoint, error) {
	c := container.New()
	c.DefaultLogger = &log.DefaultLogger
	lm, err := New(c, "api", []string{"tcp://" + addr})
	if err != nil {
		return nil, err
	}
	endp := lm.(*Endpoint)
	cfg := config.NewMap(nil, config.Node{Name: "api", Args: []string{"api", "tcp://" + addr}})
	if err := endp.Configure(nil, cfg); err != nil {
		return nil, err
	}
	return endp, nil
}
