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

package bus

import (
	"context"

	"google.golang.org/protobuf/proto"
)

const (
	DefaultChannelSize = 100 // 默认通道大小
)

// Channel 消息通道
type Channel struct {
	Legacy string // 通用？
	Server string // 服务端使用
	Local  string // 本地使用
}

// MessageBus 消息总线接口定义
type MessageBus interface {
	Publish(ctx context.Context, channel Channel, msg proto.Message) error
	Subscribe(ctx context.Context, channel Channel, channelSize int) (Reader, error)
	SubscribeQueue(ctx context.Context, channel Channel, channelSize int) (Reader, error)
}

type Reader interface {
	read() ([]byte, bool)
	Close() error
}

// Subscribe 订阅消息(泛型版本)
func Subscribe[MessageType proto.Message](
	ctx context.Context,
	bus MessageBus,
	channel Channel,
	channelSize int,
) (Subscription[MessageType], error) {

	sub, err := bus.Subscribe(ctx, channel, channelSize)
	if err != nil {
		return nil, err
	}

	return newSubscription[MessageType](sub, channelSize), nil
}

// SubscribeQueue 订阅队列消息(泛型版本)
func SubscribeQueue[MessageType proto.Message](
	ctx context.Context,
	bus MessageBus,
	channel Channel,
	channelSize int,
) (Subscription[MessageType], error) {

	sub, err := bus.SubscribeQueue(ctx, channel, channelSize)
	if err != nil {
		return nil, err
	}

	return newSubscription[MessageType](sub, channelSize), nil
}
