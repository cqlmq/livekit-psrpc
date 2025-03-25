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
	"crypto/sha256"
	"encoding/base64"
	"math/rand"
	"sync"
	"time"

	"github.com/gammazero/deque"
	"github.com/redis/go-redis/v9"
	"go.uber.org/multierr"
	"golang.org/x/exp/maps"
	"golang.org/x/exp/slices"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/psrpc/internal/logger"
)

const lockExpiration = time.Second * 5              // 锁过期时间
const reconcilerRetryInterval = time.Second         // 重试间隔
const minReadRetryInterval = time.Millisecond * 100 // 最小读取重试间隔
const maxReadRetryInterval = time.Second            // 最大读取重试间隔

type redisMessageBus struct {
	rc              redis.UniversalClient         // redis客户端
	ctx             context.Context               // 上下文
	ps              *redis.PubSub                 // redis的发布/订阅对象
	mu              sync.Mutex                    // 互斥锁
	subs            map[string]*redisSubList      // 订阅者列表
	queues          map[string]*redisSubList      // 队列列表
	wakeup          chan struct{}                 // 唤醒写线程
	ops             *redisWriteOpQueue            // 写操作对象，有点像支持缓存的队列，用于后台写线程
	publishOps      map[string]*redisWriteOpQueue // 发布操作队列
	dirtyChannels   map[string]struct{}           // 脏的主题频道
	currentChannels map[string]struct{}           // 当前的主题频道
}

// NewRedisMessageBus 创建一个redis消息总线
func NewRedisMessageBus(rc redis.UniversalClient) MessageBus {
	ctx := context.Background()
	r := &redisMessageBus{
		rc:     rc,
		ctx:    ctx,
		ps:     rc.Subscribe(ctx),
		subs:   map[string]*redisSubList{},
		queues: map[string]*redisSubList{},

		wakeup:          make(chan struct{}, 1),
		ops:             &redisWriteOpQueue{},
		publishOps:      map[string]*redisWriteOpQueue{},
		dirtyChannels:   map[string]struct{}{},
		currentChannels: map[string]struct{}{},
	}
	go r.readWorker()  // 启动读取工作协程
	go r.writeWorker() // 启动写入工作协程
	return r
}

// Publish 发布消息
// 将消息发布到指定的通道
// 第一次发布消息时，会创建一个发布操作队列，并会触发一个写操作
func (r *redisMessageBus) Publish(_ context.Context, channel Channel, msg proto.Message) error {
	b, err := serialize(msg, "")
	if err != nil {
		return err
	}

	r.mu.Lock()
	ops, ok := r.publishOps[channel.Legacy] // 获取发布操作队列
	if !ok {
		ops = &redisWriteOpQueue{}         // 创建一个新的发布操作队列
		r.publishOps[channel.Legacy] = ops // 将新的发布操作队列添加到发布操作队列列表中
	}
	ops.push(&redisPublishOp{r, channel.Legacy, b}) // 将数据添加到发布对象中
	r.mu.Unlock()

	if !ok {
		// 将一个发布操作（操作包括发布的消息）添加到写操作队列中，并触发写线程执行
		r.enqueueWriteOp(&redisExecPublishOp{r, channel.Legacy, ops})
	}
	return nil
}

// Subscribe 订阅消息
func (r *redisMessageBus) Subscribe(ctx context.Context, channel Channel, size int) (Reader, error) {
	return r.subscribe(ctx, channel.Legacy, size, r.subs, false)
}

// SubscribeQueue 订阅队列消息
func (r *redisMessageBus) SubscribeQueue(ctx context.Context, channel Channel, size int) (Reader, error) {
	return r.subscribe(ctx, channel.Legacy, size, r.queues, true)
}

func (r *redisMessageBus) subscribe(ctx context.Context, channel string, size int, subLists map[string]*redisSubList, queue bool) (Reader, error) {
	ctx, cancel := context.WithCancel(ctx)
	sub := &redisSubscription{
		bus:     r,
		ctx:     ctx,
		cancel:  cancel,
		channel: channel,
		msgChan: make(chan *redis.Message, size),
		queue:   queue,
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	subList, ok := subLists[channel]
	if !ok {
		subList = &redisSubList{}
		subLists[channel] = subList
		r.reconcileSubscriptions(channel)
	}
	subList.subs = append(subList.subs, sub)

	return sub, nil
}

func (r *redisMessageBus) unsubscribe(channel string, queue bool, sub *redisSubscription) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var subLists map[string]*redisSubList
	if queue {
		subLists = r.queues
	} else {
		subLists = r.subs
	}

	subList, ok := subLists[channel]
	if !ok {
		return
	}
	i := slices.Index(subList.subs, sub)
	if i == -1 {
		return
	}

	subList.subs = slices.Delete(subList.subs, i, i+1)

	if len(subList.subs) == 0 {
		delete(subLists, channel)
		r.reconcileSubscriptions(channel)
	}
}

// readWorker 读线程
// 读取消息
// cqlmq 2025-03-24 增加了线程退出机制
func (r *redisMessageBus) readWorker() {
	var delay time.Duration
	for {
		select {
		case <-r.ctx.Done():
			// 收到取消信号，退出工作线程
			return
		default:
			msg, err := r.ps.ReceiveMessage(r.ctx)
			if err != nil {
				// 检查是否是 context 取消导致的错误
				if r.ctx.Err() != nil {
					return
				}

				logger.Error(err, "redis receive message failed")
				time.Sleep(delay)
				if delay *= 2; delay == 0 {
					delay = minReadRetryInterval
				} else if delay > maxReadRetryInterval {
					delay = maxReadRetryInterval
				}
				continue
			}
			delay = 0

			r.mu.Lock()
			if subList, ok := r.subs[msg.Channel]; ok {
				subList.dispatch(msg)
			}
			if subList, ok := r.queues[msg.Channel]; ok {
				subList.dispatchQueue(msg)
			}
			r.mu.Unlock()
		}
	}
}

// reconcileSubscriptions 重新协调订阅频道
// 增加与删除频道后，会调用此方法
// 将内存中的订阅者列表执行到redis中
func (r *redisMessageBus) reconcileSubscriptions(channel string) {
	r.dirtyChannels[channel] = struct{}{}
	r.enqueueWriteOp(&redisReconcileSubscriptionsOp{r})
}

// enqueueWriteOp 将一个写操作添加到队列中，并触发写线程执行
func (r *redisMessageBus) enqueueWriteOp(op redisWriteOp) {
	r.ops.push(op)
	select {
	case r.wakeup <- struct{}{}:
	default:
	}
}

// writeWorker 写线程
// 唤醒时执行动作
func (r *redisMessageBus) writeWorker() {
	for range r.wakeup {
		r.ops.drain()
	}
}

// redisWriteOpQueue 是一个用于存储redis写操作的队列
// 基于deque实现，deque是一个双端队列，支持在队列的两端进行插入和删除操作
// 使用sync.Mutex实现线程安全的操作
type redisWriteOpQueue struct {
	mu  sync.Mutex
	ops deque.Deque[redisWriteOp]
}

// empty 检查队列是否为空
func (q *redisWriteOpQueue) empty() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.ops.Len() == 0
}

// push 将一个写操作添加到队列中
func (q *redisWriteOpQueue) push(op redisWriteOp) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ops.PushBack(op)
}

// drain 从队列中取出所有写操作并执行
func (q *redisWriteOpQueue) drain() {
	q.mu.Lock()
	for q.ops.Len() > 0 {
		op := q.ops.PopFront()
		q.mu.Unlock()
		if err := op.run(); err != nil {
			logger.Error(err, "redis write message failed")
		}
		q.mu.Lock()
	}
	q.mu.Unlock()
}

// redisWriteOp 是一个用于执行redis写操作的接口
type redisWriteOp interface {
	run() error
}

// redisPublishOp 是一个用于发布消息的写操作
// 实现了redisWriteOp接口
type redisPublishOp struct {
	*redisMessageBus
	channel string
	message []byte
}

// run 执行发布操作
func (r *redisPublishOp) run() error {
	return r.rc.Publish(r.ctx, r.channel, r.message).Err()
}

// redisExecPublishOp 是一个用于执行发布操作的写操作
// 实现了redisWriteOp接口
type redisExecPublishOp struct {
	*redisMessageBus
	channel string
	ops2    *redisWriteOpQueue
}

// run 执行发布操作
func (r *redisExecPublishOp) run() error {
	go r.exec()
	return nil
}

// exec 执行发布操作
func (r *redisExecPublishOp) exec() {
	r.mu.Lock()
	for !r.ops2.empty() {
		r.mu.Unlock()
		r.ops2.drain()
		r.mu.Lock()
	}
	delete(r.publishOps, r.channel)
	r.mu.Unlock()
}

// redisReconcileSubscriptionsOp 是一个用于重新协调订阅的写操作
// 实现了redisWriteOp接口
type redisReconcileSubscriptionsOp struct {
	*redisMessageBus
}

// run 执行重新协调订阅操作
// 将内存中的订阅者列表执行到redis中
func (r *redisReconcileSubscriptionsOp) run() error {
	r.mu.Lock()
	for len(r.dirtyChannels) > 0 {
		// 遍历所有脏通道
		subscribe := make(map[string]struct{}, len(r.dirtyChannels))
		unsubscribe := make(map[string]struct{}, len(r.dirtyChannels))
		for c := range r.dirtyChannels {
			_, current := r.currentChannels[c]                // 当前通道是否存在
			desired := r.subs[c] != nil || r.queues[c] != nil // 期望通道是否存在
			if !current && desired {
				subscribe[c] = struct{}{}
			} else if current && !desired {
				unsubscribe[c] = struct{}{}
			}
		}
		maps.Clear(r.dirtyChannels) // 清空脏通道，保留对象变量的引用地址
		r.mu.Unlock()

		var subscribeErr, unsubscribeErr error
		if len(subscribe) != 0 {
			subscribeErr = r.ps.Subscribe(r.ctx, maps.Keys(subscribe)...)
		}
		if len(unsubscribe) != 0 {
			unsubscribeErr = r.ps.Unsubscribe(r.ctx, maps.Keys(unsubscribe)...)
		}

		if err := multierr.Combine(subscribeErr, unsubscribeErr); err != nil {
			logger.Error(err, "redis subscription reconciliation failed")
			time.Sleep(reconcilerRetryInterval)
		}

		r.mu.Lock()
		if subscribeErr != nil {
			maps.Copy(r.dirtyChannels, subscribe)
		} else {
			// 将订阅者列表添加到当前主题频道列表中
			maps.Copy(r.currentChannels, subscribe)
		}
		if unsubscribeErr != nil {
			maps.Copy(r.dirtyChannels, unsubscribe)
		} else {
			// 将订阅者列表从当前主题频道列表中删除
			for c := range unsubscribe {
				delete(r.currentChannels, c)
			}
		}
	}
	r.mu.Unlock()
	return nil
}

// redisSubList 是一个用于存储redis订阅者的列表
type redisSubList struct {
	subs []*redisSubscription // 订阅者列表
	next int                  // 下一个订阅者索引
}

// dispatchQueue 分发队列消息
// 只分发给一个订阅者
func (r *redisSubList) dispatchQueue(msg *redis.Message) {
	if r.next >= len(r.subs) {
		r.next = 0
	}
	r.subs[r.next].write(msg)
	r.next++
}

// dispatch 分发消息
// 读取线程读取到消息后，会调用此方法，将消息分发给所有订阅者
func (r *redisSubList) dispatch(msg *redis.Message) {
	for _, sub := range r.subs {
		sub.write(msg)
	}
}

// redisSubscription 是一个用于存储redis订阅者的结构体
type redisSubscription struct {
	bus     *redisMessageBus
	ctx     context.Context
	cancel  context.CancelFunc
	channel string
	msgChan chan *redis.Message
	queue   bool
}

// write 写入消息
// 原理是读线程将收到的消息写入到消息通道中，再通过本对象的read方法读取消息
func (r *redisSubscription) write(msg *redis.Message) {
	select {
	case r.msgChan <- msg:
	case <-r.ctx.Done():
	}
}

// read 读取消息
func (r *redisSubscription) read() ([]byte, bool) {
	for {
		var msg *redis.Message
		var ok bool
		select {
		case msg, ok = <-r.msgChan:
			if !ok {
				return nil, false
			}
		case <-r.ctx.Done():
			return nil, false
		}

		if r.queue {
			sha := sha256.Sum256([]byte(msg.Payload))
			hash := base64.StdEncoding.EncodeToString(sha[:])
			acquired, err := r.bus.rc.SetNX(r.ctx, hash, rand.Int(), lockExpiration).Result()
			if err != nil || !acquired {
				continue
			}
		}

		return []byte(msg.Payload), true
	}
}

func (r *redisSubscription) Close() error {
	r.cancel()
	r.bus.unsubscribe(r.channel, r.queue, r)
	close(r.msgChan)
	return nil
}
