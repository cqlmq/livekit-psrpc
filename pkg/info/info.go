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

package info

import (
	"sync"

	"github.com/livekit/psrpc"
)

// ServiceDefinition 服务定义结构体
type ServiceDefinition struct {
	Name    string   // 服务名称
	ID      string   // 服务ID
	Methods sync.Map // 方法集合
}

// MethodInfo 方法信息结构体
type MethodInfo struct {
	AffinityEnabled bool // 是否启用亲和力
	Multi           bool // 是否多路复用
	RequireClaim    bool // 是否需要声明
	Queue           bool // 是否需要队列
}

// RequestInfo 请求信息结构体
type RequestInfo struct {
	psrpc.RPCInfo        // 继承psrpc.RPCInfo
	AffinityEnabled bool // 是否启用亲和力
	RequireClaim    bool // 是否需要声明
	Queue           bool // 是否需要队列
}

// RegisterMethod 注册方法
func (s *ServiceDefinition) RegisterMethod(name string, affinityEnabled, multi, requireClaim, queue bool) {
	s.Methods.Store(name, &MethodInfo{
		AffinityEnabled: affinityEnabled,
		Multi:           multi,
		RequireClaim:    requireClaim,
		Queue:           queue,
	})
}

// GetInfo 获取方法信息（请求信息）
// rpc: 方法名称，也就是某个服务的类别字符串，如livekit.RoomService.CreateRoom ？
// topic: 主题
func (s *ServiceDefinition) GetInfo(rpc string, topic []string) *RequestInfo {
	v, _ := s.Methods.Load(rpc)
	m := v.(*MethodInfo)

	return &RequestInfo{
		RPCInfo: psrpc.RPCInfo{
			Service: s.Name,
			Method:  rpc,
			Topic:   topic,
			Multi:   m.Multi,
		},
		AffinityEnabled: m.AffinityEnabled,
		RequireClaim:    m.RequireClaim,
		Queue:           m.Queue,
	}
}
