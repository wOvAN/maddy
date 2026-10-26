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
	"github.com/foxcpp/maddy/framework/container"
	"github.com/foxcpp/maddy/framework/log"
	"github.com/foxcpp/maddy/framework/module/modules"
	"github.com/foxcpp/maddy/framework/resource/netresource"
)

const modName = "api"

type Endpoint struct {
	addrs     []string
	endpoints []config.Endpoint
	log       *log.Logger

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
	if _, err := cfg.Process(); err != nil {
		return err
	}

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

	e.serv.Handler = e.handler()
	e.serv.ReadHeaderTimeout = 10 * time.Second
	return nil
}

func (e *Endpoint) handler() http.Handler {
	mux := http.NewServeMux()
	return mux
}

func (e *Endpoint) Name() string { return modName }

func (e *Endpoint) InstanceName() string { return "" }

func (e *Endpoint) Start() error {
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
