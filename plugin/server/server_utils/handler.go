/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 *
 * mosdns is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * mosdns is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package server_utils

import (
	"fmt"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/pkg/server_handler"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
)

// NewHandler retains the default policy for existing listener constructors.
func NewHandler(bp *coremain.BP, entry string, enableAudit bool) (server.Handler, error) {
	source, err := ResolveRequestSource(entry, "")
	if err != nil {
		return nil, err
	}
	return NewHandlerWithSource(bp, entry, enableAudit, source)
}

// ResolveRequestSource accepts explicit server-owned provenance. Only the two
// original background entry names inherit a background policy when omitted.
func ResolveRequestSource(entry, option string) (server.RequestSource, error) {
	switch option {
	case "user":
		return server.RequestSourceUser, nil
	case "prewarm":
		return server.RequestSourcePrewarm, nil
	case "refresh":
		return server.RequestSourceRefresh, nil
	case "":
		switch entry {
		case "sequence_requery":
			return server.RequestSourcePrewarm, nil
		case "sequence_requery_refresh":
			return server.RequestSourceRefresh, nil
		default:
			return server.RequestSourceUser, nil
		}
	default:
		return server.RequestSourceUnspecified, fmt.Errorf("invalid request_source %q, expected user, prewarm or refresh", option)
	}
}

// NewHandlerWithSource sets the listener policy without overriding provenance
// that an internal replay already supplied in QueryMeta.
func NewHandlerWithSource(bp *coremain.BP, entry string, enableAudit bool, source server.RequestSource) (server.Handler, error) {
	if source == server.RequestSourceUnspecified {
		source, _ = ResolveRequestSource(entry, "")
	}
	p := bp.Plugin(entry)
	exec := sequence.ToExecutable(p)
	if exec == nil {
		return nil, fmt.Errorf("cannot find executable entry by tag %s", entry)
	}

	handlerOpts := server_handler.EntryHandlerOpts{
		Logger: bp.L(),
		Entry:  exec,
		// ADDED: Pass the enableAudit flag to the handler options.
		EnableAudit:   enableAudit,
		RequestSource: source,
	}
	return server_handler.NewEntryHandler(handlerOpts), nil
}
