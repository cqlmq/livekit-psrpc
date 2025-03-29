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

package stream

import (
	"context"
	"errors"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/livekit/psrpc"
	"github.com/livekit/psrpc/internal"
	"github.com/livekit/psrpc/internal/bus"
	"github.com/livekit/psrpc/internal/interceptors"
	"github.com/livekit/psrpc/internal/logger"
	"github.com/livekit/psrpc/pkg/info"
	"github.com/livekit/psrpc/pkg/rand"
)

// 外部接收消息
// -> HandleStream
// -> 解码消息
// -> 通过 recvChan 传递给外部
// -> 外部处理消息

// Stream 的主要职责：
// 处理 Stream_Ack：确认机制
// 处理 Stream_Close：关闭机制
// 处理 Stream_Message：
// 解码消息
// 通过 recvChan 传递给外部
// 发送确认消息
// recvChan 的作用：
// 作为 Stream 和外部之间的桥梁
// 将解码后的消息传递给外部处理
// 提供异步处理机制
// 所以 Stream 确实是一个加工器，它：
// 处理了底层的消息格式（Ack、Close、Message）
// 提供了消息的编解码
// 实现了确认机制
// 通过 recvChan 将处理后的消息传递给外部

// 发送方 -> 发送消息 -> 接收方 -> 处理消息 -> 发送确认 -> 发送方收到确认

// Stream 流处理
type Stream[SendType, RecvType proto.Message] interface {
	psrpc.ServerStream[SendType, RecvType] // 继承ServerStream（根目录的types.go中定义）

	// internal,pkg中的包会使用? 外部包应该只使用psrpc.ServerStream？
	Ack(context.Context, *internal.Stream) error // 确认
	HandleStream(is *internal.Stream) error      // 处理流
	Hijacked() bool                              // 是否被劫持
}

// StreamAdapter 流处理适配器
type StreamAdapter interface {
	Send(ctx context.Context, msg *internal.Stream) error // 发送
	Close(streamID string)                                // 关闭
}

// stream 流处理实现类
type stream[SendType, RecvType proto.Message] struct {
	*streamBase[SendType, RecvType]

	handler  psrpc.StreamHandler // 流处理程序(包括拦截) （Send Recv Close）
	hijacked bool                // 好像只是给流添加一个状态，表示流是否被劫持，怎么使用还得看外部？
}

// streamBase 流处理基类
type streamBase[SendType, RecvType proto.Message] struct {
	psrpc.StreamOpts // 继承StreamOpts (Timeout time.Duration)

	ctx      context.Context    // 上下文
	cancel   context.CancelFunc // 取消
	streamID string             // 流ID

	adapter  StreamAdapter // 适配器 （Send Close）
	recvChan chan RecvType // 接收通道

	mu      sync.Mutex               // 互斥锁
	pending sync.WaitGroup           // 等待组 防止在消息处理过程中过早关闭流
	acks    map[string]chan struct{} // 确认通道
	closed  bool                     // 是否关闭
	err     error                    // 错误
}

// NewStream 创建一个流式处理
func NewStream[SendType, RecvType proto.Message](
	ctx context.Context,
	i *info.RequestInfo,
	streamID string,
	timeout time.Duration,
	adapter StreamAdapter,
	streamInterceptors []psrpc.StreamInterceptor,
	recvChan chan RecvType,
	acks map[string]chan struct{},
) Stream[SendType, RecvType] {

	ctx, cancel := context.WithCancel(ctx)
	base := &streamBase[SendType, RecvType]{
		StreamOpts: psrpc.StreamOpts{Timeout: timeout},
		ctx:        ctx,
		cancel:     cancel,
		streamID:   streamID,
		adapter:    adapter,
		recvChan:   recvChan,
		acks:       acks,
	}

	return &stream[SendType, RecvType]{
		streamBase: base,
		handler: interceptors.ChainClientInterceptors[psrpc.StreamHandler](
			streamInterceptors, i, base,
		), // 拦截器链 注意这里注入了base，stream会（能够）调用base的方法
	}
}

// HandleStream 处理流数据
func (s *stream[SendType, RecvType]) HandleStream(is *internal.Stream) error {
	switch b := is.Body.(type) {
	case *internal.Stream_Ack: // 收到对方的确认消息
		s.mu.Lock()
		ack, ok := s.acks[is.RequestId]
		delete(s.acks, is.RequestId)
		s.mu.Unlock()

		if ok {
			close(ack) // 关闭确认通道, 通知Send函数已经收到确认消息
		}

	case *internal.Stream_Message:
		// 添加等待组
		if err := s.addPending(); err != nil {
			return err // 如果关闭流后，返回错误，不再处理消息
		}
		defer s.pending.Done() // 处理完消息后，减少等待组计数

		// 解码
		v, err := bus.DeserializePayload[RecvType](b.Message.RawMessage)
		if err != nil {
			err = psrpc.NewError(psrpc.MalformedRequest, err)
			go func() {
				if e := s.handler.Close(err); e != nil {
					logger.Error(e, "failed to close stream")
				}
			}()
			return err
		}

		// 处理消息
		if err := s.handler.Recv(v); err != nil {
			return err
		}

		// 发送确认消息（消息已处理？）
		ctx, cancel := context.WithDeadline(s.ctx, time.Unix(0, is.Expiry))
		defer cancel()
		if err := s.Ack(ctx, is); err != nil {
			return err
		}

	case *internal.Stream_Close:
		cause := psrpc.NewErrorFromResponse(b.Close.Code, b.Close.Error)
		if err := s.setClosed(cause); err != nil {
			return err
		}

		s.adapter.Close(s.streamID)
		s.cancel()
		close(s.recvChan)
	}

	return nil
}

func (s *stream[SendType, RecvType]) Context() context.Context {
	return s.ctx
}

func (s *stream[SendType, RecvType]) Channel() <-chan RecvType {
	return s.recvChan
}

// Ack 确认
func (s *stream[SendType, RecvType]) Ack(ctx context.Context, is *internal.Stream) error {
	return s.adapter.Send(ctx, &internal.Stream{
		StreamId:  is.StreamId,
		RequestId: is.RequestId,
		SentAt:    is.SentAt,
		Expiry:    is.Expiry,
		Body: &internal.Stream_Ack{
			Ack: &internal.StreamAck{},
		},
	})
}

// Recv 接收 （外部调用, 将传入处理的有用消息通过通道返回给外部）
func (s *stream[SendType, RecvType]) Recv(msg proto.Message) error {
	return s.handler.Recv(msg)
}

// 接收消息
func (s *streamBase[SendType, RecvType]) Recv(msg proto.Message) error {
	select {
	case s.recvChan <- msg.(RecvType):
	default:
		return psrpc.ErrSlowConsumer
	}
	return nil
}

func (s *stream[SendType, RecvType]) Send(request SendType, opts ...psrpc.StreamOption) (err error) {
	return s.handler.Send(request, opts...)
}

// Send 发送
func (s *streamBase[SendType, RecvType]) Send(msg proto.Message, opts ...psrpc.StreamOption) (err error) {
	if err := s.addPending(); err != nil {
		return err
	}
	defer s.pending.Done()

	o := getStreamOpts(s.StreamOpts, opts...)

	b, err := bus.SerializePayload(msg)
	if err != nil {
		err = psrpc.NewError(psrpc.MalformedRequest, err)
		return
	}

	ackChan := make(chan struct{})
	requestID := rand.NewRequestID()

	s.mu.Lock()
	s.acks[requestID] = ackChan // 保存确认通道，用于接收对方的确认消息
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.acks, requestID)
		s.mu.Unlock()
	}()

	now := time.Now()
	deadline := now.Add(o.Timeout)

	ctx, cancel := context.WithDeadline(s.ctx, deadline)
	defer cancel()

	err = s.adapter.Send(ctx, &internal.Stream{
		StreamId:  s.streamID,
		RequestId: requestID,
		SentAt:    now.UnixNano(),
		Expiry:    deadline.UnixNano(),
		Body: &internal.Stream_Message{
			Message: &internal.StreamMessage{
				RawMessage: b,
			},
		},
	})
	if err != nil {
		return
	}

	select {
	case <-ackChan:
	case <-ctx.Done():
		select {
		case <-s.ctx.Done():
			err = s.Err()
		default:
			err = psrpc.ErrRequestTimedOut
		}
	}

	return
}

// Hijack 劫持
func (s *stream[SendType, RecvType]) Hijack() {
	s.mu.Lock()
	s.hijacked = true
	s.mu.Unlock()
}

func (s *stream[SendType, RecvType]) Hijacked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hijacked
}

func (s *stream[RequestType, ResponseType]) Close(cause error) error {
	return s.handler.Close(cause)
}

// Close 关闭
// 控制只能真正关闭一次，关闭会发送关闭消息给对方
func (s *streamBase[RequestType, ResponseType]) Close(cause error) error {
	if cause == nil {
		cause = psrpc.ErrStreamClosed
	}

	if err := s.setClosed(cause); err != nil {
		return err
	}

	msg := &internal.StreamClose{}
	var e psrpc.Error
	if errors.As(cause, &e) {
		msg.Error = e.Error()
		msg.Code = string(e.Code())
	} else {
		msg.Error = cause.Error()
		msg.Code = string(psrpc.Unknown)
	}

	now := time.Now()
	err := s.adapter.Send(context.Background(), &internal.Stream{
		StreamId:  s.streamID,
		RequestId: rand.NewRequestID(),
		SentAt:    now.UnixNano(),
		Expiry:    now.Add(s.Timeout).UnixNano(),
		Body: &internal.Stream_Close{
			Close: msg,
		},
	})

	s.pending.Wait()            // 等待所有消息处理完成
	s.adapter.Close(s.streamID) // 关闭流
	s.cancel()                  // 取消上下文
	close(s.recvChan)           // 关闭接收通道

	return err
}

// addPending 添加等待组
func (s *streamBase[SendType, RecvType]) addPending() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.pending.Add(1)
	}
	return s.err
}

func (s *streamBase[SendType, RecvType]) setClosed(cause error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.err
	}

	s.closed = true
	s.err = cause
	return nil
}

func (s *streamBase[SendType, RecvType]) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
