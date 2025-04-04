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

package server

import (
	"github.com/livekit/psrpc"
	"github.com/livekit/psrpc/internal/bus"
	"github.com/livekit/psrpc/internal/interceptors"
)

// getServerOpts 获取服务器选项
func getServerOpts(opts ...psrpc.ServerOption) psrpc.ServerOpts {
	o := &psrpc.ServerOpts{
		Timeout:     psrpc.DefaultServerTimeout, // 默认服务器超时时间
		ChannelSize: bus.DefaultChannelSize,     // 默认通道大小
	}
	for _, opt := range opts {
		opt(o) // 把opt中的值赋给o
	}

	// 链式拦截器
	o.ChainedInterceptor = interceptors.ChainServerInterceptors(o.Interceptors)
	return *o
}
