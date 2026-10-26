/*
Maddy Mail Server - Composable all-in-one email server.
Copyright © 2019-2020 Max Mazurov <fox.cpp@disroot.org>, Maddy Mail Server contributors

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package api

import (
	"strings"
	"testing"
	"time"

	"github.com/foxcpp/maddy/framework/config"
	"github.com/foxcpp/maddy/framework/container"
	"github.com/foxcpp/maddy/framework/log"
	"github.com/foxcpp/maddy/framework/module"
	"github.com/foxcpp/maddy/framework/module/modules"
)

// The creds and storage directives are required in the config map, so unit
// tests resolve them through the real inline-module machinery; these factories
// hand it the same doubles the handler tests inject.
func init() {
	modules.Register("auth.apifake", func(_ *container.C, _, _ string) (module.Module, error) {
		return newFakeCreds(), nil
	})
	modules.Register("storage.apifake", func(_ *container.C, _, _ string) (module.Module, error) {
		return newFakeStorage(), nil
	})
}

type cfgEndpointOpts struct {
	children []config.Node
	admins   []string
	secret   string
	// omit drops the injected creds/storage child, to pin the requiredness.
	omit string
}

func configureTestEndpoint(t *testing.T, o cfgEndpointOpts) (*Endpoint, error) {
	t.Helper()
	c := container.New()
	c.DefaultLogger = &log.DefaultLogger
	lm, err := New(c, "api", []string{"tcp://127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	endp := lm.(*Endpoint)

	node := config.Node{Name: "api", Args: []string{"api"}}
	if o.secret != "" {
		node.Children = append(node.Children, config.Node{
			Name: "secret", Args: []string{o.secret},
		})
	}
	if len(o.admins) != 0 {
		node.Children = append(node.Children, config.Node{
			Name: "admins", Args: o.admins,
		})
	}
	if o.omit != "creds" {
		node.Children = append(node.Children, config.Node{
			Name: "creds", Args: []string{"apifake"},
		})
	}
	if o.omit != "storage" {
		node.Children = append(node.Children, config.Node{
			Name: "storage", Args: []string{"apifake"},
		})
	}
	node.Children = append(node.Children, o.children...)

	return endp, endp.Configure(nil, config.NewMap(nil, node))
}

func TestConfigDefaults(t *testing.T) {
	e, err := configureTestEndpoint(t, cfgEndpointOpts{
		admins: []string{"admin@example.org"},
		secret: "0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.loginLimit != 10 || e.loginWindow != 5*time.Minute {
		t.Fatalf("login rate limit defaults: %d/%s", e.loginLimit, e.loginWindow)
	}
	if e.maxMsgSize != 32*1024*1024 {
		t.Fatalf("max_msg_size default: %d", e.maxMsgSize)
	}
}

func TestConfigRequiresAdmins(t *testing.T) {
	// An empty allowlist would let every mailbox owner log in as an
	// administrator. The block must not configure at all.
	_, err := configureTestEndpoint(t, cfgEndpointOpts{secret: "0123456789abcdef0123456789abcdef"})
	if err == nil {
		t.Fatal("expected an error for empty admins")
	}
}

func TestConfigRequiresLongSecret(t *testing.T) {
	_, err := configureTestEndpoint(t, cfgEndpointOpts{
		admins: []string{"admin@example.org"},
		secret: "short",
	})
	if err == nil {
		t.Fatal("expected an error for a short secret")
	}
}

func TestConfigRequiresPositiveRateLimit(t *testing.T) {
	// A zero value turns the limiter into allow-everything, i.e. it switches
	// off a mandatory control without saying so.
	for _, tc := range []struct {
		name string
		arg  string
	}{
		{"login_rate_limit", "0"},
		{"login_rate_window", "0s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := configureTestEndpoint(t, cfgEndpointOpts{
				admins:   []string{"admin@example.org"},
				secret:   "0123456789abcdef0123456789abcdef",
				children: []config.Node{{Name: tc.name, Args: []string{tc.arg}}},
			})
			if err == nil {
				t.Fatal("expected an error for a zero rate limit control")
			}
		})
	}
}

func TestConfigRequiresCredsStorage(t *testing.T) {
	// maddy verify-config runs Configure only: a block this module refuses to
	// start with must fail there, not only at boot. The map, not Start, is
	// where the validation step sees it.
	for _, omit := range []string{"creds", "storage"} {
		_, err := configureTestEndpoint(t, cfgEndpointOpts{
			admins: []string{"admin@example.org"},
			secret: "0123456789abcdef0123456789abcdef",
			omit:   omit,
		})
		if err == nil {
			t.Fatalf("missing %s must be a configuration error", omit)
		}
		if !strings.Contains(err.Error(), omit) {
			t.Fatalf("missing %s: the error must name the directive: %v", omit, err)
		}
	}
}

func TestConfigAdminsAreNormalized(t *testing.T) {
	e, err := configureTestEndpoint(t, cfgEndpointOpts{
		admins: []string{"ADMIN@Example.ORG"},
		secret: "0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !e.isAdmin("admin@example.org") {
		t.Fatal("admins must be compared case-insensitively")
	}
	if e.isAdmin("nobody@example.org") {
		t.Fatal("unlisted address must not be an admin")
	}
}

func TestConfigRejectsUnknownDirective(t *testing.T) {
	_, err := configureTestEndpoint(t, cfgEndpointOpts{
		admins:   []string{"admin@example.org"},
		secret:   "0123456789abcdef0123456789abcdef",
		children: []config.Node{{Name: "nonsense", Args: []string{"1"}}},
	})
	if err == nil {
		t.Fatal("unknown directives must be rejected")
	}
}

// fakeCreds/fakeStorage are the test doubles reused by every handler
// test below; they implement the module role interfaces the endpoint binds to.
var (
	_ module.PlainUserDB       = (*fakeCreds)(nil)
	_ module.ManageableStorage = (*fakeStorage)(nil)
)
