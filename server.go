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

package psrpc

import (
	"context"
	"time"

	"google.golang.org/protobuf/proto"
)

const DefaultServerTimeout = time.Second * 3 // 默认服务器超时时间

// ServerOption 服务器选项
type ServerOption func(*ServerOpts)

// ServerOpts 服务器信息的结构体
type ServerOpts struct {
	ServerID           string                 // 服务器ID
	Timeout            time.Duration          // 超时时间
	ChannelSize        int                    // 通道大小
	Interceptors       []ServerRPCInterceptor // 服务器RPC拦截器
	StreamInterceptors []StreamInterceptor    // 流拦截器
	ChainedInterceptor ServerRPCInterceptor   // 链式拦截器 把多个服务器拦截器Interceptors串联起来，形成一个拦截器
}

// WithServerID 设置服务器ID
func WithServerID(id string) ServerOption {
	return func(o *ServerOpts) {
		o.ServerID = id
	}
}

// WithServerTimeout 设置服务器超时时间
func WithServerTimeout(timeout time.Duration) ServerOption {
	return func(o *ServerOpts) {
		o.Timeout = timeout
	}
}

// WithServerChannelSize 设置服务器通道大小
func WithServerChannelSize(size int) ServerOption {
	return func(o *ServerOpts) {
		if size > 0 {
			o.ChannelSize = size
		}
	}
}

// Server interceptors wrap the service implementation
// ServerRPCInterceptor 服务器RPC拦截器函数的类型
type ServerRPCInterceptor func(ctx context.Context, req proto.Message, info RPCInfo, handler ServerRPCHandler) (proto.Message, error)

// ServerRPCHandler 服务器RPC处理函数的类型
type ServerRPCHandler func(context.Context, proto.Message) (proto.Message, error)

// WithServerRPCInterceptors 设置服务器RPC拦截器
func WithServerRPCInterceptors(interceptors ...ServerRPCInterceptor) ServerOption {
	return func(o *ServerOpts) {
		for _, interceptor := range interceptors {
			if interceptor != nil {
				o.Interceptors = append(o.Interceptors, interceptor)
			}
		}
	}
}

// WithServerStreamInterceptors 设置服务器流拦截器
func WithServerStreamInterceptors(interceptors ...StreamInterceptor) ServerOption {
	return func(o *ServerOpts) {
		o.StreamInterceptors = append(o.StreamInterceptors, interceptors...)
	}
}

// WithServerOptions 将多个服务器选项组合成一个选项
func WithServerOptions(opts ...ServerOption) ServerOption {
	return func(o *ServerOpts) {
		for _, opt := range opts {
			opt(o)
		}
	}
}
