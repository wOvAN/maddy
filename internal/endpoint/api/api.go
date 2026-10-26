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
	"context"
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

	cfg.Custom("creds", false, true, nil, func(m *config.Map, node config.Node) (interface{}, error) {
		var creds module.PlainUserDB
		if err := modconfig.ModuleFromNode("auth", node.Args, node, m.Globals, &creds); err != nil {
			return nil, err
		}
		return creds, nil
	}, &e.creds)
	cfg.Custom("storage", false, true, nil, modconfig.StorageDirective, &e.store)
	cfg.StringList("admins", false, true, nil, &adminsList)
	cfg.String("secret", false, true, "", &secretStr)
	cfg.Int("login_rate_limit", false, false, 10, &e.loginLimit)
	cfg.Duration("login_rate_window", false, false, 5*time.Minute, &e.loginWindow)
	cfg.DataSize("max_msg_size", false, false, 32*1024*1024, &e.maxMsgSize)
	cfg.Bool("debug", true, false, &e.log.Debug)

	if _, err := cfg.Process(); err != nil {
		return err
	}

	// creds and storage are marked required in the map (imap.go:97 is the
	// precedent) so that `maddy verify-config`, which runs Configure only,
	// sees a block this module refuses to start with; the Start check below
	// stays as the belt for programmatically built instances.
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
	type route struct {
		method  string
		pattern string
		h       http.HandlerFunc
	}
	routes := []route{
		{http.MethodPost, "/api/v1/login", e.handleLogin},
		{http.MethodGet, "/api/v1/health", e.handleHealth},
		{http.MethodGet, "/api/v1/users", e.requireAdmin(e.handleUsersList)},
		{http.MethodPost, "/api/v1/users", e.requireAdmin(e.handleUsersCreate)},
		{http.MethodDelete, "/api/v1/users/{user}", e.requireAdmin(e.handleUsersDelete)},
		{http.MethodPut, "/api/v1/users/{user}/password", e.requireAdmin(e.handleUserPassword)},
		{http.MethodPut, "/api/v1/users/{user}/appendlimit", e.requireAdmin(e.handleUserAppendlimit)},
		{http.MethodGet, "/api/v1/users/{user}/mailboxes", e.requireAdmin(e.handleMboxesList)},
		{http.MethodPost, "/api/v1/users/{user}/mailboxes", e.requireAdmin(e.handleMboxesCreate)},
		{http.MethodPut, "/api/v1/users/{user}/mailboxes/{mbox}", e.requireAdmin(e.handleMboxesRename)},
		{http.MethodDelete, "/api/v1/users/{user}/mailboxes/{mbox}", e.requireAdmin(e.handleMboxesDelete)},
		{http.MethodGet, "/api/v1/users/{user}/mailboxes/{mbox}/messages", e.requireAdmin(e.handleMsgsList)},
		{http.MethodPost, "/api/v1/users/{user}/mailboxes/{mbox}/messages", e.requireAdmin(e.handleMsgsAppend)},
		{http.MethodGet, "/api/v1/users/{user}/mailboxes/{mbox}/messages/{uid}", e.requireAdmin(e.handleMsgsGet)},
		{http.MethodDelete, "/api/v1/users/{user}/mailboxes/{mbox}/messages/{uid}", e.requireAdmin(e.handleMsgsDelete)},
	}

	mux := http.NewServeMux()
	// The methods to probe on a miss are derived from the route table, not
	// listed separately: a new method registered here is probed automatically.
	methods := make(map[string]struct{}, len(routes))
	for _, rt := range routes {
		mux.HandleFunc(rt.method+" "+rt.pattern, rt.h)
		methods[rt.method] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		if pattern != "" {
			// Serve through the mux, not the returned handler: Handler does not
			// populate named wildcards, so h.ServeHTTP would make every
			// r.PathValue("user") read as the empty string.
			mux.ServeHTTP(w, r)
			return
		}
		// A method miss and a path miss look the same from here (both report an
		// empty pattern); ask the mux whether some other registered method
		// matches this path to tell them apart.
		probe := *r
		for m := range methods {
			if m == r.Method {
				continue
			}
			probe.Method = m
			if _, p := mux.Handler(&probe); p != "" {
				mux.ServeHTTP(w, r)
				return
			}
		}
		writeErr(w, http.StatusNotFound, "no such endpoint", r.URL.Path, nil)
	})
}

// ctxAdminUser reads the authenticated subject set by requireAdmin.
func ctxAdminUser(ctx context.Context) string {
	user, _ := ctx.Value(ctxAdmin).(string)
	return user
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

	// Read-side deadlines: the login route costs a bcrypt comparison, so a
	// trickle-body client must not be able to pin a connection (and a
	// goroutine) for the whole request rate. No WriteTimeout: a message body
	// fetch writes up to max_msg_size (32 MiB by default) and a write deadline
	// would cut legitimate large fetches at an arbitrary byte count.
	e.serv.ReadTimeout = 30 * time.Second
	e.serv.IdleTimeout = 2 * time.Minute

	// Written before the serve goroutines start: handleHealth reads it from
	// request goroutines, and a time.Time write racing those reads is a data
	// race, not just a smell.
	e.startTime = time.Now()

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
	return nil
}

func (e *Endpoint) Stop() error {
	if err := e.serv.Close(); err != nil {
		return err
	}
	e.listenersWg.Wait()
	return nil
}

// EarlyStop drains in-flight requests before the reloaded container's instance
// binds. openmetrics uses http.Server.Close, which cuts running requests;
// management requests write to the databases, so they get a deadline instead.
//
// The port itself is safe: netresource.ListenerTracker hands out a duplicated
// fd, so closing this instance's listener does not stop the tracked port from
// accepting for the new instance.
//
// Nothing is returned, not even for a drain that ran out of its deadline:
// moduleReload aborts the reload when EarlyStop fails, and a slow fetch must not
// roll the whole reload back. Past the deadline the request keeps writing while
// the new instance is already serving, and the old container's Stop cuts it.
func (e *Endpoint) EarlyStop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.serv.Shutdown(ctx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			e.log.Error("drain timed out", err)
		} else {
			e.log.Error("shutdown failed", err)
		}
	}
	e.listenersWg.Wait()
	return nil
}

// The endpoint takes part in the reload lifecycle, not only in start/stop;
// assert that contract compiles rather than discovering it at boot.
var _ container.EarlyStopModule = &Endpoint{}

func init() {
	modules.RegisterEndpoint(modName, New)
}
