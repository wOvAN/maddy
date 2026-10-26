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

// Package api implements an in-process HTTP management endpoint.
//
// maddy upstream deliberately ships no remote administration interface, so the
// supported path is the maddyctl subcommands in internal/cli/ctl, which
// re-parse maddy.conf and open the databases in a second process. This module
// reaches the same module instances (creds, storage) in-process through the
// usual config directives, which is what lets a web panel act without
// fork/exec and without a second copy of the schema being opened by a binary
// of a possibly different version.
package api

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/foxcpp/maddy/framework/config"
	modconfig "github.com/foxcpp/maddy/framework/config/module"
	"github.com/foxcpp/maddy/framework/container"
	"github.com/foxcpp/maddy/framework/log"
	"github.com/foxcpp/maddy/framework/module"
	"github.com/foxcpp/maddy/framework/module/modules"
	"github.com/foxcpp/maddy/framework/resource/netresource"
	"github.com/foxcpp/maddy/internal/authz"
)

const modName = "api"

type Endpoint struct {
	addrs     []string
	endpoints []config.Endpoint
	log       *log.Logger

	creds     module.PlainUserDB
	store     module.Storage
	admins    map[string]struct{}
	secret    []byte
	normalize authz.NormalizeFunc

	loginLimit  int
	loginWindow time.Duration
	maxMsgSize  int64

	limiter *rateLimiter

	listenersWg sync.WaitGroup
	serv        http.Server
	startTime   time.Time
}

func New(c *container.C, _ string, args []string) (container.LifetimeModule, error) {
	return &Endpoint{
		addrs: args,
		log:   c.DefaultLogger.Sublogger(modName),
	}, nil
}

func (e *Endpoint) Configure(_ []string, cfg *config.Map) error {
	var adminsList []string
	var secretStr string

	cfg.Custom("creds", false, false, nil, func(m *config.Map, node config.Node) (interface{}, error) {
		var creds module.PlainUserDB
		if err := modconfig.ModuleFromNode("auth", node.Args, node, m.Globals, &creds); err != nil {
			return nil, err
		}
		return creds, nil
	}, &e.creds)
	cfg.Custom("storage", false, false, nil, modconfig.StorageDirective, &e.store)
	cfg.StringList("admins", false, true, nil, &adminsList)
	cfg.String("secret", false, true, "", &secretStr)
	cfg.Int("login_rate_limit", false, false, 10, &e.loginLimit)
	cfg.Duration("login_rate_window", false, false, 5*time.Minute, &e.loginWindow)
	cfg.DataSize("max_msg_size", false, false, 32*1024*1024, &e.maxMsgSize)
	cfg.Bool("debug", true, false, &e.log.Debug)

	if _, err := cfg.Process(); err != nil {
		return err
	}

	// creds/storage are checked in Start, not here, so that the module can be
	// configured in unit tests without building a module registry.
	if len(adminsList) == 0 {
		return fmt.Errorf("%s: admins must list at least one administrator", modName)
	}
	if len(secretStr) < 16 {
		return fmt.Errorf("%s: secret must be at least 16 bytes", modName)
	}
	// A zero window makes the limiter allow everything, i.e. it turns a
	// mandatory control off without saying so; negative durations are already
	// rejected by config.Map.Duration.
	if e.loginLimit <= 0 || e.loginWindow <= 0 {
		return fmt.Errorf("%s: login_rate_limit and login_rate_window must be positive", modName)
	}
	e.normalize = authz.NormalizeAuto
	if err := e.setAdmins(adminsList); err != nil {
		return err
	}
	e.secret = []byte(secretStr)

	for _, a := range e.addrs {
		endp, err := config.ParseEndpoint(a)
		if err != nil {
			return fmt.Errorf("%s: malformed endpoint: %v", modName, err)
		}
		if endp.IsTLS() {
			return fmt.Errorf("%s: TLS is not supported yet", modName)
		}
		e.endpoints = append(e.endpoints, endp)
	}

	// The limiter must exist before the first request, not before handler():
	// the routes only read e.limiter at request time.
	e.limiter = newRateLimiter(e.loginLimit, e.loginWindow)

	e.serv.Handler = e.handler()
	e.serv.ReadHeaderTimeout = 10 * time.Second
	return nil
}

func (e *Endpoint) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/login", e.handleLogin)
	mux.HandleFunc("GET /api/v1/health", e.handleHealth)
	// Stub behind the middleware so the auth surface is live from day one;
	// the real user listing lands with the user management endpoints.
	mux.HandleFunc("GET /api/v1/users", e.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"users": []string{}})
	}))
	return mux
}

func (e *Endpoint) setAdmins(list []string) error {
	e.admins = make(map[string]struct{}, len(list))
	for _, a := range list {
		normalized, err := e.normalize(a)
		if err != nil {
			return fmt.Errorf("%s: malformed administrator address: %v", modName, err)
		}
		e.admins[normalized] = struct{}{}
	}
	return nil
}

func (e *Endpoint) isAdmin(username string) bool {
	normalized, err := e.normalize(username)
	if err != nil {
		return false
	}
	_, ok := e.admins[normalized]
	return ok
}

func (e *Endpoint) Name() string { return modName }

func (e *Endpoint) InstanceName() string { return "" }

func (e *Endpoint) Start() error {
	if e.creds == nil || e.store == nil {
		return fmt.Errorf("%s: creds and storage directives are required", modName)
	}

	for _, endp := range e.endpoints {
		l, err := netresource.Listen(endp.Network(), endp.Address())
		if err != nil {
			if err := e.Stop(); err != nil {
				e.log.Error("failed to stop after failed listen", err)
			}
			return fmt.Errorf("%s: %v", modName, err)
		}

		e.listenersWg.Add(1)
		go func() {
			defer func() {
				if p := recover(); p != nil {
					e.log.Error("serve panicked", fmt.Errorf("%v", p))
				}
				e.listenersWg.Done()
			}()
			e.log.Println("listening on", endp.String())
			err := e.serv.Serve(l)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				e.log.Error("serve failed", err, "endpoint", endp.String())
			}
		}()
	}
	e.startTime = time.Now()
	return nil
}

func (e *Endpoint) Stop() error {
	if err := e.serv.Close(); err != nil {
		return err
	}
	e.listenersWg.Wait()
	return nil
}

func init() {
	modules.RegisterEndpoint(modName, New)
}
