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

package server

import (
	"context"
	"fmt"
	"net"
	"net/netip"

	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/miekg/dns"
	"go.uber.org/zap"
)

const (
	FastActionContinue = iota
	FastActionReply
)

type UDPServerOpts struct {
	Logger     *zap.Logger
	FastBypass func(reqLen int, buf []byte, clientAddr netip.AddrPort) (action int, respLen int, preMarks uint64, preDset string, preDsetMatched bool, preStaleRefresh bool)
	// FastBypassWithRuleMatch carries original mapper evidence to response storage.
	// When set it takes precedence over the legacy six-result callback.
	FastBypassWithRuleMatch func(reqLen int, buf []byte, clientAddr netip.AddrPort) (action int, respLen int, preMarks uint64, preDset string, preDsetMatched bool, preStaleRefresh bool, ruleMatch FastRuleMatchMeta)
}

func ServeUDP(c *net.UDPConn, h Handler, opts UDPServerOpts) error {
	logger := opts.Logger
	if logger == nil {
		logger = nopLogger
	}

	listenerCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(errListenerCtxCanceled)

	oobReader, oobWriter, err := initOobHandler(c)
	if err != nil {
		return fmt.Errorf("failed to init oob handler, %w", err)
	}
	batch := newUDPPacketBatch(c, oobReader != nil, opts.FastBypassWithRuleMatch != nil || opts.FastBypass != nil)
	defer batch.close()
	flush := func() {
		if err := batch.flush(); err != nil && !isListenerCloseErr(err) {
			logger.Warn("failed to write fast responses", zap.Error(err))
		}
	}

	for {
		count, err := batch.read()
		if err != nil {
			if isListenerCloseErr(err) {
				return nil
			}
			return fmt.Errorf("unexpected read err: %w", err)
		}
		for index := 0; index < count; index++ {
			message := &batch.receive[index]
			n, buf := message.N, message.Buffers[0]
			remoteAddr := batch.remote(index)

			var dstIpFromCm net.IP
			if oobReader != nil {
				dstIpFromCm, _ = oobReader(message.OOB[:message.NN])
			}

			var preMarks uint64
			var preDset string
			var preDsetMatched bool
			var preStaleRefresh bool
			var preRuleMatch FastRuleMatchMeta
			if opts.FastBypassWithRuleMatch != nil || opts.FastBypass != nil {
				var action, respLen int
				var marks uint64
				var dset string
				var dsetMatched, staleRefresh bool
				if opts.FastBypassWithRuleMatch != nil {
					action, respLen, marks, dset, dsetMatched, staleRefresh, preRuleMatch = opts.FastBypassWithRuleMatch(n, buf, remoteAddr)
				} else {
					action, respLen, marks, dset, dsetMatched, staleRefresh = opts.FastBypass(n, buf, remoteAddr)
				}
				if action == FastActionReply {
					if respLen > 0 {
						var oob []byte
						if oobWriter != nil && dstIpFromCm != nil {
							oob = oobWriter(dstIpFromCm)
						}
						batch.reply(index, respLen, oob)
					}
					continue
				}
				preMarks = marks
				preDset = dset
				preDsetMatched = dsetMatched
				preStaleRefresh = staleRefresh
			}

			// Do not let slow-path handling delay fast replies from earlier slots.
			flush()
			q := new(dns.Msg)
			if err := q.Unpack(buf[:n]); err != nil {
				logger.Warn("invalid msg", zap.Error(err), zap.Binary("msg", buf[:n]), zap.Stringer("from", remoteAddr))
				continue
			}

			// Control-message addresses can alias the receive slot reused next read.
			dstIpFromCm = append(net.IP(nil), dstIpFromCm...)
			go func() {
				payload := h.Handle(listenerCtx, q, QueryMeta{
					ClientAddr:           remoteAddr.Addr(),
					FromUDP:              true,
					PreFastFlags:         preMarks,
					PreFastRuleMatch:     preRuleMatch,
					PreFastDomainSet:     preDset,
					PreFastDomainMatched: preDsetMatched,
					PreFastStaleRefresh:  preStaleRefresh,
				}, pool.PackBuffer)

				if payload == nil {
					return
				}
				defer pool.ReleaseBuf(payload)

				var oob []byte
				if oobWriter != nil && dstIpFromCm != nil {
					oob = oobWriter(dstIpFromCm)
				}
				if _, _, err := c.WriteMsgUDPAddrPort(*payload, oob, remoteAddr); err != nil {
					logger.Warn("failed to write response", zap.Stringer("client", remoteAddr), zap.Error(err))
				}
			}()
		}
		flush()
	}
}

type getSrcAddrFromOOB func(oob []byte) (net.IP, error)
type writeSrcAddrToOOB func(a net.IP) []byte
