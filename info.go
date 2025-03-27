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

// RPCInfo 是一个结构体，定义了RPC信息
type RPCInfo struct {
	Service string   // 服务名称
	Method  string   // 方法名称
	Topic   []string // 主题
	Multi   bool     // 是否多路复用
}
