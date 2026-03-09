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

package transport

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/dnsutils"
	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
)

type dummyEchoNetConn struct {
	net.Conn
	rErrProb float64
	rLatency time.Duration
	wErrProb float64

	closeOnce sync.Once
}

func newDummyEchoNetConn(rErrProb float64, rLatency time.Duration, wErrProb float64) NetConn {
	c1, c2 := net.Pipe()
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		defer c1.Close()
		defer c2.Close()
		for {
			m, readErr := dnsutils.ReadRawMsgFromTCP(c2)
			if m != nil {
				go func() {
					defer pool.ReleaseBuf(m)
					if rLatency > 0 {
						t := time.NewTimer(rLatency)
						defer t.Stop()
						select {
						case <-t.C:
						case <-ctx.Done():
							return
						}
					}
					_, _ = dnsutils.WriteRawMsgToTCP(c2, *m)
				}()
			}
			if readErr != nil {
				return
			}
		}
	}()

	return &dummyEchoNetConn{
		Conn:     c1,
		rErrProb: rErrProb,
		rLatency: rLatency,
		wErrProb: wErrProb,
	}
}

func (d *dummyEchoNetConn) Read(p []byte) (n int, err error) {
	if rand.Float64() < d.rErrProb {
		return 0, errors.New("read err")
	}
	return d.Conn.Read(p)
}

func (d *dummyEchoNetConn) Write(p []byte) (n int, err error) {
	if rand.Float64() < d.wErrProb {
		return 0, errors.New("write err")
	}
	return d.Conn.Write(p)
}

func (d *dummyEchoNetConn) Close() error {
	var err error
	d.closeOnce.Do(func() {
		err = d.Conn.Close()
	})
	return err
}
