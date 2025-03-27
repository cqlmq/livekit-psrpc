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
	"context"
	"errors"
	"sync"

	"github.com/frostbyte73/core"
	"golang.org/x/exp/maps"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/psrpc"
	"github.com/livekit/psrpc/internal/bus"
	"github.com/livekit/psrpc/pkg/info"
)

// rpcHandler 是一个接口，定义了close方法
type rpcHandler interface {
	close(force bool)
}

// RPCServer 是一个服务器结构体
type RPCServer struct {
	*info.ServiceDefinition                       // 服务定义(名称、ID、方法集合)
	psrpc.ServerOpts                              // 服务器选项(超时时间、通道大小...)
	bus                     bus.MessageBus        // 消息总线
	mu                      sync.RWMutex          // 读写锁
	handlers                map[string]rpcHandler // 处理程序
	active                  sync.WaitGroup        // 活动
	shutdown                core.Fuse             // 熔断器
}

// NewRPCServer 创建一个RPC服务器
// todo 这个方法在livekit-server中没有直接调用，以后查看调用的位置, 可能通过psrpc?
func NewRPCServer(sd *info.ServiceDefinition, b bus.MessageBus, opts ...psrpc.ServerOption) *RPCServer {
	s := &RPCServer{
		ServiceDefinition: sd,
		ServerOpts:        getServerOpts(opts...),
		bus:               b,
		handlers:          make(map[string]rpcHandler),
	}
	// opts中的ServerID优先级更高
	if s.ServerID != "" {
		s.ID = s.ServerID
	}

	return s
}

// RegisterHandler 注册一个处理程序
func RegisterHandler[RequestType proto.Message, ResponseType proto.Message](
	s *RPCServer, // 服务器
	rpc string, // 方法名称，可能的值？？
	topic []string, // 主题
	svcImpl func(context.Context, RequestType) (ResponseType, error), // 服务实现
	affinityFunc AffinityFunc[RequestType], // 亲和力函数
) error {
	// 如果服务器已经关闭，则返回错误
	if s.shutdown.IsBroken() {
		return psrpc.ErrServerClosed
	}

	// 获取方法信息
	i := s.GetInfo(rpc, topic)

	key := i.GetHandlerKey() // 获取方法的key
	s.mu.RLock()
	_, ok := s.handlers[key] // 检查是否已经存在处理程序
	s.mu.RUnlock()
	if ok {
		return errors.New("handler already exists")
	}

	// create handler
	h, err := newRPCHandler(s, i, svcImpl, s.ChainedInterceptor, affinityFunc)
	if err != nil {
		return err
	}

	s.active.Add(1)
	h.onCompleted = func() {
		s.active.Done()
		s.mu.Lock()
		delete(s.handlers, key)
		s.mu.Unlock()
	}

	s.mu.Lock()
	s.handlers[key] = h
	s.mu.Unlock()

	h.run(s)
	return nil
}

// RegisterStreamHandler 注册一个流处理程序
func RegisterStreamHandler[RequestType proto.Message, ResponseType proto.Message](
	s *RPCServer,
	rpc string,
	topic []string,
	svcImpl func(psrpc.ServerStream[ResponseType, RequestType]) error,
	affinityFunc StreamAffinityFunc,
) error {
	if s.shutdown.IsBroken() {
		return psrpc.ErrServerClosed
	}

	i := s.GetInfo(rpc, topic)

	key := i.GetHandlerKey()
	s.mu.RLock()
	_, ok := s.handlers[key]
	s.mu.RUnlock()
	if ok {
		return errors.New("handler already exists")
	}

	// create handler
	h, err := newStreamRPCHandler(s, i, svcImpl, affinityFunc)
	if err != nil {
		return err
	}

	s.active.Add(1)
	h.onCompleted = func() {
		s.active.Done()
		s.mu.Lock()
		delete(s.handlers, key)
		s.mu.Unlock()
	}

	s.mu.Lock()
	s.handlers[key] = h
	s.mu.Unlock()

	h.run(s)
	return nil
}

// DeregisterHandler 注销一个处理程序
func (s *RPCServer) DeregisterHandler(rpc string, topic []string) {
	i := s.GetInfo(rpc, topic)
	key := i.GetHandlerKey()
	s.mu.RLock()
	h, ok := s.handlers[key]
	s.mu.RUnlock()
	if ok {
		h.close(true)
	}
}

// Publish 发布一个消息
func (s *RPCServer) Publish(ctx context.Context, rpc string, topic []string, msg proto.Message) error {
	i := s.GetInfo(rpc, topic)
	return s.bus.Publish(ctx, i.GetRPCChannel(), msg)
}

// Close 关闭服务器
func (s *RPCServer) Close(force bool) {
	s.shutdown.Once(func() {
		s.mu.RLock()
		handlers := maps.Values(s.handlers)
		s.mu.RUnlock()

		var wg sync.WaitGroup
		for _, h := range handlers {
			wg.Add(1)
			h := h
			go func() {
				h.close(force)
				wg.Done()
			}()
		}
		wg.Wait()
	})

	if !force {
		s.active.Wait()
	}
}
