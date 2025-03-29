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
	"time"

	"golang.org/x/exp/slices"
)

// RequestOption 请求选项函数类型
type RequestOption func(*RequestOpts)

// RequestOpts 请求选项结构体
type RequestOpts struct {
	Timeout       time.Duration
	SelectionOpts SelectionOpts
	Interceptors  []any
}

// SelectionOpts 选择选项结构体
type SelectionOpts struct {
	MinimumAffinity      float32                        // 最小亲和力，用于判断服务器是否有效
	MaximumAffinity      float32                        // 最大亲和力，如果大于0，任何返回最大分数的服务器将立即被选择
	AcceptFirstAvailable bool                           // 快速接受第一个可用的服务器
	AffinityTimeout      time.Duration                  // 服务器选择截止时间
	ShortCircuitTimeout  time.Duration                  // 收到第一个响应后的截止时间
	SelectionFunc        func([]*Claim) (string, error) // 自定义服务器选择函数
}

// Claim 声明结构体
type Claim struct {
	ServerID string  // 服务器ID
	Affinity float32 // 亲和力
}

// WithRequestTimeout 设置请求超时时间
func WithRequestTimeout(timeout time.Duration) RequestOption {
	return func(o *RequestOpts) {
		o.Timeout = timeout
	}
}

// WithSelectionOpts 设置选择选项
func WithSelectionOpts(opts SelectionOpts) RequestOption {
	return func(o *RequestOpts) {
		o.SelectionOpts = opts
	}
}

// RequestInterceptor 请求拦截器接口
type RequestInterceptor interface {
	ClientRPCInterceptor | ClientMultiRPCInterceptor | StreamInterceptor // 请求拦截器接口
}

// WithRequestInterceptors 设置请求拦截器
func WithRequestInterceptors[T RequestInterceptor](interceptors ...T) RequestOption {
	return func(o *RequestOpts) {
		o.Interceptors = slices.Grow(o.Interceptors, len(interceptors))
		for _, i := range interceptors {
			o.Interceptors = append(o.Interceptors, i)
		}
	}
}
