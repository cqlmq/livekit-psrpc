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

package client

import (
	"context"
	"sync"

	"github.com/frostbyte73/core"

	"github.com/livekit/psrpc"
	"github.com/livekit/psrpc/internal"
	"github.com/livekit/psrpc/internal/bus"
	"github.com/livekit/psrpc/pkg/info"
)

// RPCClient 是一个RPC客户端
type RPCClient struct {
	*info.ServiceDefinition                                        // 服务定义
	psrpc.ClientOpts                                               // 客户端选项
	bus                     bus.MessageBus                         // 消息总线
	mu                      sync.RWMutex                           // 读写锁
	claimRequests           map[string]chan *internal.ClaimRequest // 声明请求
	responseChannels        map[string]chan *internal.Response     // 响应通道
	streamChannels          map[string]chan *internal.Stream       // 流通道
	closed                  core.Fuse                              // 关闭标识
}

// NewRPCClientWithStreams 创建一个带有流的RPC客户端
func NewRPCClientWithStreams(
	sd *info.ServiceDefinition,
	b bus.MessageBus,
	opts ...psrpc.ClientOption,
) (*RPCClient, error) {
	return NewRPCClient(sd, b, append(opts, withStreams())...)
}

// NewRPCClient 创建一个RPC客户端
// 创建一个客户端时，会订阅响应、声明请求、流，
// 然后会启动一个协程，从订阅通道读取消息到本地
// 本地会根据请求ID将消息放入对应的通道中，方便后续根据请求ID获取消息
func NewRPCClient(
	sd *info.ServiceDefinition,
	b bus.MessageBus,
	opts ...psrpc.ClientOption,
) (*RPCClient, error) {
	c := &RPCClient{
		ServiceDefinition: sd,
		ClientOpts:        getClientOpts(opts...),
		bus:               b,
		claimRequests:     make(map[string]chan *internal.ClaimRequest),
		responseChannels:  make(map[string]chan *internal.Response),
		streamChannels:    make(map[string]chan *internal.Stream),
	}
	if c.ClientID != "" {
		c.ID = c.ClientID
	}

	ctx := context.Background()
	// 备注：虽然GetResponseChannel返回了一个对象，但redis,local好像只使用了channel.Legacy, Nats好像只使用了channel.Server
	responses, err := bus.Subscribe[*internal.Response](
		ctx, c.bus, info.GetResponseChannel(c.Name, c.ID), c.ChannelSize,
	)
	if err != nil {
		return nil, err
	}

	claims, err := bus.Subscribe[*internal.ClaimRequest](
		ctx, c.bus, info.GetClaimRequestChannel(c.Name, c.ID), c.ChannelSize,
	)
	if err != nil {
		_ = responses.Close()
		return nil, err
	}

	var streams bus.Subscription[*internal.Stream]
	if c.EnableStreams {
		streams, err = bus.Subscribe[*internal.Stream](
			ctx, c.bus, info.GetStreamChannel(c.Name, c.ID), c.ChannelSize,
		)
		if err != nil {
			_ = responses.Close()
			_ = claims.Close()
			return nil, err
		}
	} else {
		streams = bus.EmptySubscription[*internal.Stream]{}
	}

	go func() {
		closed := c.closed.Watch()
		for {
			select {
			case <-closed: // 关闭熔断器
				_ = claims.Close()
				_ = responses.Close()
				_ = streams.Close()
				return

			case claim := <-claims.Channel(): // 声明请求
				if claim == nil { // 如果声明请求为空，则关闭客户端
					c.Close()
					continue
				}
				c.mu.RLock()
				claimChan, ok := c.claimRequests[claim.RequestId] // 获取声明请求通道
				c.mu.RUnlock()
				if ok {
					claimChan <- claim // 将声明请求放入通道
				}

			case res := <-responses.Channel(): // 响应
				if res == nil {
					c.Close()
					continue
				}
				c.mu.RLock()
				resChan, ok := c.responseChannels[res.RequestId]
				c.mu.RUnlock()
				if ok {
					resChan <- res
				}

			case msg := <-streams.Channel(): // 流，如果空订阅，从 nil 通道读取会永远阻塞（相当于不执行这个分支）
				if msg == nil { // 如果流为空，则关闭客户端
					c.Close()
					continue
				}
				c.mu.RLock()
				streamChan, ok := c.streamChannels[msg.StreamId]
				c.mu.RUnlock()
				if ok {
					streamChan <- msg
				}
			}
		}
	}()

	return c, nil
}

// Close 关闭RPC客户端
func (c *RPCClient) Close() {
	c.closed.Break()
}
