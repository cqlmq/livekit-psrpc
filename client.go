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

const (
	DefaultClientTimeout        = time.Second * 3        // 默认客户端超时时间
	DefaultAffinityTimeout      = time.Second            // 默认亲和力超时时间
	DefaultAffinityShortCircuit = time.Millisecond * 200 // 默认亲和力短路时间
)

// ClientOption 客户端选项函数类型
type ClientOption func(*ClientOpts)

// ClientOpts 客户端选项结构体
type ClientOpts struct {
	ClientID             string                      // 客户端ID
	Timeout              time.Duration               // 超时时间
	SelectionTimeout     time.Duration               // 选择超时时间
	ChannelSize          int                         // 通道大小
	EnableStreams        bool                        // 是否启用流
	RequestHooks         []ClientRequestHook         // 请求钩子函数
	ResponseHooks        []ClientResponseHook        // 响应钩子函数
	RpcInterceptors      []ClientRPCInterceptor      // RPC拦截器
	MultiRPCInterceptors []ClientMultiRPCInterceptor // 多RPC拦截器
	StreamInterceptors   []StreamInterceptor         // 流拦截器
}

// WithClientID 设置客户端ID
func WithClientID(id string) ClientOption {
	return func(o *ClientOpts) {
		o.ClientID = id
	}
}

// WithClientTimeout 设置客户端超时时间
func WithClientTimeout(timeout time.Duration) ClientOption {
	return func(o *ClientOpts) {
		o.Timeout = timeout
	}
}

// WithClientSelectTimeout 设置客户端选择超时时间
func WithClientSelectTimeout(timeout time.Duration) ClientOption {
	return func(o *ClientOpts) {
		o.SelectionTimeout = timeout
	}
}

// WithClientChannelSize 设置客户端通道大小
func WithClientChannelSize(size int) ClientOption {
	return func(o *ClientOpts) {
		o.ChannelSize = size
	}
}

// Request hooks are called as soon as the request is made
// 客户端请求钩子函数的类型
type ClientRequestHook func(ctx context.Context, req proto.Message, info RPCInfo)

func WithClientRequestHooks(hooks ...ClientRequestHook) ClientOption {
	return func(o *ClientOpts) {
		o.RequestHooks = append(o.RequestHooks, hooks...)
	}
}

// Response hooks are called just before responses are returned
// For multi-requests, response hooks are called on every response, and block while executing
// 客户端响应钩子函数的类型
type ClientResponseHook func(ctx context.Context, req proto.Message, info RPCInfo, res proto.Message, err error)

func WithClientResponseHooks(hooks ...ClientResponseHook) ClientOption {
	return func(o *ClientOpts) {
		o.ResponseHooks = append(o.ResponseHooks, hooks...)
	}
}

// ClientRPCInterceptor 客户端RPC拦截器函数的类型
type ClientRPCInterceptor func(info RPCInfo, next ClientRPCHandler) ClientRPCHandler

// ClientRPCHandler 客户端RPC处理函数的类型
type ClientRPCHandler func(ctx context.Context, req proto.Message, opts ...RequestOption) (proto.Message, error)

func WithClientRPCInterceptors(interceptors ...ClientRPCInterceptor) ClientOption {
	return func(o *ClientOpts) {
		o.RpcInterceptors = append(o.RpcInterceptors, interceptors...)
	}
}

// ClientMultiRPCInterceptor 客户端多RPC拦截器函数的类型
type ClientMultiRPCInterceptor func(info RPCInfo, next ClientMultiRPCHandler) ClientMultiRPCHandler

// ClientMultiRPCHandler 客户端多RPC处理接口
type ClientMultiRPCHandler interface {
	Send(ctx context.Context, msg proto.Message, opts ...RequestOption) error
	Recv(msg proto.Message, err error)
	Close()
}

// WithClientMultiRPCInterceptors 设置客户端多RPC拦截器
func WithClientMultiRPCInterceptors(interceptors ...ClientMultiRPCInterceptor) ClientOption {
	return func(o *ClientOpts) {
		o.MultiRPCInterceptors = append(o.MultiRPCInterceptors, interceptors...)
	}
}

// WithClientStreamInterceptors 设置客户端流拦截器
func WithClientStreamInterceptors(interceptors ...StreamInterceptor) ClientOption {
	return func(o *ClientOpts) {
		o.StreamInterceptors = append(o.StreamInterceptors, interceptors...)
	}
}

// WithClientOptions 将多个客户端选项组合成一个选项
func WithClientOptions(opts ...ClientOption) ClientOption {
	return func(o *ClientOpts) {
		for _, opt := range opts {
			opt(o)
		}
	}
}
