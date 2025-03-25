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
	"sync"

	"google.golang.org/protobuf/proto"
)

// 本地的消息总线，使用于单机模式，基于内存，不支持持久化。
type localMessageBus struct {
	sync.RWMutex
	subs   map[string]*localSubList // 订阅者列表
	queues map[string]*localSubList // 队列列表
}

// NewLocalMessageBus 创建一个本地的消息总线。
func NewLocalMessageBus() MessageBus {
	return &localMessageBus{
		subs:   make(map[string]*localSubList),
		queues: make(map[string]*localSubList),
	}
}

// Publish 发布一个消息到指定的通道。
func (l *localMessageBus) Publish(_ context.Context, channel Channel, msg proto.Message) error {
	// 序列化消息到字节数组。
	b, err := serialize(msg, "")
	if err != nil {
		return err
	}

	// 获取订阅者列表
	l.RLock()
	subs := l.subs[channel.Legacy] // 订阅者列表
	queues := l.queues[channel.Legacy]
	l.RUnlock()

	if subs != nil {
		subs.dispatch(b)
	}
	if queues != nil {
		queues.dispatch(b)
	}
	return nil
}

// Subscribe 订阅一个消息通道
// size 是消息通道的缓冲区大小
func (l *localMessageBus) Subscribe(ctx context.Context, channel Channel, size int) (Reader, error) {
	return l.subscribe(ctx, l.subs, channel.Legacy, size, false)
}

// SubscribeQueue 订阅一个队列消息通道
// size 是消息通道的缓冲区大小
func (l *localMessageBus) SubscribeQueue(ctx context.Context, channel Channel, size int) (Reader, error) {
	return l.subscribe(ctx, l.queues, channel.Legacy, size, true)
}

// subscribe 订阅一个消息通道，并使用队列发送消息
// todo 感觉可以通过queue参数来区分是否是队列，而不需要l.subs参数
func (l *localMessageBus) subscribe(ctx context.Context, subLists map[string]*localSubList, channel string, size int, queue bool) (Reader, error) {
	l.Lock()
	defer l.Unlock()

	subList := subLists[channel]
	if subList == nil {
		// 创建一个订阅者列表
		subList = &localSubList{queue: queue}
		subList.onUnsubscribe = func(index int) {
			// lock localMessageBus before localSubList
			// 通过这个例子了解：在子对象中锁定上级对象的技巧
			l.Lock()
			subList.Lock()

			subList.subs[index] = nil // 取消订阅时只是将订阅者设置为 nil，减少数组变化
			subList.subCount--        // 订阅者数量减1
			if subList.subCount == 0 {
				delete(subLists, channel) // 如果订阅者数量为0，则删除订阅者列表
			}

			subList.Unlock()
			l.Unlock()
		}
		subLists[channel] = subList
	}

	// 创建一个本地订阅者，并添加到订阅者列表中，并返回订阅者
	return subList.create(ctx, size), nil
}

// 本地订阅者列表
type localSubList struct {
	sync.RWMutex                       // 在持有 localMessageBus 锁时允许锁定
	subs          []*localSubscription // 订阅者列表
	subCount      int                  // 订阅者数量
	queue         bool                 // 是否是队列
	next          int                  // 下一个订阅者索引（用于队列，轮询发送消息）
	onUnsubscribe func(int)            // 取消订阅时的回调函数
}

// create 创建一个本地订阅者，并添加到订阅者列表中，并返回订阅者
func (l *localSubList) create(ctx context.Context, size int) *localSubscription {
	// 创建一个订阅者对象
	ctx, cancel := context.WithCancel(ctx)
	sub := &localSubscription{
		ctx:     ctx,
		cancel:  cancel,
		msgChan: make(chan []byte, size),
	}

	l.Lock()
	defer l.Unlock()

	// 增加订阅者数量
	l.subCount++

	// 添加订阅者到订阅者列表
	added := false
	index := 0
	for i, s := range l.subs {
		if s == nil {
			added = true
			index = i
			l.subs[i] = sub
			break
		}
	}

	// 如果订阅者列表中没有空闲的订阅者，则添加到订阅者列表的末尾
	if !added {
		index = len(l.subs)
		l.subs = append(l.subs, sub)
	}

	// 设置订阅者的关闭回调函数，当订阅者关闭时，调用 onUnsubscribe 回调函数，达到取消订阅的效果
	sub.onClose = func() {
		l.onUnsubscribe(index)
	}

	return sub
}

// dispatch 分发消息到订阅者列表
func (l *localSubList) dispatch(b []byte) {
	if l.queue {
		l.Lock()
		defer l.Unlock()

		// 修复了为空数组时的bug
		// 修复了全空死循环的bug
		// 可能因为逻辑运行时一定有一个订阅者？ 以至于目前都没有触发
		// cqlmq 2025-03-24
		if len(l.subs) == 0 {
			return
		}

		// 记录起始位置，防止完整遍历一圈后还找不到有效订阅者
		startIndex := l.next
		for {
			if l.next >= len(l.subs) {
				l.next = 0
			}
			s := l.subs[l.next]
			l.next++
			if s != nil {
				s.write(b)
				return
			}

			// 如果已经遍历了一圈还没找到有效订阅者，就退出
			if l.next == startIndex {
				return
			}
		}
	} else {
		l.RLock()
		defer l.RUnlock()

		// 发送消息到所有订阅者
		for _, s := range l.subs {
			if s != nil {
				s.write(b)
			}
		}
	}
}

// 本地订阅者
// 提供读写消息的能力，也支持关闭，回调
type localSubscription struct {
	ctx     context.Context    // 上下文
	cancel  context.CancelFunc // 取消上下文
	msgChan chan []byte        // 消息通道
	onClose func()             // 关闭时的回调函数
}

func (l *localSubscription) write(b []byte) {
	select {
	case l.msgChan <- b:
	case <-l.ctx.Done():
	}
}

func (l *localSubscription) read() ([]byte, bool) {
	msg, ok := <-l.msgChan
	if !ok {
		return nil, false
	}
	return msg, true
}

func (l *localSubscription) Close() error {
	l.cancel()
	l.onClose()
	close(l.msgChan)
	return nil
}
