// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package middleware

import (
	"context"
	"time"

	"go.uber.org/atomic"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/psrpc"
)

type MetricRole int

const (
	_ MetricRole = iota
	ClientRole
	ServerRole
)

func (r MetricRole) String() string {
	switch r {
	case ClientRole:
		return "client"
	case ServerRole:
		return "server"
	default:
		return "invalid"
	}
}

// MetricsObserver 指标观察者
type MetricsObserver interface {
	OnUnaryRequest(role MetricRole, rpcInfo psrpc.RPCInfo, duration time.Duration, err error, rxBytes, txBytes int)
	OnMultiRequest(role MetricRole, rpcInfo psrpc.RPCInfo, duration time.Duration, responseCount, errorCount, rxBytes, txBytes int)
	OnStreamSend(role MetricRole, rpcInfo psrpc.RPCInfo, duration time.Duration, err error, bytes int)
	OnStreamRecv(role MetricRole, rpcInfo psrpc.RPCInfo, err error, bytes int)
	OnStreamOpen(role MetricRole, rpcInfo psrpc.RPCInfo)
	OnStreamClose(role MetricRole, rpcInfo psrpc.RPCInfo)
}

func WithClientMetrics(observer MetricsObserver) psrpc.ClientOption {
	return psrpc.WithClientOptions(
		psrpc.WithClientRPCInterceptors(newClientRPCMetricsInterceptor(observer)),
		psrpc.WithClientMultiRPCInterceptors(newMultiRPCMetricsInterceptor(observer)),
		psrpc.WithClientStreamInterceptors(newStreamMetricsInterceptor(observer, ClientRole)),
	)
}

func WithServerMetrics(observer MetricsObserver) psrpc.ServerOption {
	return psrpc.WithServerOptions(
		psrpc.WithServerRPCInterceptors(newServerRPCMetricsInterceptor(observer)),
		psrpc.WithServerStreamInterceptors(newStreamMetricsInterceptor(observer, ServerRole)),
	)
}

// newClientRPCMetricsInterceptor 创建一个客户端RPC拦截器
func newClientRPCMetricsInterceptor(observer MetricsObserver) psrpc.ClientRPCInterceptor {
	return func(rpcInfo psrpc.RPCInfo, next psrpc.ClientRPCHandler) psrpc.ClientRPCHandler {
		return func(ctx context.Context, req proto.Message, opts ...psrpc.RequestOption) (res proto.Message, err error) {
			start := time.Now()
			defer func() {
				observer.OnUnaryRequest(ClientRole, rpcInfo, time.Since(start), err, proto.Size(req), proto.Size(res))
			}()
			return next(ctx, req, opts...)
		}
	}
}

// newServerRPCMetricsInterceptor 创建一个服务器RPC拦截器
func newServerRPCMetricsInterceptor(observer MetricsObserver) psrpc.ServerRPCInterceptor {
	return func(ctx context.Context, req proto.Message, rpcInfo psrpc.RPCInfo, handler psrpc.ServerRPCHandler) (res proto.Message, err error) {
		start := time.Now()
		defer func() {
			if rpcInfo.Multi {
				var responseCount, errorCount int
				if err == nil {
					responseCount++
				} else {
					errorCount++
				}
				observer.OnMultiRequest(ServerRole, rpcInfo, time.Since(start), responseCount, errorCount, proto.Size(req), proto.Size(res))
			} else {
				observer.OnUnaryRequest(ServerRole, rpcInfo, time.Since(start), err, proto.Size(req), proto.Size(res))
			}
		}()
		return handler(ctx, req)
	}
}

// newStreamMetricsInterceptor 创建一个流拦截器
func newStreamMetricsInterceptor(observer MetricsObserver, role MetricRole) psrpc.StreamInterceptor {
	return func(rpcInfo psrpc.RPCInfo, next psrpc.StreamHandler) psrpc.StreamHandler {
		observer.OnStreamOpen(role, rpcInfo)
		return &streamMetricsInterceptor{
			StreamHandler: next,
			observer:      observer,
			role:          role,
			info:          rpcInfo,
		}
	}
}

type streamMetricsInterceptor struct {
	psrpc.StreamHandler
	observer MetricsObserver
	role     MetricRole
	info     psrpc.RPCInfo
	closed   atomic.Bool
}

func (s *streamMetricsInterceptor) Recv(msg proto.Message) (err error) {
	s.observer.OnStreamRecv(s.role, s.info, err, proto.Size(msg))
	return s.StreamHandler.Recv(msg)
}

func (s *streamMetricsInterceptor) Send(msg proto.Message, opts ...psrpc.StreamOption) (err error) {
	start := time.Now()
	defer func() { s.observer.OnStreamSend(s.role, s.info, time.Since(start), err, proto.Size(msg)) }()
	return s.StreamHandler.Send(msg, opts...)
}

func (s *streamMetricsInterceptor) Close(cause error) error {
	if !s.closed.Swap(true) {
		s.observer.OnStreamClose(s.role, s.info)
	}
	return s.StreamHandler.Close(cause)
}

func newMultiRPCMetricsInterceptor(observer MetricsObserver) psrpc.ClientMultiRPCInterceptor {
	return func(info psrpc.RPCInfo, next psrpc.ClientMultiRPCHandler) psrpc.ClientMultiRPCHandler {
		return &multiRPCMetricsInterceptor{
			ClientMultiRPCHandler: next,
			observer:              observer,
			start:                 time.Now(),
			info:                  info,
		}
	}
}

type multiRPCMetricsInterceptor struct {
	psrpc.ClientMultiRPCHandler
	observer      MetricsObserver
	start         time.Time
	info          psrpc.RPCInfo
	responseCount int
	errorCount    int
	rxBytes       int
	txBytes       int
}

func (r *multiRPCMetricsInterceptor) Send(ctx context.Context, req proto.Message, opts ...psrpc.RequestOption) error {
	r.start = time.Now()
	r.txBytes += proto.Size(req)
	return r.ClientMultiRPCHandler.Send(ctx, req, opts...)
}

func (r *multiRPCMetricsInterceptor) Recv(msg proto.Message, err error) {
	if err == nil {
		r.responseCount++
	} else {
		r.errorCount++
	}
	r.rxBytes += proto.Size(msg)
	r.ClientMultiRPCHandler.Recv(msg, err)
}

func (r *multiRPCMetricsInterceptor) Close() {
	r.observer.OnMultiRequest(ClientRole, r.info, time.Since(r.start), r.responseCount, r.errorCount, r.rxBytes, r.txBytes)
	r.ClientMultiRPCHandler.Close()
}
